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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

func hist(entries ...ledger.Transition) []ledger.Transition { return entries }

func to(s ledger.State, reason string) ledger.Transition {
	return ledger.Transition{TS: t0, To: s, By: actor, Reason: reason}
}

func TestRunEnd(t *testing.T) {
	run := to(ledger.Running, "autopilot-x-g1-r1")
	for name, tc := range map[string]struct {
		end  ledger.Transition
		want RunClass
	}{
		"pr opened":          {to(ledger.Gating, "PR_OPENED"), RunCompleted},
		"pr updated":         {to(ledger.Gating, "PR_UPDATED"), RunCompleted},
		"sdk-bump GATE_ENV":  {to(ledger.Gating, "FAILED"), RunCompleted},
		"no change":          {to(ledger.Done, "COMPLETED_NO_CHANGE"), RunCompleted},
		"needs human":        {to(ledger.Escalated, "NEEDS_HUMAN"), RunFailed},
		"invalid pr link":    {to(ledger.Escalated, "INVALID_PR_LINK"), RunFailed},
		"failed, retried":    {to(ledger.Queued, "RETRY BUILD_FAILED"), RunFailed},
		"reaped, retried":    {to(ledger.Queued, "RETRY INFRA_NO_VERDICT"), RunFailed},
		"reaped, exhausted":  {to(ledger.Escalated, "ATTEMPTS_EXHAUSTED"), RunFailed},
		"overdue":            {to(ledger.Escalated, "RUN_OVERDUE"), RunFailed},
		"job aborted":        {to(ledger.Escalated, "RUN_ABORTED"), RunFailed},
		"wont fix":           {to(ledger.WontFix, "WONT_FIX_NOT_REAL"), RunWontFix},
		"aborted by policy":  {to(ledger.Escalated, "ABORTED_BY_POLICY"), RunNone},
		"hand-edited, other": {to(ledger.Blocked, "X"), RunNone},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, RunEnd(hist(to(ledger.Queued, ""), run, tc.end), 2))
		})
	}
	h := hist(to(ledger.Queued, ""), to(ledger.Gating, "PR_OPENED"), to(ledger.Queued, "RETRY GATE_FAILED"))
	assert.Equal(t, RunNone, RunEnd(h, 2), "a gate requeue ends no run")
	assert.Equal(t, RunNone, RunEnd(h, 0))
	assert.Equal(t, RunNone, RunEnd(h, 3))
}

func TestGateVerdictAt(t *testing.T) {
	gating := to(ledger.Gating, "PR_OPENED")
	for name, tc := range map[string]struct {
		prev     ledger.Transition
		e        ledger.Transition
		pass, ok bool
	}{
		"passed":            {gating, to(ledger.PRReady, "GATE_PASSED"), true, true},
		"failed":            {gating, to(ledger.Queued, "RETRY GATE_FAILED"), false, true},
		"failed, exhausted": {gating, to(ledger.Escalated, "ATTEMPTS_EXHAUSTED"), false, true},
		"exhausted from env": {to(ledger.RetryEnv, "GATE_NO_VERDICT"), to(ledger.Escalated, "ATTEMPTS_EXHAUSTED"),
			false, true},
		"after review":  {gating, to(ledger.Escalated, "GATE_FAILED_AFTER_REVIEW"), false, true},
		"reclassify":    {gating, to(ledger.Escalated, "GATE_RECLASSIFY_HAND_WRITTEN_CODE"), false, true},
		"env retry":     {gating, to(ledger.RetryEnv, "GATE_ENV"), false, false},
		"env exhausted": {gating, to(ledger.Escalated, "GATE_ENV_EXHAUSTED"), false, false},
		"unsigned":      {gating, to(ledger.Escalated, "GATE_VERDICT_UNSIGNED"), false, false},
		"other item":    {gating, to(ledger.Escalated, "GATE_VERDICT_FOR_OTHER_ITEM"), false, false},
		"verify failed": {gating, to(ledger.RetryEnv, "GATE_VERIFY_FAILED"), false, false},
		"old head":      {gating, to(ledger.Gating, "GATE_HEAD_MOVED autopilot-x-g1-d2"), false, false},
		"stalled":       {gating, to(ledger.Escalated, "GATE_STALLED"), false, false},
		"run exhausted": {to(ledger.Running, "r1"), to(ledger.Escalated, "ATTEMPTS_EXHAUSTED"), false, false},
		"stale requeue": {to(ledger.Stale, "IDLE_14D"), to(ledger.Queued, "RETRY STALE_REQUEUE"), false, false},
	} {
		t.Run(name, func(t *testing.T) {
			pass, ok := GateVerdictAt(hist(tc.prev, tc.e), 1)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.pass, pass)
		})
	}
	_, ok := GateVerdictAt(nil, 0)
	assert.False(t, ok)
	h := hist(to(ledger.Gating, ""), to(ledger.Queued, "RETRY GATE_FAILED"), to(ledger.Gating, ""), to(ledger.PRReady, "GATE_PASSED"))
	assert.Equal(t, 1, FirstGateVerdict(h))
	assert.Equal(t, -1, FirstGateVerdict(h[:1]))
}

// TestTickCountsNothingToDo: a tick that changes nothing counts all zeros.
func TestTickCountsNothingToDo(t *testing.T) {
	p := testPolicy(t)
	cfg := Config{Now: now, Policy: &p, Launcher: &fakeLauncher{}}
	assert.Equal(t, Counts{}, Tick(context.Background(), cfg, ledgerOf()).Counts)

	// Items that only wait: an observe item, and a run still going.
	obs := item("api-change|iam|Role|spec:X|manual-review", "manual-review", ledger.Detected)
	obs.AutonomyLevel = "observe"
	live := running(item("sdk-bump|rds|*|service/rds/v1.2.0", "SDK-bump", ledger.Queued), "2026-11-12T08:30:00Z", 1, 3)
	l := &fakeLauncher{statuses: map[string]Status{live.Claim.LaunchID: {Phase: PhaseRunning}}}
	cfg.Launcher = l
	res := Tick(context.Background(), cfg, ledgerOf(obs, live))
	assert.Equal(t, Counts{}, res.Counts)
	assert.Equal(t, 0, res.Reaped)
}

// TestTickCountsRuns: one tick reconciles one run of each kind.
func TestTickCountsRuns(t *testing.T) {
	p := testPolicy(t)
	run := func(svc string) *ledger.WorkItem {
		return running(item("sdk-bump|"+svc+"|*|service/"+svc+"/v1.2.0", "SDK-bump", ledger.Queued), "2026-11-12T08:30:00Z", 1, 3)
	}
	opened, noChange, human, failed, live, wont := run("s3"), run("iam"), run("rds"), run("ec2"), run("sns"), run("sqs")
	reaped := running(item("sdk-bump|eks|*|service/eks/v1.2.0", "SDK-bump", ledger.Queued), "2026-11-12T03:29:00Z", 1, 3)
	exhausted := running(item("sdk-bump|ecr|*|service/ecr/v1.2.0", "SDK-bump", ledger.Queued), "2026-11-12T08:30:00Z", 3, 3)
	l := &fakeLauncher{statuses: map[string]Status{
		opened.Claim.LaunchID:    *finished("pr_opened", "", s3PR),
		noChange.Claim.LaunchID:  *finished("completed_no_change", "", ""),
		human.Claim.LaunchID:     *finished("needs_human", "AMBIGUOUS_SPEC", ""),
		failed.Claim.LaunchID:    *finished("failed", "BUILD_FAILED", ""),
		live.Claim.LaunchID:      {Phase: PhaseRunning},
		wont.Claim.LaunchID:      *finished("wont_fix:not_real", "", ""),
		exhausted.Claim.LaunchID: *finished("failed", "BUILD_FAILED", ""),
		// reaped: no status (PhaseUnknown), past deadline plus grace.
	}}
	items := ledgerOf(opened, noChange, human, failed, live, wont, reaped, exhausted)
	cfg := Config{Now: now, Policy: &p, Launcher: l}
	res := Tick(context.Background(), cfg, items)
	require.Equal(t, ledger.Gating, opened.State)
	require.Equal(t, ledger.Done, noChange.State)
	require.Equal(t, ledger.WontFix, wont.State)
	require.Equal(t, ledger.Escalated, exhausted.State)
	assert.Equal(t, Counts{RunsCompleted: 2, RunsFailed: 4, ItemsEscalated: 2}, res.Counts)
	assert.Equal(t, 1, res.Reaped)

	// Nothing is counted twice: the next tick reconciles nothing new.
	l.statuses[live.Claim.LaunchID] = Status{Phase: PhaseRunning}
	res = Tick(context.Background(), cfg, items)
	assert.Equal(t, 0, res.Counts.RunsCompleted+res.Counts.RunsFailed+res.Counts.ItemsEscalated)
}

// TestTickCountsAbortInflightAsEscalationsOnly: runs stopped by policy are
// escalations, not reconciled runs.
func TestTickCountsAbortInflightAsEscalationsOnly(t *testing.T) {
	p := testPolicy(t)
	p.Switches.AbortInflight = true
	w := running(item(bumpKey, "SDK-bump", ledger.Queued), "2026-11-12T08:30:00Z", 1, 3)
	l := &fakeLauncher{statuses: map[string]Status{w.Claim.LaunchID: {Phase: PhaseRunning}}}
	res := Tick(context.Background(), Config{Now: now, Policy: &p, Launcher: l}, ledgerOf(w))
	require.Equal(t, "ABORTED_BY_POLICY", last(w).Reason)
	assert.Equal(t, Counts{ItemsEscalated: 1}, res.Counts)
}

// TestTickCountsDryRun: a dry run counts what it did in memory.
func TestTickCountsDryRun(t *testing.T) {
	p := testPolicy(t)
	w := running(item(bumpKey, "SDK-bump", ledger.Queued), "2026-11-12T08:30:00Z", 1, 3)
	l := &fakeLauncher{statuses: map[string]Status{w.Claim.LaunchID: *finished("needs_human", "", "")}}
	res := Tick(context.Background(), Config{Now: now, Policy: &p, Launcher: l, DryRun: true}, ledgerOf(w))
	assert.Equal(t, Counts{RunsFailed: 1, ItemsEscalated: 1}, res.Counts)
	assert.Empty(t, l.launched)
}

// gateCounts runs one gate pass over w and counts it.
func gateCounts(t *testing.T, w *ledger.WorkItem, gh *fakeGH, l gateLauncher) Counts {
	t.Helper()
	items := ledgerOf(w)
	before := historyLens(items)
	assert.Empty(t, gate(context.Background(), items, testEnv(gh, l, true)))
	assertLints(t, items)
	return countTick(before, items)
}

func TestCountsGateFirstVerdicts(t *testing.T) {
	t.Run("first pass", func(t *testing.T) {
		w, gh, l := bumpGating(t, dod.StatusPass, "")
		assert.Equal(t, Counts{GateFirstPass: 1}, gateCounts(t, w, gh, l))
	})
	t.Run("first fail", func(t *testing.T) {
		w, gh, l := bumpGating(t, dod.StatusFail, "", "build:BUILD_FAILED")
		assert.Equal(t, Counts{GateFirstFail: 1}, gateCounts(t, w, gh, l))
		assert.Equal(t, "RETRY GATE_FAILED", last(w).Reason)
	})
	t.Run("first fail, attempts exhausted", func(t *testing.T) {
		w, gh, l := bumpGating(t, dod.StatusFail, "", "build:BUILD_FAILED")
		w.Attempts.Runs = w.Attempts.MaxRuns
		assert.Equal(t, Counts{GateFirstFail: 1, ItemsEscalated: 1}, gateCounts(t, w, gh, l))
		assert.Equal(t, "ATTEMPTS_EXHAUSTED", last(w).Reason)
	})
	t.Run("reclassify is a fail", func(t *testing.T) {
		w, gh, l := bumpGating(t, dod.StatusFail, "hand-written-code", "path-allowlist:PATH_NOT_ALLOWED")
		assert.Equal(t, Counts{GateFirstFail: 1, ItemsEscalated: 1}, gateCounts(t, w, gh, l))
	})
	t.Run("environment failure is not a verdict", func(t *testing.T) {
		w, gh, l := bumpGating(t, dod.StatusFail, "", "regen-clean:REGEN_FAILED")
		assert.Equal(t, Counts{}, gateCounts(t, w, gh, l))
		assert.Contains(t, states(w), ledger.RetryEnv)
	})
	t.Run("second verdict is not first", func(t *testing.T) {
		w, gh, l := bumpGating(t, dod.StatusPass, "")
		// An earlier gate on the same PR failed and the item was rerun.
		earlier := []ledger.Transition{to(ledger.Gating, "PR_OPENED"), to(ledger.Queued, "RETRY GATE_FAILED")}
		w.History = append(w.History[:1], append(earlier, w.History[1:]...)...)
		assert.Equal(t, Counts{}, gateCounts(t, w, gh, l))
		assert.Equal(t, ledger.HumanReview, w.State)
	})
	t.Run("tampered verdict is not a verdict", func(t *testing.T) {
		w, gh, l, _, _ := dodGating(t)
		c := gateCounts(t, w, gh, l)
		assert.Equal(t, "GATE_VERDICT_UNSIGNED", last(w).Reason)
		assert.Equal(t, Counts{ItemsEscalated: 1}, c)
	})
	t.Run("verdict for an old head is not a verdict", func(t *testing.T) {
		w, gh, l, _, id := dodGating(t)
		l.sigs[id] = signFor(t, l.verdicts[id])
		p := gh.prs[7]
		p.HeadSHA = head2
		gh.prs[7] = p
		assert.Equal(t, Counts{}, gateCounts(t, w, gh, l))
		assert.Contains(t, last(w).Reason, "GATE_HEAD_MOVED")
	})
}

// TestTickCountsLifecycle: the Plan 9 lifecycle tick (run reconciled, then
// gated and passed in the same tick) counts one completed run and one first
// pass.
func TestTickCountsLifecycle(t *testing.T) {
	p := testPolicy(t)
	bump := running(item(bumpKey, "SDK-bump", ledger.Queued), "2026-11-12T08:30:00Z", 1, 3)
	v := verdictFor(t, bump, dod.StatusPass, "")
	gh := newFakeGH()
	pr := openPR("ack-bot")
	pr.Body = "card\n\n" + dod.Trailer(v)
	gh.prs[7] = pr
	run := bump.Claim.LaunchID
	l := gateLauncher{fakeLauncher: &fakeLauncher{statuses: map[string]Status{
		run: *finishedAs(JobSucceeded, "pr_opened", "", s3PR),
	}}, verdicts: map[string][]byte{run: v}}
	cfg := Config{Now: now, Policy: &p, Launcher: l, GitHub: gh, DryRun: true,
		PRAuthors: map[string]string{"sdk-bump": "ack-bot"}, BotLogins: []string{"ack-bot"}}
	res := Tick(context.Background(), cfg, ledgerOf(bump))
	require.Equal(t, ledger.HumanReview, bump.State)
	assert.Equal(t, Counts{RunsCompleted: 1, GateFirstPass: 1}, res.Counts)
}
