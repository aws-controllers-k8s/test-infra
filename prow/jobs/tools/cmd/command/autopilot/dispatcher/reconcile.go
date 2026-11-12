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
	OutcomeNoChange   = "completed_no_change"
	OutcomeNeedsHuman = "needs_human"
	OutcomeFailed     = "failed"
	wontFixPrefix     = "wont_fix:"
)

var (
	wontFixReasons = map[string]bool{"not_real": true, "aws_api_blocker": true, "env_blocker": true, "product_decision": true}
	reasonRE       = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	prNumberRE     = regexp.MustCompile(`^[1-9][0-9]{0,9}$`)
)

// reconcile applies the result of every active run that finished and reaps
// runs past their deadline plus grace. It returns one problem per run whose
// status could not be read; those items are left as they are.
func reconcile(ctx context.Context, items map[string]*ledger.WorkItem, l Launcher, b policy.Budgets, now time.Time) []string {
	ts := now.UTC().Format(time.RFC3339)
	var problems []string
	for _, k := range sortedKeys(items) {
		w := items[k]
		if !w.State.ActiveRun() || w.Claim == nil {
			continue
		}
		st, err := l.Status(ctx, w.Claim.LaunchID)
		if err != nil {
			problems = append(problems, fmt.Sprintf("status of %s for %s: %v", w.Claim.LaunchID, w.Key, err))
			continue
		}
		switch {
		case st.Phase == PhaseFinished && st.Result != nil:
			apply(w, *st.Result, ts)
		case st.Phase == PhaseFinished:
			retry(w, "INFRA_NO_VERDICT", ts)
		case overdue(w.Claim, b.ReaperGrace(), now):
			retry(w, "INFRA_NO_VERDICT", ts)
		}
	}
	return problems
}

func apply(w *ledger.WorkItem, r RunResult, ts string) {
	// A terminal outcome with no reason records the outcome itself as the
	// token, e.g. COMPLETED_NO_CHANGE or WONT_FIX_NOT_REAL.
	outcomeToken := strings.ToUpper(strings.ReplaceAll(r.Outcome, ":", "_"))
	switch {
	case r.Outcome == OutcomePROpened:
		if !ownPR(w.Repo, r.PR) {
			finish(w, ledger.Escalated, ts, "INVALID_PR_LINK", &ledger.Verdict{Outcome: OutcomeNeedsHuman, Reason: "INVALID_PR_LINK"})
			return
		}
		w.Links.PR = r.PR
		finish(w, ledger.HumanReview, ts, "PR_OPENED", nil)
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
		finish(w, ledger.Escalated, ts, "UNKNOWN_VERDICT", &ledger.Verdict{Outcome: OutcomeNeedsHuman, Reason: "UNKNOWN_VERDICT " + strconv.Quote(r.Outcome)})
	}
}

// retry requeues a failed run while it has attempts left, else escalates.
func retry(w *ledger.WorkItem, reason, ts string) {
	if w.Attempts == nil || w.Attempts.Runs >= w.Attempts.MaxRuns {
		finish(w, ledger.Escalated, ts, "ATTEMPTS_EXHAUSTED", &ledger.Verdict{Outcome: OutcomeFailed, Reason: reason})
		return
	}
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

// ownPR reports whether pr is a pull request URL in repo.
func ownPR(repo, pr string) bool {
	n, ok := strings.CutPrefix(pr, "https://github.com/"+repo+"/pull/")
	return ok && prNumberRE.MatchString(n)
}

// overdue reports whether the claim's deadline plus grace has passed. An
// unreadable deadline counts as passed, so a hand-edited claim is reaped.
func overdue(c *ledger.Claim, grace time.Duration, now time.Time) bool {
	d, err := time.Parse(time.RFC3339, c.Deadline)
	return err != nil || now.After(d.Add(grace))
}
