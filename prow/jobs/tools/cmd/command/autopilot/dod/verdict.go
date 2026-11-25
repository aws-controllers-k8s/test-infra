// Copyright Amazon.com Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//     http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

// Package dod is ACK Autopilot's Definition-of-Done gate runner (design §3
// row 5). It checks one controller PR without an LLM or credentials and writes
// a reproducible verdict document.
package dod

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
)

// Schema names the verdict document.
const Schema = "ack.autopilot.verdict/v1"

// ToolName is recorded in every verdict.
const ToolName = "ack-build-tools autopilot dod"

// TrailerKey is the provenance trailer that carries a verdict's digest (§4.4).
const TrailerKey = "Gate-Verdict"

const maxVerdictBytes = 4 << 20

// Status is a check's outcome; a verdict is only ever pass or fail.
type Status string

const (
	StatusPass Status = "pass"
	StatusFail Status = "fail"
	StatusSkip Status = "skip"
)

// Finding is one observation. Fails marks the findings that fail their check;
// the others are inventory. Detail never holds a secret value.
type Finding struct {
	Signature string `json:"signature"`
	File      string `json:"file,omitempty"`
	Line      int    `json:"line,omitempty"`
	Detail    string `json:"detail,omitempty"`
	Fails     bool   `json:"fails"`
}

// Check is one gate's result. Reclassify is the least change class its
// findings require; Labels are PR labels it asks for.
type Check struct {
	ID         string    `json:"id"`
	Binding    bool      `json:"binding"`
	Status     Status    `json:"status"`
	Reasons    []string  `json:"reasons"`
	Reclassify string    `json:"reclassify,omitempty"`
	Labels     []string  `json:"labels"`
	Findings   []Finding `json:"findings"`
}

type Tool struct {
	Name     string `json:"name"`
	Revision string `json:"revision"`
}

// Subject is what the verdict is about: the merge base and the head tree.
type Subject struct {
	Repo     string `json:"repo"`
	Service  string `json:"service"`
	BaseSHA  string `json:"base_sha"`
	HeadTree string `json:"head_tree"`
}

// Inputs records everything besides the subject that the checks read.
type Inputs struct {
	ChangeClass     string   `json:"change_class"`
	WorkItem        string   `json:"work_item"`
	CodegenSHA      string   `json:"codegen_sha"`
	RuntimeSHA      string   `json:"runtime_sha"`
	CRDCompatSource string   `json:"crd_compat_source"`
	CRDCompatSHA256 string   `json:"crd_compat_sha256"`
	SDKModel        string   `json:"sdk_model"`
	SDKModelSHA256  string   `json:"sdk_model_sha256"`
	SkippedByFlag   []string `json:"skipped_by_flag"`
}

// Verdict is the ack.autopilot.verdict/v1 document.
type Verdict struct {
	Schema       string   `json:"schema"`
	Tool         Tool     `json:"tool"`
	Subject      Subject  `json:"subject"`
	Inputs       Inputs   `json:"inputs"`
	Verdict      Status   `json:"verdict"`
	Reasons      []string `json:"reasons"`
	ReclassifyTo string   `json:"reclassify_to"`
	Labels       []string `json:"labels"`
	HumanCommits int      `json:"human_commits"`
	NotCovered   []string `json:"not_covered"`
	Checks       []Check  `json:"checks"`
}

// newCheck builds a check from its findings: fail when any finding fails.
// Reasons are the failing signatures, sorted and unique.
func newCheck(id string, findings []Finding) Check {
	c := Check{ID: id, Status: StatusPass, Findings: findings}
	for _, f := range findings {
		if f.Fails {
			c.Status = StatusFail
			c.Reasons = append(c.Reasons, f.Signature)
		}
	}
	c.Reasons = sortedUnique(c.Reasons)
	return c
}

// skipCheck is a check that did not run, and why.
func skipCheck(id, reason string) Check {
	return Check{ID: id, Status: StatusSkip, Reasons: []string{reason}}
}

func sortedUnique(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	out = slices.Compact(out)
	if out == nil {
		out = []string{}
	}
	return out
}

func (c *Check) normalize() {
	c.Reasons = sortedUnique(c.Reasons)
	c.Labels = sortedUnique(c.Labels)
	slices.SortFunc(c.Findings, func(a, b Finding) int {
		return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line),
			cmp.Compare(a.Signature, b.Signature), cmp.Compare(a.Detail, b.Detail),
			cmp.Compare(fmt.Sprint(a.Fails), fmt.Sprint(b.Fails)))
	})
	c.Findings = slices.Compact(c.Findings)
	if c.Findings == nil {
		c.Findings = []Finding{}
	}
}

// Marshal sorts every list and returns the document's canonical bytes, which
// Digest hashes. The check order is kept (decide sets it).
func Marshal(v *Verdict) ([]byte, error) {
	for i := range v.Checks {
		v.Checks[i].normalize()
	}
	if v.Checks == nil {
		v.Checks = []Check{}
	}
	v.Reasons = sortedUnique(v.Reasons)
	v.Labels = sortedUnique(v.Labels)
	v.NotCovered = sortedUnique(v.NotCovered)
	v.Inputs.SkippedByFlag = sortedUnique(v.Inputs.SkippedByFlag)
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Digest is "sha256:<64 hex>" of the exact bytes.
func Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Trailer is the Gate-Verdict trailer line for a verdict's bytes.
func Trailer(b []byte) string { return TrailerKey + ": " + Digest(b) }

// ParseVerdict decodes a verdict strictly: unknown keys, trailing data, another
// schema or a verdict other than pass/fail are errors.
func ParseVerdict(data []byte) (*Verdict, error) {
	if len(data) > maxVerdictBytes {
		return nil, fmt.Errorf("verdict is larger than %d bytes", maxVerdictBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var v Verdict
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("decode verdict: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("decode verdict: data after the object")
	}
	if v.Schema != Schema {
		return nil, fmt.Errorf("verdict schema %q, want %q", v.Schema, Schema)
	}
	if v.Verdict != StatusPass && v.Verdict != StatusFail {
		return nil, fmt.Errorf("verdict %q is neither pass nor fail", v.Verdict)
	}
	return &v, nil
}

// TrailerValue returns the Gate-Verdict value in the last trailer block of a
// PR body or commit message.
func TrailerValue(text []byte) (string, bool) {
	vs := parseTrailers(string(text))[TrailerKey]
	if len(vs) == 0 {
		return "", false
	}
	return vs[len(vs)-1], true
}

var trailerLine = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9-]*):[ \t]*(.*?)[ \t]*$`)

// parseTrailers returns the trailers in a message's last paragraph, or nil
// when the message has one paragraph or any line of the last one is not a
// "Key: value" line (git's rule, without continuation lines).
func parseTrailers(msg string) map[string][]string {
	paras := strings.Split(strings.TrimSpace(strings.ReplaceAll(msg, "\r\n", "\n")), "\n\n")
	if len(paras) < 2 {
		return nil
	}
	out := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSpace(paras[len(paras)-1]), "\n") {
		m := trailerLine.FindStringSubmatch(line)
		if m == nil {
			return nil
		}
		out[m[1]] = append(out[m[1]], m[2])
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
