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
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/policy"
)

// Outcomes a run may report: design 4.1, plus pr_opened for a run that left
// a PR for the next state.
const (
	OutcomePROpened   = "pr_opened"
	OutcomePRUpdated  = "pr_updated" // address-feedback's, or a run that refreshed its open PR
	OutcomeNoChange   = "completed_no_change"
	OutcomeNeedsHuman = "needs_human"
	OutcomeFailed     = "failed"
	wontFixPrefix     = "wont_fix:"
)

var (
	wontFixReasons = map[string]bool{"not_real": true, "aws_api_blocker": true, "env_blocker": true, "product_decision": true}
	reasonRE       = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	outcomeRE      = regexp.MustCompile(`^[a-z][a-z0-9_:]{0,47}$`)
)

// reconcile applies the result of every active run that finished and reaps
// runs past their deadline plus grace. It returns one problem per run whose
// status could not be read; those items are left as they are.
func reconcile(ctx context.Context, items map[string]*ledger.WorkItem, l Launcher, b policy.Budgets, now time.Time) []string {
	ts := now.UTC().Format(time.RFC3339)
	var problems []string
	for _, k := range sortedKeys(items) {
		w := items[k]
		// GATING and RETRY_ENV belong to gate (Plan 9).
		if w.State != ledger.Running || w.Claim == nil {
			continue
		}
		st, err := l.Status(ctx, w.Claim.LaunchID)
		if err != nil {
			problems = append(problems, fmt.Sprintf("status of %s for %s: %v", w.Claim.LaunchID, w.Key, err))
			continue
		}
		switch {
		case st.Phase == PhaseFinished && st.Job == JobAborted:
			// Someone stopped the job (Deck, a newer run): a person decides
			// what happens next, so it is not retried.
			finish(w, ledger.Escalated, ts, "RUN_ABORTED", &ledger.Verdict{Outcome: OutcomeNeedsHuman, Reason: "RUN_ABORTED"})
		case st.Phase == PhaseFinished && st.Job == JobErrored:
			retry(w, "INFRA_JOB_ERROR", ts)
		case st.Phase == PhaseFinished && st.Result != nil && !consistent(st.Job, st.Result.Outcome):
			// The file and the exit code disagree: an agent may have written
			// the file. Nothing it reports reaches the ledger.
			retry(w, "RESULT_STATE_MISMATCH", ts)
		case st.Phase == PhaseFinished && st.Result != nil:
			apply(w, *st.Result, ts)
		case st.Phase == PhaseFinished:
			retry(w, cleanReason(st.Problem, "INFRA_NO_VERDICT"), ts)
		case overdue(w.Claim, b.ReaperGrace(), now) && (st.Phase == PhasePending || st.Phase == PhaseRunning):
			// Requeueing a live run could open a second PR while the first
			// is going, so stop it first, then escalate. A shadow (or
			// dry-run) launcher stops nothing.
			if !l.Shadow() {
				if err := l.Abort(ctx, w.Claim.LaunchID); err != nil {
					problems = append(problems, fmt.Sprintf("abort overdue %s for %s: %v", w.Claim.LaunchID, w.Key, err))
					continue
				}
			}
			finish(w, ledger.Escalated, ts, "RUN_OVERDUE", &ledger.Verdict{Outcome: OutcomeNeedsHuman, Reason: "RUN_OVERDUE"})
		case overdue(w.Claim, b.ReaperGrace(), now):
			retry(w, "INFRA_NO_VERDICT", ts)
		}
	}
	return problems
}

func apply(w *ledger.WorkItem, r RunResult, ts string) {
	addUsage(w, r)
	// A terminal outcome with no reason records the outcome itself as the
	// token, e.g. COMPLETED_NO_CHANGE or WONT_FIX_NOT_REAL.
	outcomeToken := strings.ToUpper(strings.ReplaceAll(r.Outcome, ":", "_"))
	switch {
	case r.Outcome == OutcomePROpened || r.Outcome == OutcomePRUpdated ||
		(r.Outcome == OutcomeFailed && r.Reason == "GATE_ENV" && r.PR != ""):
		// RUNNING -> GATING (design 5.4; Plan 9): gate reads the verdict for
		// the PR head. sdk-bump's GATE_ENV is a PR whose in-process gate
		// hit an environment failure: gate retries it with a dod job.
		if !ledger.PRInRepo(w.Repo, r.PR) || (w.Links.PR != "" && w.Links.PR != r.PR) {
			finish(w, ledger.Escalated, ts, "INVALID_PR_LINK", &ledger.Verdict{Outcome: OutcomeNeedsHuman, Reason: "INVALID_PR_LINK"})
			return
		}
		w.Links.PR = r.PR
		w.Verdict = nil
		transition(w, ledger.Gating, ts, strings.ToUpper(r.Outcome))
	case r.Outcome == OutcomeNoChange:
		reason := cleanReason(r.Reason, outcomeToken)
		finish(w, ledger.Done, ts, reason, &ledger.Verdict{Outcome: r.Outcome, Reason: reason})
	case r.Outcome == OutcomeNeedsHuman:
		reason := cleanReason(r.Reason, outcomeToken)
		finish(w, ledger.Escalated, ts, reason, &ledger.Verdict{Outcome: r.Outcome, Reason: reason})
	case strings.HasPrefix(r.Outcome, wontFixPrefix) && wontFixReasons[strings.TrimPrefix(r.Outcome, wontFixPrefix)]:
		reason := cleanReason(r.Reason, outcomeToken)
		finish(w, ledger.WontFix, ts, reason, &ledger.Verdict{Outcome: r.Outcome, Reason: reason})
	case r.Outcome == OutcomeFailed:
		retry(w, cleanReason(r.Reason, "FAILED"), ts)
	default:
		// The outcome is untrusted: keep it only if it looks like a token.
		reason := fmt.Sprintf("UNKNOWN_VERDICT len=%d", len(r.Outcome))
		if outcomeRE.MatchString(r.Outcome) {
			reason = "UNKNOWN_VERDICT " + strconv.Quote(r.Outcome)
		}
		finish(w, ledger.Escalated, ts, "UNKNOWN_VERDICT", &ledger.Verdict{Outcome: OutcomeNeedsHuman, Reason: reason})
	}
}

// consistent reports whether a run's reported outcome fits how its job ended
// (Plan 3 carry-over). prow-job.sh and sdk-bump exit 0 exactly when they
// record pr_opened, pr_updated or completed_no_change, and non-zero for failed
// and needs_human. An empty JobState (a launcher that cannot tell) skips the
// check.
func consistent(job JobState, outcome string) bool {
	switch job {
	case "":
		return true
	case JobSucceeded:
		return outcome == OutcomePROpened || outcome == OutcomePRUpdated || outcome == OutcomeNoChange
	case JobFailed:
		return outcome == OutcomeFailed || outcome == OutcomeNeedsHuman
	}
	return false
}

// retry requeues a failed run while it has attempts left, else escalates.
func retry(w *ledger.WorkItem, reason, ts string) {
	if w.Attempts == nil || w.Attempts.Runs >= w.Attempts.MaxRuns {
		finish(w, ledger.Escalated, ts, "ATTEMPTS_EXHAUSTED", &ledger.Verdict{Outcome: OutcomeFailed, Reason: reason})
		return
	}
	w.Verdict = nil
	finish(w, ledger.Queued, ts, "RETRY "+reason, nil)
}

func finish(w *ledger.WorkItem, to ledger.State, ts, reason string, v *ledger.Verdict) {
	w.Claim = nil
	if v != nil {
		w.Verdict = v
	}
	transition(w, to, ts, reason)
}

// cleanReason keeps a SCREAMING_SNAKE signature, uses fallback for an empty
// one, and replaces anything else, so a run's free text never reaches the
// ledger or the status comment.
func cleanReason(s, fallback string) string {
	switch {
	case s == "":
		return fallback
	case !reasonRE.MatchString(s):
		return "INVALID_REASON"
	}
	return s
}

// overdue reports whether the claim's deadline plus grace has passed. An
// unreadable deadline counts as passed, so a hand-edited claim is reaped.
func overdue(c *ledger.Claim, grace time.Duration, now time.Time) bool {
	d, err := time.Parse(time.RFC3339, c.Deadline)
	return err != nil || now.After(d.Add(grace))
}

// addUsage adds what a finished run spent to the item. apply runs once per
// run, because it clears the claim. The sums saturate rather than wrap:
// ParseResult bounds each run, and this is the backstop.
func addUsage(w *ledger.WorkItem, r RunResult) {
	if r.Tokens <= 0 && r.E2ERuns <= 0 {
		return
	}
	if w.Usage == nil {
		w.Usage = &ledger.Usage{}
	}
	if r.Tokens > 0 {
		if w.Usage.Tokens > math.MaxInt64-r.Tokens {
			w.Usage.Tokens = math.MaxInt64
		} else {
			w.Usage.Tokens += r.Tokens
		}
	}
	if r.E2ERuns > 0 {
		if w.Usage.E2ERuns > math.MaxInt-r.E2ERuns {
			w.Usage.E2ERuns = math.MaxInt
		} else {
			w.Usage.E2ERuns += r.E2ERuns
		}
	}
}
