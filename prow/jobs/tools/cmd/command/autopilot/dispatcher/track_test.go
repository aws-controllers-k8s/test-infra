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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

// inReview is w in HUMAN_REVIEW on PR 7 after a passing gate at head1.
func inReview(w *ledger.WorkItem, author string) *ledger.WorkItem {
	w = running(w, "2026-11-12T07:00:00Z", 1, 3)
	id := w.Claim.LaunchID
	w.Claim = nil
	w.Links.PR = s3PR
	w.State = ledger.HumanReview
	w.History = append(w.History, ledger.Transition{TS: "2026-11-12T08:00:00Z", To: ledger.HumanReview, By: actor, Reason: "MARKED_READY"})
	w.Gate = &ledger.Gate{Source: ledger.GateSourceSDKBump, LaunchID: id, HeadSHA: head1, BaseSHA: base1,
		Verdict: ledger.GatePass, Digest: "sha256:x", Gates: 1, TS: "2026-11-12T08:00:00Z"}
	w.Review = &ledger.Review{Number: 7, Author: author, WouldMerge: ledger.WouldMergeYes, ReadyAt: "2026-11-12T08:00:00Z",
		LastActivity: "2026-11-12T08:00:00Z"}
	return w
}

func readyPR(author string) PR {
	p := openPR(author)
	p.Draft = false
	return p
}

func trackEnv(gh *fakeGH, l Launcher) prEnv { return testEnv(gh, l, true) }

func TestTrackMergedPRIsDoneAndUnblocksDependents(t *testing.T) {
	bump := inReview(item(bumpKey, "SDK-bump", ledger.Queued), "ack-bot")
	field := item(fieldKey, "config-only", ledger.Blocked)
	field.DependsOn = []string{bumpKey}
	field.History = append(field.History, ledger.Transition{TS: t0, To: ledger.Blocked, By: actor, Reason: "DEPENDS_ON x"})
	gh := newFakeGH()
	p := readyPR("ack-bot")
	p.State, p.Merged, p.MergedAt, p.MergeSHA = "closed", true, "2026-11-12T08:50:00Z", head2
	gh.prs[7] = p
	gh.commits[7] = []PRCommit{{SHA: head1, Author: "ack-bot", Date: "2026-11-12T07:30:00Z"}}
	items := ledgerOf(bump, field)

	assert.Empty(t, trackPRs(context.Background(), items, trackEnv(gh, &fakeLauncher{})))
	assert.Equal(t, ledger.Done, bump.State)
	assert.Equal(t, []ledger.State{ledger.Merged, ledger.Done}, states(bump)[len(bump.History)-2:])
	assert.Equal(t, "2026-11-12T08:50:00Z", bump.Review.MergedAt)
	assert.Equal(t, head2, bump.Review.MergeSHA)
	assert.Equal(t, ledger.WouldMergeYes, bump.Review.WouldMerge)

	route(items, "2026-11-12T09:00:00Z")
	assert.Equal(t, ledger.Queued, field.State, "the merged bump unblocks its dependent")
	assertLints(t, items)
}

func TestTrackMergedFreesTheReviewSlot(t *testing.T) {
	p := testPolicy(t)
	var ws []*ledger.WorkItem
	gh := newFakeGH()
	for i := 0; i < p.Budgets.MaxPRsAwaitingReview; i++ {
		w := inReview(item("sdk-bump|svc"+string(rune('a'+i))+"|*|v1", "SDK-bump", ledger.Queued), "ack-bot")
		ws = append(ws, w)
	}
	queued := item("sdk-bump|s3|*|service/s3/v9.0.0", "SDK-bump", ledger.Queued)
	// Band 2 skips the per-class caps (the six runs above were launched
	// today); review backpressure applies to every band.
	queued.PriorityBand = 2
	items := ledgerOf(append(ws, queued)...)
	_, waits := schedule(items, p, now)
	require.Len(t, waits, 1)
	assert.Equal(t, CapReview, waits[0].cap)

	merged := readyPR("ack-bot")
	merged.State, merged.Merged, merged.MergedAt = "closed", true, "2026-11-12T08:50:00Z"
	gh.prs[7] = merged
	// Only the first item's PR is 7 in its repo; the others' repos differ, but
	// the fake serves PR 7 to every repo: all merge.
	trackPRs(context.Background(), items, trackEnv(gh, &fakeLauncher{}))
	picks, _ := schedule(items, p, now)
	assert.Len(t, picks, 1, "merged PRs no longer count against max_prs_awaiting_review")
}

func TestTrackClosedPRIsEscalated(t *testing.T) {
	w := inReview(item(bumpKey, "SDK-bump", ledger.Queued), "ack-bot")
	gh := newFakeGH()
	p := readyPR("ack-bot")
	p.State, p.ClosedAt = "closed", "2026-11-12T08:55:00Z"
	gh.prs[7] = p
	items := ledgerOf(w)
	trackPRs(context.Background(), items, trackEnv(gh, &fakeLauncher{}))
	assert.Equal(t, ledger.Escalated, w.State)
	assert.Equal(t, &ledger.Verdict{Outcome: OutcomeNeedsHuman, Reason: "PR_CLOSED_UNMERGED"}, w.Verdict)
	assert.Equal(t, "2026-11-12T08:55:00Z", w.Review.ClosedAt)
	assertLints(t, items)
}

func TestTrackMergeWhileGatingAbortsTheDodJob(t *testing.T) {
	w := gating(running(item(fieldKey, "config-only", ledger.Queued), "2026-11-12T08:30:00Z", 1, 3))
	gh := newFakeGH()
	gh.prs[7] = openPR("ack-agent")
	l := gateLauncher{fakeLauncher: &fakeLauncher{statuses: map[string]Status{}}, verdicts: map[string][]byte{}}
	gate(context.Background(), ledgerOf(w), testEnv(gh, l, true))
	dod := w.Claim.LaunchID
	p := gh.prs[7]
	p.State, p.Merged, p.MergedAt = "closed", true, "2026-11-12T08:59:00Z"
	gh.prs[7] = p
	trackPRs(context.Background(), ledgerOf(w), trackEnv(gh, l))
	assert.Equal(t, ledger.Done, w.State)
	assert.Equal(t, []string{dod}, l.aborted)
	assert.Nil(t, w.Claim)
	assert.Empty(t, w.Review.WouldMerge, "merged before any gate passed: no verdict to agree with")
}

func TestTrackReviewRoundMovesToFeedbackAndPins(t *testing.T) {
	w := inReview(item(fieldKey, "config-only", ledger.Queued), "ack-agent")
	gh := newFakeGH()
	gh.prs[7] = readyPR("ack-agent")
	gh.reviews[7] = []PRReview{
		{ID: 1, Author: "alice", State: "APPROVED", SubmittedAt: "2026-11-12T08:10:00Z"},
		{ID: 2, Author: "mallory", State: "CHANGES_REQUESTED", SubmittedAt: "2026-11-12T08:20:00Z"},
		{ID: 3, Author: "alice", State: "CHANGES_REQUESTED", SubmittedAt: "2026-11-12T08:30:00Z"},
	}
	items := ledgerOf(w)
	trackPRs(context.Background(), items, trackEnv(gh, &fakeLauncher{}))
	assert.Equal(t, ledger.Feedback, w.State)
	assert.Equal(t, "CHANGES_REQUESTED", last(w).Reason)
	assert.Equal(t, 1, w.Review.ReviewRounds, "only team members' non-approval reviews count")
	assert.Equal(t, 1, w.Review.FeedbackRounds)
	assert.Equal(t, int64(3), w.Review.LastReviewID)
	assert.True(t, w.Review.PinnedHumanMerge)
	assert.Equal(t, ledger.WouldMergeNo, w.Review.WouldMerge)
	assertLints(t, items)

	// The same reviews again change nothing.
	n := len(w.History)
	trackPRs(context.Background(), items, trackEnv(gh, &fakeLauncher{}))
	assert.Len(t, w.History, n)
	assert.Equal(t, 1, w.Review.ReviewRounds)
}

func TestTrackHeadMoveRegates(t *testing.T) {
	w := inReview(item(fieldKey, "config-only", ledger.Queued), "ack-agent")
	gh := newFakeGH()
	p := readyPR("ack-agent")
	p.HeadSHA = head2
	gh.prs[7] = p
	gh.commits[7] = []PRCommit{
		{SHA: head1, Author: "ack-agent", Date: "2026-11-12T07:30:00Z"},
		{SHA: head2, Author: "alice", Date: "2026-11-12T08:40:00Z"},
	}
	l := gateLauncher{fakeLauncher: &fakeLauncher{statuses: map[string]Status{}}, verdicts: map[string][]byte{}}
	items := ledgerOf(w)
	trackPRs(context.Background(), items, trackEnv(gh, l))
	assert.Equal(t, ledger.Gating, w.State)
	require.Len(t, l.launched, 1)
	assert.Equal(t, head2, l.launched[0].Args["head-sha"])
	assert.Equal(t, 1, w.Review.HumanCommits)
	assert.True(t, w.Review.PinnedHumanMerge)
	assert.Equal(t, 2, w.Gate.Gates)
	assertLints(t, items)
}

func TestTrackWaitsWhileAddressFeedbackRuns(t *testing.T) {
	w := inReview(item(fieldKey, "config-only", ledger.Queued), "ack-agent")
	gh := newFakeGH()
	p := readyPR("ack-agent")
	p.HeadSHA = head2
	gh.prs[7] = p
	l := gateLauncher{fakeLauncher: &fakeLauncher{statuses: map[string]Status{}}, feedback: true}
	trackPRs(context.Background(), ledgerOf(w), trackEnv(gh, l))
	assert.Equal(t, ledger.Feedback, w.State)
	assert.Equal(t, "FEEDBACK_RUNNING", last(w).Reason)
	assert.Empty(t, l.launched)
}

func TestTrackStaleRequeuesOnceThenEscalates(t *testing.T) {
	w := inReview(item(bumpKey, "SDK-bump", ledger.Queued), "ack-bot")
	gh := newFakeGH()
	gh.prs[7] = readyPR("ack-bot")
	e := trackEnv(gh, &fakeLauncher{})
	e.now = now.Add(15 * 24 * time.Hour)
	e.ts = e.now.Format(time.RFC3339)
	items := ledgerOf(w)
	trackPRs(context.Background(), items, e)
	assert.Equal(t, ledger.Queued, w.State)
	assert.Equal(t, []ledger.State{ledger.Stale, ledger.Queued}, states(w)[len(w.History)-2:])
	assert.Equal(t, "RETRY STALE_REQUEUE", last(w).Reason)
	assert.Equal(t, 1, w.Review.StaleRequeues)
	assertLints(t, items)

	// Its next run reuses the PR and passes again; stale a second time.
	w.State = ledger.HumanReview
	w.History = append(w.History, ledger.Transition{TS: e.ts, To: ledger.HumanReview, By: actor})
	e.now = e.now.Add(15 * 24 * time.Hour)
	e.ts = e.now.Format(time.RFC3339)
	trackPRs(context.Background(), items, e)
	assert.Equal(t, ledger.Escalated, w.State)
	assert.Equal(t, "STALE_TWICE", w.Verdict.Reason)
}

func TestTrackStaleAfterReviewEscalates(t *testing.T) {
	w := inReview(item(fieldKey, "config-only", ledger.Queued), "ack-agent")
	w.Review.PinnedHumanMerge, w.Review.WouldMerge = true, ledger.WouldMergeNo
	gh := newFakeGH()
	gh.prs[7] = readyPR("ack-agent")
	e := trackEnv(gh, &fakeLauncher{})
	e.now = now.Add(15 * 24 * time.Hour)
	e.ts = e.now.Format(time.RFC3339)
	trackPRs(context.Background(), ledgerOf(w), e)
	assert.Equal(t, ledger.Escalated, w.State)
	assert.Equal(t, "STALE_AFTER_REVIEW", w.Verdict.Reason)
}

func TestTrackMarksReadyOnceTheBlockLifts(t *testing.T) {
	w := inReview(item(bumpKey, "SDK-bump", ledger.Queued), "ack-bot")
	w.Review.ReadyAt = ""
	gh := newFakeGH()
	p := openPR("ack-bot")
	p.Labels = []string{LabelBlockedServerDefault}
	gh.prs[7] = p
	trackPRs(context.Background(), ledgerOf(w), trackEnv(gh, &fakeLauncher{}))
	assert.Empty(t, gh.ready)

	p.Labels = nil
	gh.prs[7] = p
	trackPRs(context.Background(), ledgerOf(w), trackEnv(gh, &fakeLauncher{}))
	assert.Equal(t, []int{7}, gh.ready)
	assert.NotEmpty(t, w.Review.ReadyAt)

	// A person who turns it back into a draft is not overruled.
	p = gh.prs[7]
	p.Draft = true
	gh.prs[7] = p
	trackPRs(context.Background(), ledgerOf(w), trackEnv(gh, &fakeLauncher{}))
	assert.Equal(t, []int{7}, gh.ready)
}

func TestTrackReadsOnlyLinkedPRs(t *testing.T) {
	w := inReview(item(bumpKey, "SDK-bump", ledger.Queued), "ack-bot")
	w.Links.PR = "https://github.com/someone/else/pull/7"
	w.Review = nil
	items := ledgerOf(w)
	trackPRs(context.Background(), items, trackEnv(newFakeGH(), &fakeLauncher{}))
	assert.Equal(t, ledger.Escalated, w.State)
	assert.Equal(t, "INVALID_PR_LINK", w.Verdict.Reason)
}

// TestTrackMarksReadyAgainAfterANewGate (Plan 9 review F5): ReadyAt holds
// only within one gate pass. A PR marked ready, back to draft while a new
// head re-gates, is marked ready again when that head passes.
func TestTrackMarksReadyAgainAfterANewGate(t *testing.T) {
	w := inReview(item(fieldKey, "config-only", ledger.Queued), "ack-agent")
	gh := newFakeGH()
	gh.trees[head2] = tree1
	p := openPR("ack-agent")
	p.HeadSHA = head2
	gh.prs[7] = p
	gh.commits[7] = []PRCommit{
		{SHA: head1, Author: "ack-agent", Date: "2026-11-12T07:30:00Z"},
		{SHA: head2, Author: "ack-agent", Date: "2026-11-12T08:40:00Z"},
	}
	l := gateLauncher{fakeLauncher: &fakeLauncher{statuses: map[string]Status{}}, verdicts: map[string][]byte{}}
	e := trackEnv(gh, l)
	items := ledgerOf(w)
	trackPRs(context.Background(), items, e)
	require.Equal(t, ledger.Gating, w.State)
	id := w.Claim.LaunchID
	l.statuses[id] = Status{Phase: PhaseFinished, Job: JobSucceeded}
	l.verdicts[id] = verdictFor(t, w, dod.StatusPass, "")
	assert.Empty(t, gate(context.Background(), items, e))
	assert.Equal(t, ledger.HumanReview, w.State)
	assert.Equal(t, "MARKED_READY", last(w).Reason)
	assert.Equal(t, []int{7}, gh.ready)
	assertLints(t, items)
}
