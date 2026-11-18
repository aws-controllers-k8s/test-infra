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
	"fmt"
	"slices"
	"time"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

// HumanReviewIdle is how long a PR may sit with no gate, review or commit
// before it is STALE (design 5.4 timeout ladder: human review 14d).
const HumanReviewIdle = 14 * 24 * time.Hour

// trackPRs follows the PR of every item that has one (Plan 9, design 5.4 and
// 5.5): merged -> MERGED -> DONE, closed -> ESCALATED, review rounds and human
// commits into the item's Review (the gate-agreement inputs), FEEDBACK while
// a person asks for changes, a re-gate when the head moves, and STALE after
// HumanReviewIdle. It reads the PR links.pr names, never one found by
// trailer. It returns one problem per PR it could not read.
func trackPRs(ctx context.Context, items map[string]*ledger.WorkItem, e prEnv) []string {
	var problems []string
	for _, k := range sortedKeys(items) {
		w := items[k]
		switch w.State {
		case ledger.Gating, ledger.RetryEnv, ledger.PRReady, ledger.HumanReview, ledger.Feedback, ledger.Stale:
		default:
			continue
		}
		if w.Links.PR == "" {
			continue
		}
		if err := trackOne(ctx, w, e); err != nil {
			problems = append(problems, fmt.Sprintf("track %s: %v", w.Links.PR, err))
		}
	}
	return problems
}

func trackOne(ctx context.Context, w *ledger.WorkItem, e prEnv) error {
	n, ok := prNumber(w)
	if !ok {
		abortClaim(ctx, w, e)
		finish(w, ledger.Escalated, e.ts, "INVALID_PR_LINK", &ledger.Verdict{Outcome: OutcomeNeedsHuman, Reason: "INVALID_PR_LINK"})
		return nil
	}
	pr, err := e.gh.PR(ctx, w.Repo, n)
	if err != nil {
		return err
	}
	if w.Review == nil {
		w.Review = &ledger.Review{Number: n, Author: pr.Author}
	}
	r := w.Review
	if err := countCommits(ctx, w, pr, e); err != nil {
		return err
	}
	if pr.Merged || pr.State != "open" {
		// The final record: count the reviews; no round moves the item now.
		if _, err := countReviews(ctx, w, pr, e); err != nil {
			return err
		}
	}
	switch {
	case pr.Merged:
		abortClaim(ctx, w, e)
		r.MergedAt, r.MergeSHA = pr.MergedAt, pr.MergeSHA
		finish(w, ledger.Merged, e.ts, "PR_MERGED", nil)
		// Phase 1 tracks no release (design 5.4 RELEASED/VERIFIED come
		// later): the change is on main, so the item is done.
		transition(w, ledger.Done, e.ts, "NO_RELEASE_TRACKING")
		return nil
	case pr.State != "open":
		abortClaim(ctx, w, e)
		r.ClosedAt = pr.ClosedAt
		finish(w, ledger.Escalated, e.ts, "PR_CLOSED_UNMERGED", &ledger.Verdict{Outcome: OutcomeNeedsHuman, Reason: "PR_CLOSED_UNMERGED"})
		return nil
	}
	if w.State.ActiveRun() || w.State == ledger.PRReady {
		// gate owns the item until it reaches HUMAN_REVIEW. Reviews wait
		// (LastReviewID does not move), so a change request made while
		// gating moves the item to FEEDBACK later (Plan 9 review F8).
		return nil
	}
	rounds, err := countReviews(ctx, w, pr, e)
	if err != nil {
		return err
	}

	if w.Gate != nil && pr.HeadSHA != w.Gate.HeadSHA {
		active, err := feedbackActive(ctx, e.l, w.Repo, n)
		if err != nil {
			return err
		}
		// Pinning comes only from countCommits and countReviews (human
		// commits, team reviews), not from the head move itself: the
		// bot's own push does not pin the PR (Plan 9 review F7).
		derr := unready(ctx, w, pr, e)
		if active {
			toFeedback(w, e.ts, "FEEDBACK_RUNNING")
			return derr
		}
		// Someone pushed: the feedback agent for a reviewer, or a person.
		// The new head needs its own verdict (design 5.5 rule 4).
		return errors.Join(derr, launchGate(ctx, w, pr, e, "HEAD_MOVED"))
	}
	if rounds > 0 {
		toFeedback(w, e.ts, "CHANGES_REQUESTED")
		return nil
	}
	if e.now.Sub(mustParse(r.LastActivity, e.now)) > HumanReviewIdle {
		stale(w, e.ts)
		return nil
	}
	if w.State == ledger.HumanReview {
		_, err := ensureReady(ctx, w, pr, e)
		return err
	}
	return nil
}

// countCommits updates the Review's human commits and its last activity
// from the PR's commits and gate. Any human commit pins the PR to
// human-merge for good.
func countCommits(ctx context.Context, w *ledger.WorkItem, pr PR, e prEnv) error {
	r := w.Review
	commits, err := e.gh.Commits(ctx, w.Repo, pr.Number)
	if err != nil {
		return err
	}
	humans := 0
	for _, c := range commits {
		if c.Author == "" || !slices.Contains(e.bots, c.Author) {
			humans++
		}
		r.LastActivity = latest(r.LastActivity, c.Date)
	}
	if humans > r.HumanCommits {
		r.HumanCommits = humans
		pin(r)
	}
	if w.Gate != nil {
		r.LastActivity = latest(r.LastActivity, w.Gate.TS)
	}
	return nil
}

// countReviews updates the Review's review rounds and last activity from
// the reviews since LastReviewID, and returns how many rounds are new. A
// team member's non-approval review pins the PR to human-merge for good.
func countReviews(ctx context.Context, w *ledger.WorkItem, pr PR, e prEnv) (int, error) {
	r := w.Review
	reviews, err := e.gh.Reviews(ctx, w.Repo, pr.Number)
	if err != nil {
		return 0, err
	}
	rounds := 0
	for _, rv := range reviews {
		if rv.ID <= r.LastReviewID {
			continue
		}
		r.LastReviewID = rv.ID
		r.LastActivity = latest(r.LastActivity, rv.SubmittedAt)
		if !e.team[rv.Author] || rv.Author == pr.Author {
			continue
		}
		if rv.State == "CHANGES_REQUESTED" || rv.State == "COMMENTED" {
			r.ReviewRounds++
			rounds++
			pin(r)
		}
	}
	return rounds, nil
}

// pin marks the PR human-merge for good and its gate-agreement record
// "would-merge = no" (design 5.5 rule 4).
func pin(r *ledger.Review) {
	r.PinnedHumanMerge = true
	r.WouldMerge = ledger.WouldMergeNo
}

// toFeedback is HUMAN_REVIEW -> FEEDBACK, counted once per round.
func toFeedback(w *ledger.WorkItem, ts, reason string) {
	if w.State == ledger.Feedback {
		return
	}
	w.Review.FeedbackRounds++
	transition(w, ledger.Feedback, ts, reason)
}

// stale is HUMAN_REVIEW|FEEDBACK -> STALE -> requeue once -> ESCALATED
// (design 5.4). A PR a person has changed is never requeued: a new run
// would overwrite their commits.
func stale(w *ledger.WorkItem, ts string) {
	r := w.Review
	transition(w, ledger.Stale, ts, "IDLE_14D")
	switch {
	case r.PinnedHumanMerge:
		finish(w, ledger.Escalated, ts, "STALE_AFTER_REVIEW", &ledger.Verdict{Outcome: OutcomeNeedsHuman, Reason: "STALE_AFTER_REVIEW"})
	case r.StaleRequeues > 0:
		finish(w, ledger.Escalated, ts, "STALE_TWICE", &ledger.Verdict{Outcome: OutcomeNeedsHuman, Reason: "STALE_TWICE"})
	default:
		r.StaleRequeues++
		retry(w, "STALE_REQUEUE", ts)
	}
}

func feedbackActive(ctx context.Context, l Launcher, repo string, n int) (bool, error) {
	fj, ok := l.(FeedbackJobs)
	if !ok {
		return false, nil
	}
	return fj.FeedbackActive(ctx, repo, n)
}

// mustParse parses an RFC 3339 time; an empty or bad one is def.
func mustParse(s string, def time.Time) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return def
	}
	return t
}
