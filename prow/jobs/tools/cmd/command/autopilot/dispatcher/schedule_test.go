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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

func queued(key, class string, band int, since string) *ledger.WorkItem {
	w := item(key, class, ledger.Queued)
	w.PriorityBand = band
	w.History = []ledger.Transition{{TS: since, To: ledger.Queued, By: actor}}
	return w
}

func pickKeys(c []candidate) []string {
	var out []string
	for _, x := range c {
		out = append(out, x.item.Key)
	}
	return out
}

func waitCaps(ws []capped) map[string]string {
	out := map[string]string{}
	for _, w := range ws {
		out[w.item.Key] = w.cap
	}
	return out
}

func TestScheduleOrder(t *testing.T) {
	a := queued("sdk-bump|s3|*|v1", "SDK-bump", 3, "2026-11-12T08:10:00Z")
	b := queued("sdk-bump|iam|*|v1", "SDK-bump", 3, "2026-11-12T08:00:00Z")
	c := queued("api-change|kms|Key|spec:X|needs-config", "config-only", 2, "2026-11-12T08:20:00Z")
	p := testPolicy(t)
	p.Switches.Services = append(p.Switches.Services, "kms")
	picks, waits := schedule(ledgerOf(a, b, c), p, now)
	assert.Empty(t, waits)
	assert.Equal(t, []string{c.Key, b.Key, a.Key}, pickKeys(picks))
}

func TestScheduleLeastRecentlyVisitedRepoFirst(t *testing.T) {
	a := queued("sdk-bump|s3|*|v1", "SDK-bump", 3, t0)
	b := queued("sdk-bump|iam|*|v1", "SDK-bump", 3, t0)
	// s3 was visited (a done item ran there); iam never was.
	old := running(item("sdk-bump|s3|*|v0", "SDK-bump", ledger.Queued), "2026-11-01T08:00:00Z", 1, 3)
	old.State, old.Claim = ledger.Done, nil
	picks, _ := schedule(ledgerOf(a, b, old), testPolicy(t), now)
	assert.Equal(t, []string{b.Key, a.Key}, pickKeys(picks))
}

func TestScheduleKillSwitches(t *testing.T) {
	a := queued("sdk-bump|s3|*|v1", "SDK-bump", 3, t0)
	b := queued("sdk-bump|sqs|*|v1", "SDK-bump", 3, t0)
	c := queued("sdk-bump|iam|*|v1", "SDK-bump", 3, t0)
	p := testPolicy(t)
	p.Switches.PausedRepos = []string{"aws-controllers-k8s/iam-controller"}
	picks, waits := schedule(ledgerOf(a, b, c), p, now)
	assert.Equal(t, []string{a.Key}, pickKeys(picks))
	assert.Equal(t, map[string]string{b.Key: CapServiceNotEnabled, c.Key: CapRepoPaused}, waitCaps(waits))

	p.Switches.Paused = true
	picks, waits = schedule(ledgerOf(a), p, now)
	assert.Empty(t, picks)
	assert.Equal(t, map[string]string{a.Key: CapPaused}, waitCaps(waits))
}

func TestSchedulePerRepoAndWithinTick(t *testing.T) {
	busy := running(item("sdk-bump|iam|*|v0", "SDK-bump", ledger.Queued), "2026-11-12T08:30:00Z", 1, 3)
	a := queued("sdk-bump|iam|*|v1", "SDK-bump", 3, t0)
	b := queued("api-change|s3|Bucket|spec:A|needs-config", "config-only", 4, t0)
	c := queued("api-change|s3|Bucket|spec:B|needs-config", "config-only", 4, t0)
	picks, waits := schedule(ledgerOf(busy, a, b, c), testPolicy(t), now)
	assert.Equal(t, []string{b.Key}, pickKeys(picks))
	assert.Equal(t, map[string]string{a.Key: CapRepoBusy, c.Key: CapRepoBusy}, waitCaps(waits))
}

func TestScheduleLLMCap(t *testing.T) {
	r1 := running(item("api-change|iam|Role|spec:A|needs-config", "config-only", ledger.Queued), "2026-11-12T08:30:00Z", 1, 3)
	r2 := running(item("api-change|rds|DBInstance|spec:A|needs-config", "config-only", ledger.Queued), "2026-11-12T08:30:00Z", 1, 3)
	q := queued("api-change|s3|Bucket|spec:A|needs-config", "config-only", 4, t0)
	bump := queued("sdk-bump|eks|*|v1", "SDK-bump", 3, t0)
	picks, waits := schedule(ledgerOf(r1, r2, q, bump), testPolicy(t), now)
	assert.Equal(t, []string{bump.Key}, pickKeys(picks), "sdk-bump is not an LLM run")
	assert.Equal(t, map[string]string{q.Key: CapLLM}, waitCaps(waits))
}

func TestScheduleDailyCaps(t *testing.T) {
	var items []*ledger.WorkItem
	// Five SDK-bump launches in the last 24h (repos other than s3).
	for i, svc := range []string{"a", "b", "c", "d", "e"} {
		w := running(item(fmt.Sprintf("sdk-bump|%s|*|v0", svc), "SDK-bump", ledger.Queued),
			now.Add(-time.Duration(i+1)*time.Hour).Format(time.RFC3339), 1, 3)
		w.State, w.Claim = ledger.Done, nil
		items = append(items, w)
	}
	// An ec2 launch 2h ago (expensive), and one 30h ago that no longer counts.
	ec2 := running(item("sdk-bump|ec2|*|v0", "SDK-bump", ledger.Queued), now.Add(-30*time.Hour).Format(time.RFC3339), 1, 3)
	ec2.History = append(ec2.History, ledger.Transition{TS: now.Add(-2 * time.Hour).Format(time.RFC3339), To: ledger.Running, By: actor})
	ec2.State, ec2.Claim = ledger.Done, nil
	items = append(items, ec2)

	s3 := queued("sdk-bump|s3|*|v1", "SDK-bump", 3, t0)
	urgent := queued("sdk-bump|iam|*|v1", "SDK-bump", 2, t0)
	ec2q := queued("api-change|ec2|Instance|spec:A|needs-config", "config-only", 4, t0)
	items = append(items, s3, urgent, ec2q)

	picks, waits := schedule(ledgerOf(items...), testPolicy(t), now)
	assert.Equal(t, []string{urgent.Key}, pickKeys(picks), "band 2 skips the per-class daily cap")
	assert.Equal(t, map[string]string{s3.Key: CapSDKBumpDaily, ec2q.Key: CapExpensive}, waitCaps(waits))
}

func TestScheduleReviewBackpressureAndOpenPRs(t *testing.T) {
	var items []*ledger.WorkItem
	for _, svc := range []string{"a", "b", "c", "d", "e"} {
		items = append(items, item(fmt.Sprintf("sdk-bump|%s|*|v0", svc), "SDK-bump", ledger.HumanReview))
	}
	s3open := item("sdk-bump|s3|*|v0", "SDK-bump", ledger.HumanReview)
	s3next := queued("sdk-bump|s3|*|v1", "SDK-bump", 3, t0)
	iam := queued("sdk-bump|iam|*|v1", "SDK-bump", 3, t0)
	eks := queued("sdk-bump|eks|*|v1", "SDK-bump", 3, "2026-11-12T08:30:00Z")
	items = append(items, s3open, s3next, iam, eks)

	picks, waits := schedule(ledgerOf(items...), testPolicy(t), now)
	// Six PRs await review (the cap), so nothing more starts.
	assert.Empty(t, picks)
	assert.Equal(t, CapReview, waitCaps(waits)[iam.Key])

	// Drop one: iam fits; s3 already has an open SDK-bump PR; eks then hits review again.
	items = items[1:]
	picks, waits = schedule(ledgerOf(items...), testPolicy(t), now)
	assert.Equal(t, []string{iam.Key}, pickKeys(picks))
	assert.Equal(t, map[string]string{s3next.Key: CapOpenPRs, eks.Key: CapReview}, waitCaps(waits))
}

func TestScheduleRunsExhausted(t *testing.T) {
	w := queued("sdk-bump|s3|*|v1", "SDK-bump", 3, t0)
	w.Attempts = &ledger.Attempts{Runs: 3, MaxRuns: 3}
	picks, waits := schedule(ledgerOf(w), testPolicy(t), now)
	assert.Empty(t, picks)
	assert.Equal(t, map[string]string{w.Key: CapRuns}, waitCaps(waits))
}

func TestScheduleBand2SkipsOpenPRCap(t *testing.T) {
	open := item("sdk-bump|s3|*|v0", "SDK-bump", ledger.HumanReview)
	urgent := queued("sdk-bump|s3|*|v1", "SDK-bump", 2, t0)
	picks, waits := schedule(ledgerOf(open, urgent), testPolicy(t), now)
	assert.Equal(t, []string{urgent.Key}, pickKeys(picks))
	assert.Empty(t, waits)

	normal := queued("sdk-bump|s3|*|v1", "SDK-bump", 3, t0)
	picks, waits = schedule(ledgerOf(open, normal), testPolicy(t), now)
	assert.Empty(t, picks)
	assert.Equal(t, map[string]string{normal.Key: CapOpenPRs}, waitCaps(waits))
}

func TestScheduleReviewLoadMergeSoakVersusGating(t *testing.T) {
	build := func(extra *ledger.WorkItem) map[string]*ledger.WorkItem {
		var items []*ledger.WorkItem
		for _, svc := range []string{"a", "b", "c", "d", "e"} {
			items = append(items, item(fmt.Sprintf("sdk-bump|%s|*|v0", svc), "SDK-bump", ledger.HumanReview))
		}
		items = append(items, extra, queued("sdk-bump|iam|*|v1", "SDK-bump", 3, t0))
		return ledgerOf(items...)
	}
	// MERGE_SOAK is no longer waiting on a reviewer: 5 awaiting, iam fits.
	picks, waits := schedule(build(item("sdk-bump|f|*|v0", "SDK-bump", ledger.MergeSoak)), testPolicy(t), now)
	assert.Equal(t, []string{"sdk-bump|iam|*|v1"}, pickKeys(picks))
	assert.Empty(t, waits)

	// GATING is producing a human-merge PR: 6 awaiting, iam waits.
	picks, waits = schedule(build(item("sdk-bump|f|*|v0", "SDK-bump", ledger.Gating)), testPolicy(t), now)
	assert.Empty(t, picks)
	assert.Equal(t, map[string]string{"sdk-bump|iam|*|v1": CapReview}, waitCaps(waits))
}

func TestScheduleSDKBumpDailyCapWithinTick(t *testing.T) {
	p := testPolicy(t)
	p.Switches.Services = append(p.Switches.Services, "a", "b", "c", "d", "e", "f")
	p.Budgets.MaxPRsAwaitingReview = 100
	var items []*ledger.WorkItem
	for _, svc := range []string{"a", "b", "c", "d", "e", "f"} {
		items = append(items, queued(fmt.Sprintf("sdk-bump|%s|*|v1", svc), "SDK-bump", 3, t0))
	}
	picks, waits := schedule(ledgerOf(items...), p, now)
	assert.Equal(t, []string{items[0].Key, items[1].Key, items[2].Key, items[3].Key, items[4].Key}, pickKeys(picks))
	assert.Equal(t, map[string]string{items[5].Key: CapSDKBumpDaily}, waitCaps(waits))
}

func TestScheduleLLMCapWithinTick(t *testing.T) {
	p := testPolicy(t)
	p.Switches.Services = append(p.Switches.Services, "sqs")
	var items []*ledger.WorkItem
	for _, svc := range []string{"s3", "iam", "sqs"} {
		items = append(items, queued(fmt.Sprintf("api-change|%s|Thing|spec:A|needs-config", svc), "config-only", 4, t0))
	}
	picks, waits := schedule(ledgerOf(items...), p, now)
	assert.Len(t, picks, 2)
	assert.Equal(t, []string{items[1].Key, items[0].Key}, pickKeys(picks), "ties break by key: iam before s3")
	assert.Equal(t, map[string]string{items[2].Key: CapLLM}, waitCaps(waits))
}

func TestSchedule24HourBoundary(t *testing.T) {
	// ec2 is expensive with a cap of one launch per day.
	launched := func(ago time.Duration) *ledger.WorkItem {
		w := running(item("sdk-bump|ec2|*|v0", "SDK-bump", ledger.Queued), now.Add(-ago).Format(time.RFC3339), 1, 3)
		w.State, w.Claim = ledger.Done, nil
		return w
	}
	next := queued("api-change|ec2|Instance|spec:A|needs-config", "config-only", 4, t0)

	picks, waits := schedule(ledgerOf(launched(24*time.Hour), next), testPolicy(t), now)
	assert.Equal(t, []string{next.Key}, pickKeys(picks), "exactly 24h ago no longer counts")
	assert.Empty(t, waits)

	picks, waits = schedule(ledgerOf(launched(24*time.Hour-time.Minute), next), testPolicy(t), now)
	assert.Empty(t, picks)
	assert.Equal(t, map[string]string{next.Key: CapExpensive}, waitCaps(waits))
}

func TestScheduleRunsCapFallback(t *testing.T) {
	nilAttempts := queued("sdk-bump|s3|*|v1", "SDK-bump", 3, t0)
	picks, waits := schedule(ledgerOf(nilAttempts), testPolicy(t), now)
	assert.Equal(t, []string{nilAttempts.Key}, pickKeys(picks))
	assert.Empty(t, waits)

	zeroMax := queued("sdk-bump|s3|*|v1", "SDK-bump", 3, t0)
	zeroMax.Attempts = &ledger.Attempts{Runs: 3, MaxRuns: 0}
	picks, waits = schedule(ledgerOf(zeroMax), testPolicy(t), now)
	assert.Empty(t, picks)
	assert.Equal(t, map[string]string{zeroMax.Key: CapRuns}, waitCaps(waits))
}

func TestScheduleNoLane(t *testing.T) {
	w := queued("api-change|s3|Bucket|spec:A|hand-written", "hand-written-code", 4, t0)
	picks, waits := schedule(ledgerOf(w), testPolicy(t), now)
	assert.Empty(t, picks)
	assert.Equal(t, map[string]string{w.Key: CapNoLane}, waitCaps(waits))
}

func TestScheduleObserveWaits(t *testing.T) {
	w := queued("api-change|s3|Bucket|spec:A|config-only", "config-only", 4, t0)
	w.AutonomyLevel = "observe"
	picks, waits := schedule(ledgerOf(w), testPolicy(t), now)
	assert.Empty(t, picks)
	assert.Equal(t, map[string]string{w.Key: CapObserve}, waitCaps(waits))
}

func TestScheduleNoQueuedHistorySortsLast(t *testing.T) {
	a := queued("sdk-bump|s3|*|v1", "SDK-bump", 3, t0)
	a.History = nil
	b := queued("sdk-bump|iam|*|v1", "SDK-bump", 3, "2026-11-12T08:30:00Z")
	p := testPolicy(t)
	p.Budgets.MaxPRsAwaitingReview = 100
	picks, _ := schedule(ledgerOf(a, b), p, now)
	assert.Equal(t, []string{b.Key, a.Key}, pickKeys(picks))
}

func TestScheduleConfigOnlyLaneCapHoldsAboveThePolicy(t *testing.T) {
	r1 := running(item("api-change|iam|Role|spec:A|needs-config", "config-only", ledger.Queued), "2026-11-12T08:30:00Z", 1, 3)
	r2 := running(item("api-change|rds|DBInstance|spec:A|needs-config", "config-only", ledger.Queued), "2026-11-12T08:30:00Z", 1, 3)
	q := queued("api-change|s3|Bucket|spec:A|needs-config", "config-only", 4, t0)
	p := testPolicy(t)
	p.Budgets.MaxConcurrentLLMRuns = 8 // a ledger edit cannot raise the lane's 2
	picks, waits := schedule(ledgerOf(r1, r2, q), p, now)
	assert.Empty(t, picks)
	assert.Equal(t, map[string]string{q.Key: CapLane}, waitCaps(waits))
}

func TestScheduleCountsOrphans(t *testing.T) {
	q := queued("api-change|s3|Bucket|spec:A|needs-config", "config-only", 4, t0)
	bump := queued("sdk-bump|iam|*|v1", "SDK-bump", 3, t0)
	orphans := []ActiveJob{
		{LaunchID: "autopilot-1111111111111111-g1-r1", Workflow: "add-field", Service: "rds"},
		{LaunchID: "autopilot-2222222222222222-g1-r1", Workflow: "add-field", Service: "eks"},
		{LaunchID: "autopilot-3333333333333333-g1-r1", Workflow: "sdk-bump", Service: "iam"},
	}
	picks, waits := scheduleWith(ledgerOf(q, bump), testPolicy(t), now, orphans, nil)
	assert.Empty(t, picks)
	assert.Equal(t, map[string]string{q.Key: CapLLM, bump.Key: CapRepoBusy}, waitCaps(waits))
}

func TestScheduleAbortInflightLaunchesNothing(t *testing.T) {
	a := queued("sdk-bump|s3|*|v1", "SDK-bump", 0, t0)
	p := testPolicy(t)
	p.Switches.AbortInflight = true
	picks, waits := schedule(ledgerOf(a), p, now)
	assert.Empty(t, picks, "not even band 0")
	assert.Equal(t, map[string]string{a.Key: CapAborting}, waitCaps(waits))
}

func TestScheduleAdoptsFirstAndSkipsLoadCaps(t *testing.T) {
	r1 := running(item("api-change|iam|Role|spec:A|needs-config", "config-only", ledger.Queued), "2026-11-12T08:30:00Z", 1, 3)
	r2 := running(item("api-change|rds|DBInstance|spec:A|needs-config", "config-only", ledger.Queued), "2026-11-12T08:30:00Z", 1, 3)
	early := queued("api-change|eks|Cluster|spec:A|needs-config", "config-only", 4, t0)
	lost := queued("api-change|s3|Bucket|spec:A|needs-config", "config-only", 4, "2026-11-12T08:45:00Z")
	adopt := map[string]bool{nextLaunchID(lost): true}
	picks, waits := scheduleWith(ledgerOf(r1, r2, early, lost), testPolicy(t), now, nil, adopt)
	assert.Equal(t, []string{lost.Key}, pickKeys(picks), "already running: adopted whatever the load")
	assert.Equal(t, map[string]string{early.Key: CapLLM}, waitCaps(waits))

	p := testPolicy(t)
	p.Switches.Paused = true
	picks, _ = scheduleWith(ledgerOf(lost), p, now, nil, adopt)
	assert.Empty(t, picks, "the kill switches still apply")
}
