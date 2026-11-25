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
	"strings"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

// Reading what happened from an item's history (Plan 10). The tick's metrics
// and the daily health report both use these, so they always agree.

// RunClass is how a workflow run ended.
type RunClass int

const (
	// RunNone: the entry does not end a reconciled run.
	RunNone RunClass = iota
	// RunCompleted: the run reported pr_opened, pr_updated or
	// completed_no_change and the Dispatcher accepted it (RUNNING -> GATING
	// or DONE). sdk-bump's failed GATE_ENV with a PR also lands here: the run
	// opened its PR and only the in-process gate's environment failed, which
	// the gate retries without charging an attempt.
	RunCompleted
	// RunFailed: failed or needs_human, and every run the Dispatcher ended
	// for it (RUNNING -> QUEUED "RETRY <reason>" or ESCALATED): a job error,
	// an aborted job, a result that contradicts the job state, an unknown
	// outcome, an invalid PR link, and a reaped run (INFRA_NO_VERDICT,
	// RUN_OVERDUE). A reaped run did not do its job, so it counts as failed.
	RunFailed
	// RunWontFix: the run reported wont_fix:<reason>; neither completed nor
	// failed (design 10: the failure rate excludes WONT_FIX).
	RunWontFix
)

// RunEnd classifies h[i] as the end of a workflow run. Only a lane workflow
// (sdk-bump, add-field, add-resource) moves an item to RUNNING: triage runs
// leave the item DETECTED and dod gate jobs hold GATING. So h[i] ends a run
// exactly when h[i-1] moved the item to RUNNING. An abort by policy
// (ABORTED_BY_POLICY) is not a reconciled run.
func RunEnd(h []ledger.Transition, i int) RunClass {
	if i <= 0 || i >= len(h) || h[i-1].To != ledger.Running {
		return RunNone
	}
	e := h[i]
	switch {
	case e.To == ledger.Gating, e.To == ledger.Done:
		return RunCompleted
	case e.To == ledger.WontFix:
		return RunWontFix
	case e.To == ledger.Escalated && e.Reason == "ABORTED_BY_POLICY":
		return RunNone
	case e.To == ledger.Escalated, e.To == ledger.Queued && strings.HasPrefix(e.Reason, "RETRY "):
		return RunFailed
	}
	return RunNone
}

// GateVerdictAt reports whether h[i] records a valid ack-dod verdict for the
// item's PR, and whether it passed. Valid means gate checked it (signature,
// digest, tree, item) and acted on its verdict: GATE_PASSED, a GATE_FAILED
// requeue (or ATTEMPTS_EXHAUSTED straight out of GATING), GATE_FAILED_AFTER_REVIEW
// or GATE_RECLASSIFY_*. Environment failures (RETRY_ENV, GATE_ENV_EXHAUSTED),
// tampered or invalid verdicts (GATE_VERDICT_*, GATE_VERIFY_FAILED) and
// verdicts for an old head (GATE_HEAD_MOVED) never match.
func GateVerdictAt(h []ledger.Transition, i int) (pass, ok bool) {
	if i < 0 || i >= len(h) {
		return false, false
	}
	switch r := h[i].Reason; {
	case r == "GATE_PASSED":
		return true, true
	case r == "RETRY GATE_FAILED", r == "GATE_FAILED_AFTER_REVIEW", strings.HasPrefix(r, "GATE_RECLASSIFY"):
		return false, true
	case r == "ATTEMPTS_EXHAUSTED" && i > 0 && (h[i-1].To == ledger.Gating || h[i-1].To == ledger.RetryEnv):
		// gate's only retry is GATE_FAILED; reconcile's comes out of RUNNING.
		return false, true
	}
	return false, false
}

// FirstGateVerdict is the index of the item's first valid verdict, or -1. An
// item has one PR for life (reconcile refuses a second links.pr), so the
// first verdict in its history is the first for its current PR.
func FirstGateVerdict(h []ledger.Transition) int {
	for i := range h {
		if _, ok := GateVerdictAt(h, i); ok {
			return i
		}
	}
	return -1
}
