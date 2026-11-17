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

package ledger

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const triageKey = "api-change|s3|Bucket|spec:Mode|needs-config"

// triagedItem is a needs-config item TRIAGED with a pending field-hook
// proposal, as the Dispatcher leaves it.
func triagedItem(key string) *WorkItem {
	w := sampleItem(key)
	w.DetectorWork, w.ChangeClass = "custom-update", "hand-written-code"
	id := TriageLaunchID(w, 1)
	w.Links.ProwJobs = []string{id}
	w.Triage = &Triage{
		Mode: TriageModeAPI, Input: TriageInput(w), Runs: 1, Launches: 1,
		Proposal: &TriageProposal{
			LaunchID: id, Decision: DecisionFieldHook, Class: "hand-written-code",
			Reason: "needs the update hook", EvidenceRefs: []string{"op:PutBucketMode"}, TS: "2026-11-12T09:30:00Z",
		},
		Log: []TriageEvent{
			{TS: "2026-11-12T09:00:00Z", Event: TriageStarted, LaunchID: id},
			{TS: "2026-11-12T09:30:00Z", Event: TriageProposed, LaunchID: id, Decision: DecisionFieldHook},
		},
	}
	w.State = Triaged
	w.History = append(w.History,
		Transition{TS: "2026-11-12T09:00:00Z", To: Detected, By: "dispatcher", Reason: "TRIAGE_STARTED " + id},
		Transition{TS: "2026-11-12T09:30:00Z", To: Triaged, By: "dispatcher", Reason: "TRIAGE_PROPOSED field-hook"})
	return w
}

func TestTriageRoundTripAndLintClean(t *testing.T) {
	root := t.TempDir()
	w := triagedItem(triageKey)
	require.NoError(t, Save(root, map[string]*WorkItem{w.Key: w}))
	got, err := Load(root)
	require.NoError(t, err)
	assert.Equal(t, w, got[w.Key])
	assert.Empty(t, Lint(root))
	assert.True(t, w.Triage.Pending())
	assert.Equal(t, "", w.Triage.Decided())

	var none *Triage
	assert.False(t, none.Pending())
	assert.Equal(t, "", none.Decided())
}

func TestTriageLaunchIDAndInput(t *testing.T) {
	w := sampleItem(triageKey)
	assert.Equal(t, "autopilot-"+strings.TrimPrefix(KeyHash(w.Key), "sha256:")+"-g1-t2", TriageLaunchID(w, 2))
	in := TriageInput(w)
	assert.Regexp(t, `^sha256:[0-9a-f]{16}$`, in)
	w.Evidence.SDKTo = "service/s3/v9.9.9"
	assert.Equal(t, in, TriageInput(w), "a newer SDK target does not re-triage")
	w.DetectorWork = "custom-update"
	assert.NotEqual(t, in, TriageInput(w), "a detector reclassification does")
}

func TestCleanTriageText(t *testing.T) {
	assert.Equal(t, "a b c", CleanTriageText(" a\n\tb\x07  c ", 100))
	assert.Equal(t, "ok", CleanTriageText("ok\xff", 100), "invalid UTF-8 is dropped")
	assert.Equal(t, "ab", CleanTriageText("a�b", 100))
	got := CleanTriageText(strings.Repeat("é", 400), 600)
	assert.Equal(t, 600, len(got))
	assert.True(t, utf8.ValidString(got))
	assert.True(t, ValidEvidenceRef("crd:status.location"))
	assert.False(t, ValidEvidenceRef("crd:metadata.name"))
	assert.True(t, ValidInvalidCode("UNKNOWN_REF op:DeleteEverything"))
	assert.False(t, ValidInvalidCode("unknown ref"))
}

func TestTriageLogIsBounded(t *testing.T) {
	tr := &Triage{}
	for i := 0; i < MaxTriageLog+5; i++ {
		tr.AppendLog(TriageEvent{TS: "2026-11-12T09:00:00Z", Event: TriageStarted, LaunchID: fmt.Sprintf("x%d", i)})
	}
	assert.Len(t, tr.Log, MaxTriageLog)
	assert.Equal(t, "x5", tr.Log[0].LaunchID, "the oldest events go first")
}

func TestLintTriageRules(t *testing.T) {
	review := func(w *WorkItem, outcome, decision, by string) *TriageReview {
		return &TriageReview{LaunchID: w.Triage.Proposal.LaunchID, Outcome: outcome, Decision: decision, By: by, CommentID: 1, TS: "2026-11-12T10:00:00Z"}
	}
	for name, tc := range map[string]struct {
		edit func(w *WorkItem)
		want string
	}{
		"not needs-config": {func(w *WorkItem) { w.GapClass = "regen" }, "only needs-config items"},
		"mode":             {func(w *WorkItem) { w.Triage.Mode = "stat" }, `mode "stat"`},
		"input":            {func(w *WorkItem) { w.Triage.Input = "x" }, `input "x"`},
		"runs":             {func(w *WorkItem) { w.Triage.Runs = 2 }, "runs=2 launches=1"},
		"failed":           {func(w *WorkItem) { w.Triage.Failed = "not snake" }, `failed "not snake"`},
		"claim beside proposal": {func(w *WorkItem) {
			w.Triage.Claim = &Claim{LaunchID: w.Triage.Proposal.LaunchID, Workflow: "triage", TS: "2026-11-12T09:00:00Z", Deadline: "2026-11-12T09:30:00Z"}
		}, "beside a proposal"},
		"foreign launch id":  {func(w *WorkItem) { w.Triage.Proposal.LaunchID = "autopilot-ffffffffffffffff-g1-t1" }, "not a triage run of this item"},
		"decision":           {func(w *WorkItem) { w.Triage.Proposal.Decision = "merge" }, "not a mode-api decision"},
		"class mismatch":     {func(w *WorkItem) { w.Triage.Proposal.Class = "config-only" }, "do not fit"},
		"class on undecided": {func(w *WorkItem) { w.Triage.Proposal.Decision = DecisionUndecided }, "gives no class"},
		"invalid on decided": {func(w *WorkItem) { w.Triage.Proposal.Invalid = []string{"UNKNOWN_REF op:X"} }, "must be undecided"},
		"reason":             {func(w *WorkItem) { w.Triage.Proposal.Reason = "two\nlines" }, "reason is not clean"},
		"ref":                {func(w *WorkItem) { w.Triage.Proposal.EvidenceRefs = []string{"url:x"} }, `evidence ref "url:x"`},
		"pending not triaged": {func(w *WorkItem) {
			w.State = Queued
			w.History[len(w.History)-1].To = Queued
		}, "pending proposal needs the item TRIAGED"},
		"review for another proposal": {func(w *WorkItem) {
			w.Triage.Review = review(w, ReviewAccepted, DecisionFieldHook, "m")
			w.Triage.Review.LaunchID = "autopilot-0000000000000000-g1-t9"
		}, "review is for"},
		"accepted another decision": {func(w *WorkItem) { w.Triage.Review = review(w, ReviewAccepted, DecisionFieldConfig, "m") }, "applies the proposal's decision"},
		"review login":              {func(w *WorkItem) { w.Triage.Review = review(w, ReviewRejected, "", "") }, "need a login"},
		"log event":                 {func(w *WorkItem) { w.Triage.Log[0].Event = "exploded" }, `log event "exploded"`},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			w := triagedItem(triageKey)
			tc.edit(w)
			dir, _ := w.State.Dir()
			b, err := Marshal(w)
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(filepath.Join(root, "workitems", dir), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(root, "workitems", dir, w.FileName()), b, 0o644))
			errs := Lint(root)
			require.NotEmpty(t, errs)
			var all []string
			for _, e := range errs {
				all = append(all, e.Error())
			}
			assert.Contains(t, strings.Join(all, "\n"), tc.want)
		})
	}
}

func TestLintTriageAcceptsReviewedAndFinishedItems(t *testing.T) {
	root := t.TempDir()
	accepted := triagedItem(triageKey)
	accepted.Triage.Review = &TriageReview{LaunchID: accepted.Triage.Proposal.LaunchID, Outcome: ReviewAccepted,
		Decision: DecisionFieldHook, By: "maintainer", CommentID: 42, TS: "2026-11-12T10:00:00Z"}
	accepted.State = Blocked
	accepted.History = append(accepted.History, Transition{TS: "2026-11-12T10:00:00Z", To: Blocked, By: "dispatcher"})
	// The issue finder closed this one while its proposal was pending.
	closed := triagedItem("api-change|s3|Bucket|spec:Zeta|needs-config")
	closed.State = WontFix
	closed.History = append(closed.History, Transition{TS: "2026-11-12T10:00:00Z", To: WontFix, By: "issue-finder"})
	require.NoError(t, Save(root, map[string]*WorkItem{accepted.Key: accepted, closed.Key: closed}))
	assert.Empty(t, Lint(root))
}
