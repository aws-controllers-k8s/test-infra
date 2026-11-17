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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

func marshalAll(t *testing.T, items map[string]*ledger.WorkItem) map[string]string {
	out := map[string]string{}
	for k, w := range items {
		b, err := ledger.Marshal(w)
		require.NoError(t, err)
		out[k] = string(b)
	}
	return out
}

func TestTickShadowChangesNoItemForLaunch(t *testing.T) {
	bump := item(bumpKey, "SDK-bump", ledger.Detected)
	field := item(fieldKey, "config-only", ledger.Detected)
	items := ledgerOf(bump, field)
	p := testPolicy(t)
	l := &ShadowLauncher{}
	res := Tick(context.Background(), Config{Now: now, Policy: &p, Launcher: l}, items)

	assert.Empty(t, res.Problems)
	assert.Equal(t, map[string]int{HoldAwaitingTriage: 1}, res.Held)
	require.Len(t, res.Launched, 1)
	assert.Equal(t, Launch{Key: bumpKey, KeyHash: bump.KeyHash, Workflow: "sdk-bump", LaunchID: launchID(bump, 1), Shadow: true}, res.Launched[0])
	assert.Equal(t, ledger.Queued, bump.State, "shadow launch leaves it QUEUED")
	assert.Nil(t, bump.Claim)
	assert.False(t, bump.EverDispatched())
	require.Len(t, l.Requests, 1)
	assert.Equal(t, p.Budgets.JobTimeout(), l.Requests[0].Timeout)

	before := marshalAll(t, items)
	again := Tick(context.Background(), Config{Now: now.Add(30 * time.Minute), Policy: &p, Launcher: &ShadowLauncher{}}, items)
	assert.Equal(t, before, marshalAll(t, items), "a second shadow tick changes nothing")
	assert.Len(t, again.Launched, 1)
}

func TestTickRealLaunchClaims(t *testing.T) {
	bump := item(bumpKey, "SDK-bump", ledger.Queued)
	items := ledgerOf(bump)
	p := testPolicy(t)
	f := &fakeLauncher{}
	res := Tick(context.Background(), Config{Now: now, Policy: &p, Launcher: f}, items)
	require.Len(t, res.Launched, 1)
	assert.False(t, res.Launched[0].Shadow)
	assert.Equal(t, ledger.Running, bump.State)
	assert.Equal(t, &ledger.Attempts{Runs: 1, MaxRuns: 3}, bump.Attempts)
	assert.Equal(t, &ledger.Claim{LaunchID: launchID(bump, 1), Workflow: "sdk-bump",
		TS: "2026-11-12T09:00:00Z", Deadline: "2026-11-12T14:00:00Z"}, bump.Claim)
	assert.Equal(t, []string{launchID(bump, 1)}, bump.Links.ProwJobs)

	root := t.TempDir()
	require.NoError(t, ledger.Save(root, items))
	assert.Empty(t, ledger.Lint(root))

	// The run fails; the next tick requeues it and launches run 2.
	f.statuses = map[string]Status{launchID(bump, 1): *finished("failed", "BUILD_FAILED", "")}
	res = Tick(context.Background(), Config{Now: now.Add(30 * time.Minute), Policy: &p, Launcher: f}, items)
	require.Len(t, res.Launched, 1)
	assert.Equal(t, launchID(bump, 2), res.Launched[0].LaunchID)
	assert.Equal(t, 2, bump.Attempts.Runs)
}

func TestTickLaunchErrorLeavesQueued(t *testing.T) {
	bump := item(bumpKey, "SDK-bump", ledger.Queued)
	p := testPolicy(t)
	res := Tick(context.Background(), Config{Now: now, Policy: &p, Launcher: &fakeLauncher{launchErr: errors.New("denied")}}, ledgerOf(bump))
	assert.Empty(t, res.Launched)
	require.Len(t, res.Problems, 1)
	assert.Contains(t, res.Problems[0], "denied")
	assert.Equal(t, ledger.Queued, bump.State)
}

func TestTickWithoutPolicyRoutesOnly(t *testing.T) {
	bump := item(bumpKey, "SDK-bump", ledger.Detected)
	res := Tick(context.Background(), Config{Now: now, PolicyErr: errors.New("no policy/autopilot.yaml")}, ledgerOf(bump))
	assert.Equal(t, ledger.Queued, bump.State)
	assert.Empty(t, res.Launched)
	require.Len(t, res.Problems, 1)
	assert.Contains(t, res.Problems[0], "no policy/autopilot.yaml")
}

func TestTickReportsWaiting(t *testing.T) {
	bump := item("sdk-bump|sqs|*|v1", "SDK-bump", ledger.Queued)
	p := testPolicy(t)
	res := Tick(context.Background(), Config{Now: now, Policy: &p, Launcher: &ShadowLauncher{}}, ledgerOf(bump))
	assert.Equal(t, []Wait{{Key: bump.Key, KeyHash: bump.KeyHash, Cap: CapServiceNotEnabled}}, res.Waiting)
}

func TestTickNilPolicyLeavesRunningItemUntouched(t *testing.T) {
	w := running(item(bumpKey, "SDK-bump", ledger.Queued), "2026-11-12T08:30:00Z", 1, 3)
	claim, hist := *w.Claim, len(w.History)
	res := Tick(context.Background(), Config{Now: now}, ledgerOf(w))
	assert.Equal(t, ledger.Running, w.State)
	assert.Equal(t, claim, *w.Claim)
	assert.Len(t, w.History, hist)
	require.Len(t, res.Problems, 1)
	assert.Contains(t, res.Problems[0], "policy or launcher unavailable")
	assert.NotContains(t, res.Problems[0], "<nil>")
}

func TestTickNilLauncherRoutesOnly(t *testing.T) {
	bump := item(bumpKey, "SDK-bump", ledger.Detected)
	p := testPolicy(t)
	res := Tick(context.Background(), Config{Now: now, Policy: &p}, ledgerOf(bump))
	assert.Equal(t, ledger.Queued, bump.State)
	assert.Empty(t, res.Launched)
	require.Len(t, res.Problems, 1)
	assert.Contains(t, res.Problems[0], "policy or launcher unavailable")
}

func TestTickLaunchesInScheduleOrder(t *testing.T) {
	iam := item("sdk-bump|iam|*|v1", "SDK-bump", ledger.Queued)
	iam.PriorityBand = 3
	s3 := item("sdk-bump|s3|*|v1", "SDK-bump", ledger.Queued)
	s3.PriorityBand = 1
	sqs := item("sdk-bump|sqs|*|v1", "SDK-bump", ledger.Queued)
	sns := item("sdk-bump|sns|*|v1", "SDK-bump", ledger.Queued)
	p := testPolicy(t)
	res := Tick(context.Background(), Config{Now: now, Policy: &p, Launcher: &ShadowLauncher{}}, ledgerOf(iam, s3, sqs, sns))
	require.Len(t, res.Launched, 2)
	assert.Equal(t, []string{s3.Key, iam.Key}, []string{res.Launched[0].Key, res.Launched[1].Key})
	assert.Equal(t, []Wait{
		{Key: sns.Key, KeyHash: sns.KeyHash, Cap: CapServiceNotEnabled},
		{Key: sqs.Key, KeyHash: sqs.KeyHash, Cap: CapServiceNotEnabled},
	}, res.Waiting)
}

func TestTickReconcileStatusErrorSurfaces(t *testing.T) {
	w := running(item(bumpKey, "SDK-bump", ledger.Queued), "2026-11-12T08:30:00Z", 1, 3)
	p := testPolicy(t)
	f := &fakeLauncher{statusErr: map[string]error{w.Claim.LaunchID: errors.New("api down")}}
	res := Tick(context.Background(), Config{Now: now, Policy: &p, Launcher: f}, ledgerOf(w))
	require.Len(t, res.Problems, 1)
	assert.Contains(t, res.Problems[0], "api down")
	assert.Equal(t, ledger.Running, w.State)
}

func TestTickDryRunNeverLaunches(t *testing.T) {
	bump := item(bumpKey, "SDK-bump", ledger.Queued)
	p := testPolicy(t)
	f := &fakeLauncher{}
	res := Tick(context.Background(), Config{Now: now, Policy: &p, Launcher: f, DryRun: true}, ledgerOf(bump))
	assert.Empty(t, f.launched)
	require.Len(t, res.Launched, 1)
	assert.True(t, res.Launched[0].Shadow)
	assert.Equal(t, ledger.Queued, bump.State)
}

func TestTickDryRunNeverLaunchesOrAborts(t *testing.T) {
	overdue := running(item(bumpKey, "SDK-bump", ledger.Queued), "2026-11-12T02:00:00Z", 1, 3)
	q := item("sdk-bump|iam|*|v1", "SDK-bump", ledger.Queued)
	f := &fakeLauncher{statuses: map[string]Status{overdue.Claim.LaunchID: {Phase: PhaseRunning}}}
	p := testPolicy(t)
	res := Tick(context.Background(), Config{Now: now.Add(2 * time.Hour), Policy: &p, Launcher: f, DryRun: true}, ledgerOf(overdue, q))
	assert.Empty(t, f.launched)
	assert.Empty(t, f.aborted)
	require.Len(t, res.Launched, 1)
	assert.True(t, res.Launched[0].Shadow)
	assert.Equal(t, ledger.Escalated, overdue.State, "the dry run still shows what reconcile would do")
}

func TestTickAbortInflight(t *testing.T) {
	r := running(item(bumpKey, "SDK-bump", ledger.Queued), "2026-11-12T08:30:00Z", 1, 3)
	q := item("sdk-bump|iam|*|v1", "SDK-bump", ledger.Queued)
	p := testPolicy(t)
	p.Switches.AbortInflight = true
	id := r.Claim.LaunchID
	f := &fakeLauncher{statuses: map[string]Status{id: {Phase: PhaseRunning}}}
	res := Tick(context.Background(), Config{Now: now, Policy: &p, Launcher: f}, ledgerOf(r, q))
	assert.Equal(t, []string{id}, f.aborted)
	assert.Equal(t, ledger.Escalated, r.State)
	assert.Equal(t, &ledger.Verdict{Outcome: "needs_human", Reason: "ABORTED_BY_POLICY"}, r.Verdict)
	assert.Empty(t, res.Launched)
	assert.Equal(t, []Wait{{Key: q.Key, KeyHash: q.KeyHash, Cap: CapAborting}}, res.Waiting)
	assertLints(t, ledgerOf(r, q))

	// The shadow launcher aborts nothing and changes no item.
	r2 := running(item(bumpKey, "SDK-bump", ledger.Queued), "2026-11-12T08:30:00Z", 1, 3)
	res = Tick(context.Background(), Config{Now: now, Policy: &p, Launcher: &ShadowLauncher{}}, ledgerOf(r2))
	assert.Equal(t, ledger.Running, r2.State)
	assert.Contains(t, res.Problems, "abort_inflight: 1 run(s) in flight; this launcher aborts nothing")
}

func TestTickOrphansCountAndAdoptionIsNotAnOrphan(t *testing.T) {
	a := item("api-change|s3|Bucket|spec:A|needs-config", "config-only", ledger.Queued)
	b := item("api-change|iam|Role|spec:A|needs-config", "config-only", ledger.Queued)
	p := testPolicy(t)
	f := inventoryLauncher{&fakeLauncher{active: []ActiveJob{
		// a's next launch: a lost ledger push. Launch adopts it.
		{LaunchID: launchID(a, 1), Workflow: "add-field", Service: "s3"},
		{LaunchID: "autopilot-1111111111111111-g1-r1", Workflow: "add-field", Service: "rds"},
		{LaunchID: "autopilot-2222222222222222-g1-f0123abcd", Workflow: "sdk-bump", Service: "eks", Refresh: true},
	}}}
	res := Tick(context.Background(), Config{Now: now, Policy: &p, Launcher: f}, ledgerOf(a, b))
	assert.Contains(t, res.Problems, "orphan run autopilot-1111111111111111-g1-r1 (add-field) is in flight and no item claims it; it counts against the caps")
	require.Len(t, res.Launched, 1, "the orphan takes one of the two LLM slots")
	assert.Equal(t, launchID(a, 1), res.Launched[0].LaunchID)
	assert.Equal(t, []Wait{{Key: b.Key, KeyHash: b.KeyHash, Cap: CapLLM}}, res.Waiting)

	f.activeErr = errors.New("forbidden")
	res = Tick(context.Background(), Config{Now: now, Policy: &p, Launcher: f}, ledgerOf(item("sdk-bump|eks|*|v1", "SDK-bump", ledger.Queued)))
	assert.Empty(t, res.Launched)
	assert.Contains(t, res.Problems, "inventory: forbidden; nothing launched")
}

func TestTickRefreshesBumpWhenDependentsChange(t *testing.T) {
	bump := item(bumpKey, "SDK-bump", ledger.HumanReview)
	bump.Links.PR = "https://github.com/aws-controllers-k8s/s3-controller/pull/9"
	dep := item(fieldKey, "config-only", ledger.Blocked)
	dep.DetectorWork, dep.DependsOn = "server-default", []string{bumpKey}
	items := ledgerOf(bump, dep)
	p := testPolicy(t)
	f := &fakeLauncher{}

	res := Tick(context.Background(), Config{Now: now, Policy: &p, Launcher: f}, items)
	require.Len(t, f.launched, 1)
	first := f.launched[0]
	assert.Equal(t, "sdk-bump", first.Workflow)
	assert.Equal(t, "refresh", first.Args["mode"])
	assert.Equal(t, bump.KeyHash, first.Args["work-item"])
	assert.Regexp(t, `^autopilot-[0-9a-f]{16}-g1-f[0-9a-f]{8}$`, first.LaunchID)
	assert.Contains(t, bump.Links.ProwJobs, first.LaunchID)
	assert.Equal(t, ledger.HumanReview, bump.State, "a refresh takes no claim")
	assert.Nil(t, bump.Attempts)
	require.Len(t, res.Launched, 1)
	assertLints(t, items)

	// Same states: no second refresh.
	Tick(context.Background(), Config{Now: now.Add(30 * time.Minute), Policy: &p, Launcher: f}, items)
	assert.Len(t, f.launched, 1)

	// The dependent is decided (wontfix): one new refresh.
	dep.State = ledger.WontFix
	dep.History = append(dep.History, ledger.Transition{TS: "2026-11-12T10:00:00Z", To: ledger.WontFix, By: "issuefinder"})
	Tick(context.Background(), Config{Now: now.Add(time.Hour), Policy: &p, Launcher: f}, items)
	require.Len(t, f.launched, 2)
	assert.NotEqual(t, first.LaunchID, f.launched[1].LaunchID)
}

func TestNextLaunch(t *testing.T) {
	w := item(fieldKey, "config-only", ledger.Queued)
	w.Resource, w.FieldPath, w.DetectorWork = "Bucket", "Mode", "server-default"
	req, ok := NextLaunch(w, testPolicy(t).Budgets)
	require.True(t, ok)
	assert.Equal(t, launchID(w, 1), req.LaunchID)
	assert.Equal(t, "add-field", req.Workflow)
	assert.Equal(t, "server-default", req.Args["detector-work"])
	assert.Equal(t, 5*time.Hour, req.Timeout)
	_, ok = NextLaunch(item(fieldKey, "hand-written-code", ledger.Queued), testPolicy(t).Budgets)
	assert.False(t, ok)
}

// TestTickNeverRelaunchesAGoneJob: once a launch id is in the ledger (the
// claim, links.prowjobs), no tick launches it again, even when the launcher no
// longer knows the job (Prow's sinker deleted it: PhaseUnknown). The run is
// reaped after deadline plus grace and the next run gets a new id.
func TestTickNeverRelaunchesAGoneJob(t *testing.T) {
	bump := item(bumpKey, "SDK-bump", ledger.Queued)
	items := ledgerOf(bump)
	p := testPolicy(t)
	f := &fakeLauncher{} // every Status is PhaseUnknown: the job is gone
	at := now
	for i := 0; i < 40; i++ { // 20 hours of ticks
		Tick(context.Background(), Config{Now: at, Policy: &p, Launcher: f}, items)
		at = at.Add(30 * time.Minute)
	}
	seen := map[string]bool{}
	for _, r := range f.launched {
		assert.False(t, seen[r.LaunchID], "launch id %s requested twice", r.LaunchID)
		seen[r.LaunchID] = true
	}
	// Deadline 5h + grace 30m per run: r1 at 09:00, r2 at 14:30, r3 at 20:00;
	// then max runs (3) are spent and the item is escalated.
	require.Len(t, f.launched, 3)
	for i, r := range f.launched {
		assert.Equal(t, launchID(bump, i+1), r.LaunchID)
	}
	assert.Equal(t, []string{launchID(bump, 1), launchID(bump, 2), launchID(bump, 3)}, bump.Links.ProwJobs)
	assert.NotEqual(t, ledger.Running, bump.State)
	assertLints(t, items)

	// A refresh id is consumed the same way: recorded in links.prowjobs, never
	// relaunched while the dependents' states are the same, job or no job.
	open := item("sdk-bump|s3|*|v1.2.0", "SDK-bump", ledger.HumanReview)
	open.Links.PR = "https://github.com/aws-controllers-k8s/s3-controller/pull/9"
	dep := item(fieldKey, "config-only", ledger.Blocked)
	dep.DetectorWork, dep.DependsOn = "server-default", []string{open.Key}
	items = ledgerOf(open, dep)
	f = &fakeLauncher{}
	for i := 0; i < 10; i++ {
		Tick(context.Background(), Config{Now: now.Add(time.Duration(i) * 30 * time.Minute), Policy: &p, Launcher: f}, items)
	}
	require.Len(t, f.launched, 1)
	assert.Equal(t, "refresh", f.launched[0].Args["mode"])
}
