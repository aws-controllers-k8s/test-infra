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
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

const fixtureKey = "sha256:0123456789abcdef"

func fixtureVerdict(t *testing.T) json.RawMessage {
	t.Helper()
	data, err := os.ReadFile("testdata/agent-result-triage-v1.json")
	require.NoError(t, err)
	r, err := ParseResult(data, fixtureKey)
	require.NoError(t, err)
	return r.Triage
}

func TestParseResultKeepsTheTriageVerdict(t *testing.T) {
	data, err := os.ReadFile("testdata/agent-result-triage-v1.json")
	require.NoError(t, err)
	r, err := ParseResult(data, fixtureKey)
	require.NoError(t, err)
	assert.Equal(t, "completed_no_change", r.Outcome)
	assert.Equal(t, "TRIAGE_VERDICT", r.Reason)
	assert.Equal(t, int64(5380), r.Tokens)
	// Plan 7's launcher checks both against the job it read the file from.
	assert.Equal(t, "triage", r.Workflow)
	assert.Equal(t, "autopilot-0123456789abcdef-g1-t1", r.ProwJobID)
	assert.Contains(t, string(r.Triage), `"decision": "field-hook"`)

	plain, err := os.ReadFile("testdata/agent-result-v1.json")
	require.NoError(t, err)
	r, err = ParseResult(plain, "")
	require.NoError(t, err)
	assert.Nil(t, r.Triage, "other workflows carry no verdict")
}

// TestTheLargestTriageVerdictFitsTheReadCap: Plan 7's launcher reads at most
// MaxResultBytes (64 KiB) of agent-result.json. The largest verdict the
// bounds allow (600-byte reason, MaxEvidenceRefs refs and invalid codes of
// the longest form, every character escaped by the JSON writer) must fit
// with room to spare, or a valid run would read as RESULT_TOO_LARGE.
func TestTheLargestTriageVerdictFitsTheReadCap(t *testing.T) {
	data, err := os.ReadFile("testdata/agent-result-triage-v1.json")
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	var refs, invalid []any
	for i := 0; i < ledger.MaxEvidenceRefs; i++ {
		refs = append(refs, fmt.Sprintf("crd:spec.%s%03d", strings.Repeat("a", 197), i))
		invalid = append(invalid, fmt.Sprintf("%s %s%03d", strings.Repeat("A", 64), strings.Repeat("a", 207), i))
	}
	doc["triage"] = map[string]any{"schema": TriageSchema, "mode": "api", "items": []any{map[string]any{
		"item_key": fixtureKey, "decision": "undecided", "reason": strings.Repeat("<", ledger.MaxTriageReason),
		"evidence_refs": refs, "invalid": invalid,
	}}}
	big, err := json.MarshalIndent(doc, "", "  ") // encoding/json writes "<" as \u003c: 6 bytes each
	require.NoError(t, err)
	assert.Less(t, len(big), MaxResultBytes/4, "the largest verdict uses under a quarter of the read cap")
	_, err = ParseResult(big, fixtureKey)
	require.NoError(t, err)
}

func TestParseTriageFixture(t *testing.T) {
	assert.Equal(t, TriageVerdict{
		Decision: ledger.DecisionFieldHook,
		Reason:   "DefaultEventHold rides on PutObjectLockConfiguration, which the Bucket's custom update hook sends; the hook must copy the new member.",
		EvidenceRefs: []string{
			"crd:spec.objectLockConfiguration.rule.defaultRetention",
			"field:ObjectLockConfiguration.Rule.DefaultRetention.DefaultEventHold",
			"op:PutObjectLockConfiguration",
		},
	}, ParseTriage(fixtureVerdict(t), fixtureKey))
}

// verdictDoc is a valid mode-api verdict for key, as a JSON tree tests edit.
func verdictDoc(key string) map[string]any {
	return map[string]any{"schema": TriageSchema, "mode": "api", "items": []any{map[string]any{
		"item_key": key, "decision": "field-config", "class": "config-only", "reason": "is_immutable is enough",
		"evidence_refs": []any{"op:CreateBucket"}, "invalid": []any{},
	}}}
}

func rawOf(t *testing.T, doc map[string]any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(doc)
	require.NoError(t, err)
	return b
}

func itemOf(d map[string]any) map[string]any { return d["items"].([]any)[0].(map[string]any) }

func TestParseTriageFailsClosed(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(d map[string]any)
		code string
	}{
		"unknown key":               {func(d map[string]any) { d["extra"] = 1 }, "TRIAGE_MALFORMED"},
		"schema":                    {func(d map[string]any) { d["schema"] = "ack.autopilot.triage/v2" }, "TRIAGE_SCHEMA"},
		"mode":                      {func(d map[string]any) { d["mode"] = "stat" }, "TRIAGE_MODE"},
		"two items":                 {func(d map[string]any) { d["items"] = append(d["items"].([]any), itemOf(d)) }, "TRIAGE_ITEM_COUNT"},
		"unknown item key":          {func(d map[string]any) { itemOf(d)["confidence"] = 0.9 }, "TRIAGE_MALFORMED"},
		"another item":              {func(d map[string]any) { itemOf(d)["item_key"] = "sha256:ffffffffffffffff" }, "TRIAGE_ITEM_KEY"},
		"decision":                  {func(d map[string]any) { itemOf(d)["decision"] = "merge" }, "UNKNOWN_DECISION"},
		"class of another decision": {func(d map[string]any) { itemOf(d)["class"] = "hand-written-code" }, "CLASS_MISMATCH"},
		"class on undecided":        {func(d map[string]any) { itemOf(d)["decision"] = "undecided" }, "CLASS_MISMATCH"},
		"missing class":             {func(d map[string]any) { delete(itemOf(d), "class") }, "CLASS_MISMATCH"},
		"missing reason":            {func(d map[string]any) { delete(itemOf(d), "reason") }, "TRIAGE_MALFORMED"},
		"missing refs":              {func(d map[string]any) { delete(itemOf(d), "evidence_refs") }, "TRIAGE_MALFORMED"},
		"malformed ref":             {func(d map[string]any) { itemOf(d)["evidence_refs"] = []any{"op:Put X"} }, "MALFORMED_REF"},
		"crd outside spec":          {func(d map[string]any) { itemOf(d)["evidence_refs"] = []any{"crd:metadata.name"} }, "MALFORMED_REF"},
		"too many refs": {func(d map[string]any) {
			var refs []any
			for i := 0; i < 17; i++ {
				refs = append(refs, fmt.Sprintf("op:Op%d", i))
			}
			itemOf(d)["evidence_refs"] = refs
		}, "TRIAGE_TOO_LONG"},
		"invalid on decided": {func(d map[string]any) { itemOf(d)["invalid"] = []any{"UNKNOWN_REF op:X"} }, "INVALID_NOT_UNDECIDED"},
		"malformed invalid": {func(d map[string]any) {
			it := itemOf(d)
			it["decision"] = "undecided"
			delete(it, "class")
			it["invalid"] = []any{"not a code"}
		}, "MALFORMED_INVALID"},
		"resource without op": {func(d map[string]any) {
			it := itemOf(d)
			it["decision"], it["class"], it["evidence_refs"] = "resource", "new-resource", []any{"field:Mode"}
		}, "RESOURCE_WITHOUT_OPERATION"},
	} {
		t.Run(name, func(t *testing.T) {
			d := verdictDoc(fixtureKey)
			tc.edit(d)
			assert.Equal(t, TriageVerdict{Decision: ledger.DecisionUndecided, Invalid: []string{tc.code}}, ParseTriage(rawOf(t, d), fixtureKey))
		})
	}
	assert.Equal(t, []string{"TRIAGE_MISSING"}, ParseTriage(nil, fixtureKey).Invalid)
	assert.Equal(t, []string{"TRIAGE_MALFORMED"}, ParseTriage(json.RawMessage(`{`), fixtureKey).Invalid)
	assert.Equal(t, []string{"TRIAGE_MALFORMED"}, ParseTriage(json.RawMessage(`{"schema":"x"} {}`), fixtureKey).Invalid)
}

func TestParseTriageKeepsTheJobsUndecided(t *testing.T) {
	d := verdictDoc(fixtureKey)
	it := itemOf(d)
	it["decision"] = "undecided"
	delete(it, "class")
	it["invalid"] = []any{"UNKNOWN_REF op:DeleteEverything"}
	it["reason"] = "two\nlines"
	it["evidence_refs"] = []any{"op:CreateBucket", "op:CreateBucket"}
	assert.Equal(t, TriageVerdict{
		Decision: ledger.DecisionUndecided, Reason: "two lines",
		EvidenceRefs: []string{"op:CreateBucket"}, Invalid: []string{"UNKNOWN_REF op:DeleteEverything"},
	}, ParseTriage(rawOf(t, d), fixtureKey))
}

func TestStricterOnly(t *testing.T) {
	for _, tc := range []struct {
		finder, proposed, want string
		clamped                bool
	}{
		{"config-only", "config-only", "config-only", false},
		{"config-only", "hand-written-code", "hand-written-code", false},
		{"config-only", "new-resource", "new-resource", false},
		{"hand-written-code", "config-only", "hand-written-code", true},
		{"hand-written-code", "new-resource", "new-resource", false},
		{"new-resource", "hand-written-code", "new-resource", true},
		{"SDK-bump", "config-only", "SDK-bump", true},
		{"hand-written-code", "bogus", "hand-written-code", true},
	} {
		got, clamped := stricter(tc.finder, tc.proposed)
		assert.Equal(t, tc.want, got, "%s <- %s", tc.finder, tc.proposed)
		assert.Equal(t, tc.clamped, clamped, "%s <- %s", tc.finder, tc.proposed)
	}
}
