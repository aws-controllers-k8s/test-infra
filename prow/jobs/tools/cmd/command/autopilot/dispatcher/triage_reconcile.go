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
	"fmt"
	"time"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/policy"
)

// reconcileTriage applies every finished triage run, reaps runs past their
// deadline plus grace, discards runs whose item the issue finder closed, and
// resets records the detector made stale. A verdict becomes a pending
// proposal (DETECTED -> TRIAGED); nothing is applied until a maintainer
// replies (applyTriageCommands). It applies Plan 7's job-state rules (the
// ones reconcile applies to w.Claim runs): a result only counts when it is
// consistent with how the job ended. A run that is no longer wanted is
// aborted first, so it does not linger as an orphan in the launcher's
// inventory. It returns one problem per run whose status could not be read or
// that could not be aborted; those items are left as they are.
func reconcileTriage(ctx context.Context, items map[string]*ledger.WorkItem, l Launcher, p policy.Policy, now time.Time) []string {
	ts := now.UTC().Format(time.RFC3339)
	maxRuns := triageMaxRuns(p)
	var problems []string
	for _, k := range sortedKeys(items) {
		w := items[k]
		t := w.Triage
		if t == nil {
			continue
		}
		stale := w.State == ledger.Detected && t.Input != ledger.TriageInput(w)
		if t.Claim != nil && (stale || w.State != ledger.Detected) {
			if err := stopTriage(ctx, l, t.Claim.LaunchID); err != nil {
				problems = append(problems, fmt.Sprintf("abort %s for %s: %v", t.Claim.LaunchID, w.Key, err))
				continue
			}
		}
		if stale {
			restale(w, ts)
			continue
		}
		if t.Claim == nil {
			continue
		}
		if w.State != ledger.Detected {
			t.AppendLog(ledger.TriageEvent{TS: ts, Event: ledger.TriageDiscarded, LaunchID: t.Claim.LaunchID, Reason: "ITEM_" + string(w.State)})
			t.Claim = nil
			continue
		}
		st, err := l.Status(ctx, t.Claim.LaunchID)
		if err != nil {
			problems = append(problems, fmt.Sprintf("status of %s for %s: %v", t.Claim.LaunchID, w.Key, err))
			continue
		}
		switch {
		case st.Phase == PhaseFinished && st.Job == JobAborted:
			// Someone stopped the job (Deck, a person): a person decides
			// what happens next, as for Plan 7's RUN_ABORTED.
			triageFailed(w, "RUN_ABORTED", false, maxRuns, ts)
		case st.Phase == PhaseFinished && st.Job == JobErrored:
			triageFailed(w, "INFRA_JOB_ERROR", true, maxRuns, ts)
		case st.Phase == PhaseFinished && st.Result != nil && !consistent(st.Job, st.Result.Outcome):
			// The file and the exit code disagree: the run may have been
			// tampered with. Nothing it reports (verdict, tokens) is kept.
			triageFailed(w, "RESULT_STATE_MISMATCH", true, maxRuns, ts)
		case st.Phase == PhaseFinished && st.Result != nil:
			applyTriage(w, *st.Result, maxRuns, ts)
		case st.Phase == PhaseFinished:
			triageFailed(w, cleanReason(st.Problem, "INFRA_NO_VERDICT"), true, maxRuns, ts)
		case overdue(t.Claim, p.Budgets.ReaperGrace(), now):
			if st.Phase == PhasePending || st.Phase == PhaseRunning {
				if err := stopTriage(ctx, l, t.Claim.LaunchID); err != nil {
					problems = append(problems, fmt.Sprintf("abort overdue %s for %s: %v", t.Claim.LaunchID, w.Key, err))
					continue
				}
			}
			triageFailed(w, "TRIAGE_OVERDUE", true, maxRuns, ts)
		}
	}
	return problems
}

// triageMaxRuns is policy/triage.yaml's max_triage_runs_per_item, or 1 when
// the lane is off (runs still in flight are reconciled, not retried).
func triageMaxRuns(p policy.Policy) int {
	if p.Triage != nil {
		return p.Triage.MaxTriageRunsPerItem
	}
	return 1
}

// stopTriage aborts a triage run (Plan 7's Launcher.Abort: idempotent, a
// missing or finished job is left alone). A shadow or dry-run launcher stops
// nothing.
func stopTriage(ctx context.Context, l Launcher, id string) error {
	if l.Shadow() {
		return nil
	}
	return l.Abort(ctx, id)
}

// applyTriage turns one finished run's result into a proposal, or a failure.
func applyTriage(w *ledger.WorkItem, r RunResult, maxRuns int, ts string) {
	t := w.Triage
	addUsage(w, r)
	switch r.Outcome {
	case OutcomeNoChange:
		v := ParseTriage(r.Triage, w.KeyHash)
		if w.DetectorWork == serverDefaultWork && (v.Decision == ledger.DecisionResource || v.Decision == ledger.DecisionNotWorthModelling) {
			v = TriageVerdict{Decision: ledger.DecisionUndecided, Reason: v.Reason, EvidenceRefs: v.EvidenceRefs,
				Invalid: []string{"DECISION_NOT_ALLOWED_FOR_SERVER_DEFAULT"}}
		}
		class, clamped := "", false
		if dc, ok := ledger.DecisionClass[v.Decision]; ok {
			class, clamped = stricter(w.ChangeClass, dc)
		}
		id := t.Claim.LaunchID
		t.Claim = nil
		t.Proposal = &ledger.TriageProposal{LaunchID: id, Decision: v.Decision, Class: class, Clamped: clamped,
			Invalid: v.Invalid, Reason: v.Reason, EvidenceRefs: v.EvidenceRefs, TS: ts}
		t.AppendLog(ledger.TriageEvent{TS: ts, Event: ledger.TriageProposed, LaunchID: id, Decision: v.Decision, Invalid: len(v.Invalid) > 0})
		transition(w, ledger.Triaged, ts, "TRIAGE_PROPOSED "+v.Decision)
	case OutcomeFailed:
		triageFailed(w, cleanReason(r.Reason, "FAILED"), true, maxRuns, ts)
	case OutcomeNeedsHuman:
		triageFailed(w, cleanReason(r.Reason, "NEEDS_HUMAN"), false, maxRuns, ts)
	default:
		triageFailed(w, "UNKNOWN_VERDICT", false, maxRuns, ts)
	}
}

// triageFailed ends the run in flight. The item may run again while retry is
// set and runs are left; otherwise Failed holds it for a person.
func triageFailed(w *ledger.WorkItem, reason string, retry bool, maxRuns int, ts string) {
	t := w.Triage
	t.AppendLog(ledger.TriageEvent{TS: ts, Event: ledger.TriageFailed, LaunchID: t.Claim.LaunchID, Reason: reason})
	t.Claim = nil
	if !retry || t.Runs >= maxRuns {
		t.Failed = reason
	}
	transition(w, ledger.Detected, ts, "TRIAGE_FAILED "+reason)
}

// restale starts the record over for the item's new classification. Launch
// ids keep counting, so a late result of the old run never matches.
func restale(w *ledger.WorkItem, ts string) {
	t := w.Triage
	id := ""
	if t.Claim != nil {
		id = t.Claim.LaunchID
	}
	t.Input, t.Runs, t.Claim, t.Failed, t.Proposal, t.Review = ledger.TriageInput(w), 0, nil, "", nil, nil
	t.AppendLog(ledger.TriageEvent{TS: ts, Event: ledger.TriageStale, LaunchID: id})
}

// abortInflightTriage stops every triage run in flight (Plan 7's policy
// abort_inflight). The item goes back to DETECTED with that run used
// (TRIAGE_FAILED ABORTED_BY_POLICY), so it is triaged again once the switch
// is off and a run is left. A shadow or dry-run launcher stops nothing and
// changes no item; one problem line says how many it would stop.
func abortInflightTriage(ctx context.Context, items map[string]*ledger.WorkItem, l Launcher, p policy.Policy, ts string) []string {
	var active []*ledger.WorkItem
	for _, k := range sortedKeys(items) {
		if w := items[k]; w.Triage != nil && w.Triage.Claim != nil {
			active = append(active, w)
		}
	}
	if len(active) == 0 {
		return nil
	}
	if l.Shadow() {
		for _, w := range active {
			_ = l.Abort(ctx, w.Triage.Claim.LaunchID)
		}
		return []string{fmt.Sprintf("abort_inflight: %d triage run(s) in flight; this launcher aborts nothing", len(active))}
	}
	maxRuns := triageMaxRuns(p)
	var problems []string
	for _, w := range active {
		if err := l.Abort(ctx, w.Triage.Claim.LaunchID); err != nil {
			problems = append(problems, fmt.Sprintf("abort %s for %s: %v", w.Triage.Claim.LaunchID, w.Key, err))
			continue
		}
		triageFailed(w, "ABORTED_BY_POLICY", true, maxRuns, ts)
	}
	return problems
}
