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
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

// reconcileTriageWith reconciles w, whose triage run reports st (nil: no
// such status), at at, and checks the ledger lints. A second reconcile must
// change nothing.
func reconcileTriageWith(t *testing.T, w *ledger.WorkItem, st *Status, at string) []string {
	t.Helper()
	f := &fakeLauncher{statuses: map[string]Status{}}
	if st != nil && w.Triage != nil && w.Triage.Claim != nil {
		f.statuses[w.Triage.Claim.LaunchID] = *st
	}
	items := ledgerOf(w)
	problems := reconcileTriage(context.Background(), items, f, withTriage(t), mustTime(at))
	assertLints(t, items)
	before := marshalAll(t, items)
	reconcileTriage(context.Background(), items, f, withTriage(t), mustTime(at))
	assert.Equal(t, before, marshalAll(t, items), "a second reconcile changes nothing")
	return problems
}

func TestReconcileTriageProposal(t *testing.T) {
	w := triaging(needsConfig(fieldKey, "config-only", "create-only"))
	id := w.Triage.Claim.LaunchID
	st := triageDone(verdictJSON(w.KeyHash, "field-hook", "hand-written-code", "op:PutBucketMode"))
	assert.Empty(t, reconcileTriageWith(t, w, &st, "2026-11-12T09:00:00Z"))

	assert.Equal(t, ledger.Triaged, w.State)
	assert.Equal(t, "TRIAGE_PROPOSED field-hook", last(w).Reason)
	assert.Nil(t, w.Triage.Claim)
	assert.Equal(t, &ledger.TriageProposal{LaunchID: id, Decision: "field-hook", Class: "hand-written-code",
		Reason: "because", EvidenceRefs: []string{"op:PutBucketMode"}, TS: "2026-11-12T09:00:00Z"}, w.Triage.Proposal)
	assert.Equal(t, "config-only", w.ChangeClass, "nothing applies until a maintainer replies")
	assert.Equal(t, int64(5000), w.Usage.Tokens)
	assert.True(t, w.Triage.Pending())
	assert.True(t, w.FinderOwned())
}

func TestReconcileTriageClampsALooserDecision(t *testing.T) {
	w := triaging(needsConfig(fieldKey, "hand-written-code", "custom-update"))
	st := triageDone(verdictJSON(w.KeyHash, "field-config", "config-only"))
	reconcileTriageWith(t, w, &st, "2026-11-12T09:00:00Z")
	assert.Equal(t, "field-config", w.Triage.Proposal.Decision)
	assert.Equal(t, "hand-written-code", w.Triage.Proposal.Class, "the detector's stricter class is kept")
	assert.True(t, w.Triage.Proposal.Clamped)
}

func TestReconcileTriageInvalidVerdictIsUndecided(t *testing.T) {
	w := triaging(needsConfig(fieldKey, "config-only", "create-only"))
	st := triageDone([]byte(`{"schema":"ack.autopilot.triage/v9"}`))
	reconcileTriageWith(t, w, &st, "2026-11-12T09:00:00Z")
	assert.Equal(t, ledger.Triaged, w.State)
	assert.Equal(t, "undecided", w.Triage.Proposal.Decision)
	assert.Equal(t, []string{"TRIAGE_SCHEMA"}, w.Triage.Proposal.Invalid)
	assert.True(t, w.Triage.Log[len(w.Triage.Log)-1].Invalid)
}

func TestReconcileTriageServerDefault(t *testing.T) {
	w := triaging(needsConfig(fieldKey, "config-only", serverDefaultWork))
	st := triageDone(verdictJSON(w.KeyHash, "not-worth-modelling", ""))
	reconcileTriageWith(t, w, &st, "2026-11-12T09:00:00Z")
	assert.Equal(t, "undecided", w.Triage.Proposal.Decision)
	assert.Equal(t, []string{"DECISION_NOT_ALLOWED_FOR_SERVER_DEFAULT"}, w.Triage.Proposal.Invalid)
}

func TestReconcileTriageRetriesThenFails(t *testing.T) {
	w := triaging(needsConfig(fieldKey, "config-only", "create-only"))
	failed := Status{Phase: PhaseFinished, Result: &RunResult{Outcome: OutcomeFailed, Reason: "MODEL_CALL_FAILED"}}
	reconcileTriageWith(t, w, &failed, "2026-11-12T09:00:00Z")
	assert.Equal(t, ledger.Detected, w.State)
	assert.Nil(t, w.Triage.Claim)
	assert.Empty(t, w.Triage.Failed, "one run of two used: it may run again")
	assert.Equal(t, "TRIAGE_FAILED MODEL_CALL_FAILED", last(w).Reason)
	assert.True(t, triageCandidate(w))

	triaging(w)
	reconcileTriageWith(t, w, &failed, "2026-11-12T09:00:00Z")
	assert.Equal(t, "MODEL_CALL_FAILED", w.Triage.Failed, "runs exhausted")
	assert.False(t, triageCandidate(w))
}

func TestReconcileTriageNeedsHumanStops(t *testing.T) {
	w := triaging(needsConfig(fieldKey, "config-only", "create-only"))
	st := Status{Phase: PhaseFinished, Result: &RunResult{Outcome: OutcomeNeedsHuman, Reason: "SDK_MODEL_UNREADABLE"}}
	reconcileTriageWith(t, w, &st, "2026-11-12T09:00:00Z")
	assert.Equal(t, "SDK_MODEL_UNREADABLE", w.Triage.Failed)

	odd := triaging(needsConfig(fieldKey, "config-only", "create-only"))
	st = Status{Phase: PhaseFinished, Result: &RunResult{Outcome: OutcomePROpened, PR: "https://github.com/aws-controllers-k8s/s3-controller/pull/1"}}
	reconcileTriageWith(t, odd, &st, "2026-11-12T09:00:00Z")
	assert.Equal(t, "UNKNOWN_VERDICT", odd.Triage.Failed, "a triage run never opens a PR")
}

func TestReconcileTriageNoVerdictAndOverdue(t *testing.T) {
	w := triaging(needsConfig(fieldKey, "config-only", "create-only"))
	st := Status{Phase: PhaseFinished}
	reconcileTriageWith(t, w, &st, "2026-11-12T09:00:00Z")
	assert.Equal(t, "TRIAGE_FAILED INFRA_NO_VERDICT", last(w).Reason)

	late := triaging(needsConfig(fieldKey, "config-only", "create-only"))
	assert.Empty(t, reconcileTriageWith(t, late, nil, "2026-11-12T09:40:00Z"), "deadline 09:20 plus 30m grace not passed")
	assert.NotNil(t, late.Triage.Claim)
	reconcileTriageWith(t, late, nil, "2026-11-12T10:00:00Z")
	assert.Nil(t, late.Triage.Claim)
	assert.Equal(t, "TRIAGE_FAILED TRIAGE_OVERDUE", last(late).Reason)
}

// Plan 7's cross-check (dispatcher.consistent and the job states), applied to
// triage runs: the verdict counts only when the job's exit agrees with it.
func TestReconcileTriageCrossChecksTheJobState(t *testing.T) {
	for name, tc := range map[string]struct {
		job     JobState
		result  *RunResult
		problem string
		reason  string // the item's last history reason
		failed  string // Triage.Failed afterwards ("" = may run again)
	}{
		"succeeded, verdict":        {JobSucceeded, &RunResult{Outcome: OutcomeNoChange, Reason: "TRIAGE_VERDICT"}, "", "TRIAGE_PROPOSED field-hook", ""},
		"failed job says no change": {JobFailed, &RunResult{Outcome: OutcomeNoChange, Reason: "TRIAGE_VERDICT"}, "", "TRIAGE_FAILED RESULT_STATE_MISMATCH", ""},
		"succeeded job says failed": {JobSucceeded, &RunResult{Outcome: OutcomeFailed, Reason: "MODEL_CALL_FAILED"}, "", "TRIAGE_FAILED RESULT_STATE_MISMATCH", ""},
		"failed, needs_human":       {JobFailed, &RunResult{Outcome: OutcomeNeedsHuman, Reason: "SDK_MODEL_UNREADABLE"}, "", "TRIAGE_FAILED SDK_MODEL_UNREADABLE", "SDK_MODEL_UNREADABLE"},
		"aborted by someone":        {JobAborted, nil, "", "TRIAGE_FAILED RUN_ABORTED", "RUN_ABORTED"},
		"errored":                   {JobErrored, nil, "", "TRIAGE_FAILED INFRA_JOB_ERROR", ""},
		"no file":                   {JobFailed, nil, "RESULT_MISSING", "TRIAGE_FAILED RESULT_MISSING", ""},
		"another job's file":        {JobSucceeded, nil, "RESULT_FOR_OTHER_JOB", "TRIAGE_FAILED RESULT_FOR_OTHER_JOB", ""},
	} {
		t.Run(name, func(t *testing.T) {
			w := triaging(needsConfig(fieldKey, "config-only", "create-only"))
			if tc.result != nil && tc.result.Outcome == OutcomeNoChange {
				tc.result.Triage = verdictJSON(w.KeyHash, "field-hook", "hand-written-code", "op:PutBucketMode")
				tc.result.Tokens = 5000
			}
			st := Status{Phase: PhaseFinished, Job: tc.job, Result: tc.result, Problem: tc.problem}
			reconcileTriageWith(t, w, &st, "2026-11-12T09:00:00Z")
			assert.Equal(t, tc.reason, last(w).Reason)
			assert.Equal(t, tc.failed, w.Triage.Failed)
			assert.Nil(t, w.Triage.Claim)
			if strings.HasSuffix(tc.reason, "RESULT_STATE_MISMATCH") {
				assert.Nil(t, w.Triage.Proposal, "a mismatched file never reaches the ledger")
				assert.Nil(t, w.Usage)
			}
		})
	}
}

// Like Plan 7's reaper: a live overdue run is stopped before its claim goes,
// so it never lingers as an orphan that holds a triage slot.
func TestReconcileTriageAbortsALiveOverdueRun(t *testing.T) {
	w := triaging(needsConfig(fieldKey, "config-only", "create-only"))
	id := w.Triage.Claim.LaunchID
	f := &fakeLauncher{statuses: map[string]Status{id: {Phase: PhaseRunning}}}
	problems := reconcileTriage(context.Background(), ledgerOf(w), f, withTriage(t), mustTime("2026-11-12T10:00:00Z"))
	assert.Empty(t, problems)
	assert.Equal(t, []string{id}, f.aborted)
	assert.Equal(t, "TRIAGE_FAILED TRIAGE_OVERDUE", last(w).Reason)

	stuck := triaging(needsConfig(fieldKey, "config-only", "create-only"))
	f = &fakeLauncher{statuses: map[string]Status{stuck.Triage.Claim.LaunchID: {Phase: PhaseRunning}}, abortErr: errors.New("forbidden")}
	problems = reconcileTriage(context.Background(), ledgerOf(stuck), f, withTriage(t), mustTime("2026-11-12T10:00:00Z"))
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0], "forbidden")
	assert.NotNil(t, stuck.Triage.Claim, "retried next tick")

	shadow := triaging(needsConfig(fieldKey, "config-only", "create-only"))
	s := &ShadowLauncher{}
	reconcileTriage(context.Background(), ledgerOf(shadow), s, withTriage(t), mustTime("2026-11-12T10:00:00Z"))
	assert.Empty(t, s.Aborts, "a shadow launcher reports unknown and stops nothing")
	assert.Nil(t, shadow.Triage.Claim)
}

func TestReconcileTriageStopsTheRunOfAClosedItem(t *testing.T) {
	w := triaging(needsConfig(fieldKey, "config-only", "create-only"))
	id := w.Triage.Claim.LaunchID
	w.State = ledger.WontFix
	w.History = append(w.History, ledger.Transition{TS: "2026-11-12T08:55:00Z", To: ledger.WontFix, By: "issue-finder"})
	f := &fakeLauncher{statuses: map[string]Status{id: {Phase: PhaseRunning}}}
	assert.Empty(t, reconcileTriage(context.Background(), ledgerOf(w), f, withTriage(t), now))
	assert.Equal(t, []string{id}, f.aborted)
	assert.Nil(t, w.Triage.Claim)
}

func TestReconcileTriageStatusError(t *testing.T) {
	w := triaging(needsConfig(fieldKey, "config-only", "create-only"))
	f := &fakeLauncher{statusErr: map[string]error{w.Triage.Claim.LaunchID: errors.New("boom")}}
	problems := reconcileTriage(context.Background(), ledgerOf(w), f, withTriage(t), now)
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0], "boom")
	assert.NotNil(t, w.Triage.Claim)
}

func TestReconcileTriageDiscardsWhenTheFinderClosedTheItem(t *testing.T) {
	w := triaging(needsConfig(fieldKey, "config-only", "create-only"))
	w.State = ledger.WontFix
	w.Verdict = &ledger.Verdict{Outcome: "wont_fix:product_decision", Reason: "MAINTAINER_WONTFIX by m"}
	w.History = append(w.History, ledger.Transition{TS: "2026-11-12T08:55:00Z", To: ledger.WontFix, By: "issue-finder"})
	st := triageDone(verdictJSON(w.KeyHash, "field-hook", "hand-written-code"))
	reconcileTriageWith(t, w, &st, "2026-11-12T09:00:00Z")
	assert.Equal(t, ledger.WontFix, w.State)
	assert.Nil(t, w.Triage.Claim)
	assert.Nil(t, w.Triage.Proposal)
	assert.Equal(t, ledger.TriageDiscarded, w.Triage.Log[len(w.Triage.Log)-1].Event)
}

func TestReconcileTriageResetsAStaleRecord(t *testing.T) {
	w := triaging(needsConfig(fieldKey, "config-only", "create-only"))
	st := triageDone(verdictJSON(w.KeyHash, "field-config", "config-only"))
	reconcileTriageWith(t, w, &st, "2026-11-12T09:00:00Z")
	require.True(t, w.Triage.Pending())

	// Ingest: the detector's work changed, so the item is DETECTED again
	// with a fresh classification.
	w.DetectorWork, w.ChangeClass = "custom-update", "hand-written-code"
	w.State = ledger.Detected
	w.History = append(w.History, ledger.Transition{TS: "2026-11-12T09:30:00Z", To: ledger.Detected, By: "issue-finder", Reason: "DETECTOR_RECLASSIFIED"})
	reconcileTriageWith(t, w, nil, "2026-11-12T10:00:00Z")
	assert.Nil(t, w.Triage.Proposal)
	assert.Equal(t, 0, w.Triage.Runs)
	assert.Equal(t, 1, w.Triage.Launches, "launch ids keep counting")
	assert.Equal(t, ledger.TriageInput(w), w.Triage.Input)
	assert.Equal(t, ledger.TriageStale, w.Triage.Log[len(w.Triage.Log)-1].Event)
	assert.True(t, triageCandidate(w))
}

// reconcileTriage could not abort the run of an item the finder closed, so the
// claim stays; abort_inflight's abort then succeeds. The item stays closed:
// the run is discarded, not failed back to DETECTED.
func TestAbortInflightTriageLeavesAClosedItemClosed(t *testing.T) {
	ctx := context.Background()
	w := triaging(needsConfig(fieldKey, "config-only", "create-only"))
	id := w.Triage.Claim.LaunchID
	w.State = ledger.WontFix
	w.Verdict = &ledger.Verdict{Outcome: "wont_fix:product_decision", Reason: "MAINTAINER_WONTFIX by m"}
	w.History = append(w.History, ledger.Transition{TS: "2026-11-12T08:55:00Z", To: ledger.WontFix, By: "issue-finder"})
	items := ledgerOf(w)
	p := withTriage(t)
	f := &fakeLauncher{abortErr: errors.New("transient")}
	require.Len(t, reconcileTriage(ctx, items, f, p, now), 1)
	require.NotNil(t, w.Triage.Claim)

	f.abortErr = nil
	assert.Empty(t, abortInflightTriage(ctx, items, f, p, now.UTC().Format(time.RFC3339)))
	assert.Equal(t, []string{id}, f.aborted)
	assert.Equal(t, ledger.WontFix, w.State)
	assert.Equal(t, ledger.WontFix, last(w).To)
	assert.Nil(t, w.Triage.Claim)
	assert.Empty(t, w.Triage.Failed)
	ev := w.Triage.Log[len(w.Triage.Log)-1]
	assert.Equal(t, ledger.TriageDiscarded, ev.Event)
	assert.Equal(t, id, ev.LaunchID)
	assert.Equal(t, "ITEM_WONT_FIX", ev.Reason)
	assertLints(t, items)
}
