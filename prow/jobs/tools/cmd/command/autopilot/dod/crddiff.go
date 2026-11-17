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

package dod

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	crdCompatSchema   = "ack.crd-compat/v1"
	maxCRDCompatBytes = 64 << 20
	// changeUncompared is Plan 4's residual change type: a difference kro does
	// not compare. It is never additive (owner decision).
	changeUncompared = "UNCOMPARED_CHANGED"
)

// crdBuckets are Plan 4's 13 bucket names.
var crdBuckets = []string{
	"added_optional_spec", "added_status", "added_required", "type_changed", "removed", "enum_changed",
	"validation_tightened", "validation_relaxed", "default_changed", "description_only", "crd_added", "crd_removed", "other",
}

type crdChange struct {
	File       string `json:"file"`
	Kind       string `json:"kind,omitempty"`
	Bucket     string `json:"bucket"`
	Field      string `json:"field,omitempty"`
	Path       string `json:"path,omitempty"`
	ChangeType string `json:"change_type"`
	Old        string `json:"old,omitempty"`
	New        string `json:"new,omitempty"`
	Breaking   bool   `json:"breaking"`
}

type crdFile struct {
	File   string `json:"file"`
	Kind   string `json:"kind,omitempty"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

type crdCandidate struct {
	File  string `json:"file"`
	Kind  string `json:"kind,omitempty"`
	Field string `json:"field"`
}

// crdCompat mirrors code-generator pkg/crdcompat.Result (ack.crd-compat/v1).
type crdCompat struct {
	Schema              string                 `json:"schema"`
	BaseRef             string                 `json:"base_ref"`
	Breaking            bool                   `json:"breaking"`
	Files               []crdFile              `json:"files"`
	Buckets             map[string][]crdChange `json:"buckets"`
	SensitiveCandidates []crdCandidate         `json:"sensitive_candidates"`
}

func parseCRDCompat(data []byte) (*crdCompat, error) {
	if len(data) > maxCRDCompatBytes {
		return nil, fmt.Errorf("crd-compat document is larger than %d bytes", maxCRDCompatBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var d crdCompat
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("decode crd-compat document: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("decode crd-compat document: data after the object")
	}
	if d.Schema != crdCompatSchema {
		return nil, fmt.Errorf("crd-compat schema %q, want %q", d.Schema, crdCompatSchema)
	}
	return &d, nil
}

var additiveBuckets = map[string]bool{
	"added_optional_spec": true, "added_status": true, "description_only": true, "validation_relaxed": true, "enum_changed": true,
}

// breakingBuckets break existing custom resources in every class.
var breakingBuckets = map[string]bool{"added_required": true, "type_changed": true, "removed": true, "crd_removed": true}

func additive(c crdChange) bool {
	return additiveBuckets[c.Bucket] && !c.Breaking && c.ChangeType != changeUncompared
}

type crdMode int

const (
	crdAdditive crdMode = iota
	crdDescriptionOnly
	crdReview
)

func crdModeFor(class string) crdMode {
	switch class {
	case ClassDocsOnly:
		return crdDescriptionOnly
	case ClassConfigOnly, ClassHandWritten, ClassNewResource:
		return crdReview
	}
	return crdAdditive
}

func changeDetail(c crdChange) string {
	what := c.Field
	if what == "" {
		what = c.Kind
	}
	return c.Bucket + " " + c.ChangeType + " " + what
}

// checkCRDDiff applies the class's CRD rule (decisions 8 and 9) to a Plan 4
// document. docBase is the document's base_ref resolved in the repo ("" when
// it does not resolve); it must be the merge base dod diffs against.
func checkCRDDiff(class string, d *crdCompat, docBase, mergeBase string) Check {
	var findings []Finding
	if docBase != mergeBase {
		findings = append(findings, Finding{Signature: "CRD_COMPAT_BASE_MISMATCH", Detail: "document base_ref " + d.BaseRef, Fails: true})
	}
	known := map[string]bool{}
	for _, b := range crdBuckets {
		known[b] = true
	}
	mode, reclass, anyBreaking := crdModeFor(class), "", false
	for _, b := range sortedKeys(d.Buckets) {
		for _, c := range d.Buckets[b] {
			anyBreaking = anyBreaking || c.Breaking
			if c.Bucket != b {
				findings = append(findings, Finding{Signature: "CRD_COMPAT_INCONSISTENT", File: c.File, Detail: "change in " + b + " says " + c.Bucket, Fails: true})
				c.Bucket = b // already failing; judge the change by its map key
			}
			f := Finding{File: c.File, Detail: changeDetail(c), Fails: true}
			switch mode {
			case crdAdditive:
				if known[b] && additive(c) {
					continue
				}
				f.Signature, reclass = "CRD_NOT_ADDITIVE", atLeast(reclass, ClassBreakingAPI)
			case crdDescriptionOnly:
				if b == "description_only" && additive(c) {
					continue
				}
				f.Signature = "CRD_NOT_DESCRIPTION_ONLY"
				if known[b] && additive(c) {
					reclass = atLeast(reclass, ClassSDKBump)
				} else {
					reclass = atLeast(reclass, ClassBreakingAPI)
				}
			case crdReview:
				switch {
				case known[b] && additive(c):
					continue
				case b == "crd_added" && !c.Breaking:
					if class == ClassNewResource {
						continue
					}
					f.Signature, reclass = "CRD_ADDED", atLeast(reclass, ClassNewResource)
				case !known[b] || breakingBuckets[b] || (c.Breaking && c.ChangeType != changeUncompared):
					f.Signature, reclass = "CRD_BREAKING", atLeast(reclass, ClassBreakingAPI)
				default: // validation_tightened, other, default_changed, uncompared
					f.Signature, f.Fails = "CRD_NEEDS_JUDGEMENT", false
				}
			}
			findings = append(findings, f)
		}
	}
	if anyBreaking != d.Breaking {
		findings = append(findings, Finding{Signature: "CRD_COMPAT_INCONSISTENT", Detail: "breaking flag disagrees with the changes", Fails: true})
	}
	c := newCheck(CheckCRDDiff, findings)
	c.Reclassify = reclass
	return c
}
