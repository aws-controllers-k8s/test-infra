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

package dispatcher

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

// TriageSchema is the triage verdict inside agent-result.json (design 5.1),
// written by prow/agent-workflows/agents/workflows/triage.py. Both sides
// test against testdata/agent-result-triage-v1.json.
const TriageSchema = "ack.autopilot.triage/v1"

// TriageVerdict is one item's checked triage verdict.
type TriageVerdict struct {
	Decision     string
	Reason       string
	EvidenceRefs []string
	Invalid      []string
}

type triageFile struct {
	Schema string            `json:"schema"`
	Mode   string            `json:"mode"`
	Items  []json.RawMessage `json:"items"`
}

type triageFileItem struct {
	ItemKey      string   `json:"item_key"`
	Decision     string   `json:"decision"`
	Class        *string  `json:"class"`
	Reason       *string  `json:"reason"`
	EvidenceRefs []string `json:"evidence_refs"`
	Invalid      []string `json:"invalid"`
}

// ParseTriage checks a triage run's verdict for the item keyHash, strictly.
// Unknown keys, another item, a decision outside the mode-api enum, a class
// that is not the decision's, a malformed reference or invalid code: each
// makes the verdict undecided, with the problem as its only Invalid code.
// The untrusted-input firewall (design 5.2) fails closed to "a maintainer
// decides", never to a retry that spends more tokens. References are checked
// for form only: the job checked that each exists in the SDK model or the
// CRD, and the Dispatcher has neither.
func ParseTriage(raw json.RawMessage, keyHash string) TriageVerdict {
	undecided := func(code string) TriageVerdict {
		return TriageVerdict{Decision: ledger.DecisionUndecided, Invalid: []string{code}}
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return undecided("TRIAGE_MISSING")
	}
	var f triageFile
	if err := strictDecode(raw, &f); err != nil {
		return undecided("TRIAGE_MALFORMED")
	}
	switch {
	case f.Schema != TriageSchema:
		return undecided("TRIAGE_SCHEMA")
	case f.Mode != ledger.TriageModeAPI:
		return undecided("TRIAGE_MODE")
	case len(f.Items) != 1:
		return undecided("TRIAGE_ITEM_COUNT")
	}
	var it triageFileItem
	if err := strictDecode(f.Items[0], &it); err != nil {
		return undecided("TRIAGE_MALFORMED")
	}
	if it.ItemKey != keyHash {
		return undecided("TRIAGE_ITEM_KEY")
	}
	if !slices.Contains(ledger.APIDecisions, it.Decision) {
		return undecided("UNKNOWN_DECISION")
	}
	want, hasClass := ledger.DecisionClass[it.Decision]
	if (it.Class != nil) != hasClass || (it.Class != nil && *it.Class != want) {
		return undecided("CLASS_MISMATCH")
	}
	if it.Reason == nil || it.EvidenceRefs == nil || it.Invalid == nil {
		return undecided("TRIAGE_MALFORMED")
	}
	if len(it.EvidenceRefs) > ledger.MaxEvidenceRefs || len(it.Invalid) > ledger.MaxEvidenceRefs {
		return undecided("TRIAGE_TOO_LONG")
	}
	for _, r := range it.EvidenceRefs {
		if !ledger.ValidEvidenceRef(r) {
			return undecided("MALFORMED_REF")
		}
	}
	for _, c := range it.Invalid {
		if !ledger.ValidInvalidCode(c) {
			return undecided("MALFORMED_INVALID")
		}
	}
	if len(it.Invalid) > 0 && it.Decision != ledger.DecisionUndecided {
		return undecided("INVALID_NOT_UNDECIDED")
	}
	if it.Decision == ledger.DecisionResource && !slices.ContainsFunc(it.EvidenceRefs, func(r string) bool { return strings.HasPrefix(r, "op:") }) {
		return undecided("RESOURCE_WITHOUT_OPERATION")
	}
	return TriageVerdict{
		Decision:     it.Decision,
		Reason:       ledger.CleanTriageText(*it.Reason, ledger.MaxTriageReason),
		EvidenceRefs: sortedSet(it.EvidenceRefs),
		Invalid:      sortedSet(it.Invalid),
	}
}

func strictDecode(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("data after the object")
	}
	return nil
}

// sortedSet is s sorted and de-duplicated, or nil when empty.
func sortedSet(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	out := slices.Clone(s)
	slices.Sort(out)
	return slices.Compact(out)
}

// classRank orders change classes from loosest to strictest (design 5.1: "a
// stricter class or level"): config-only, then hand-written-code, then
// new-resource, whose draft plan gate is a stricter level.
var classRank = map[string]int{"config-only": 1, "hand-written-code": 2, "new-resource": 3}

// stricter applies the stricter-only rule: the class a triage decision gives
// an item whose detector class is finder. A looser proposal keeps finder
// (clamped). A class outside the ranking on either side keeps finder too: it
// is never replaced by a guess.
func stricter(finder, proposed string) (string, bool) {
	f, okF := classRank[finder]
	p, okP := classRank[proposed]
	if !okF || !okP || p < f {
		return finder, finder != proposed
	}
	return proposed, false
}
