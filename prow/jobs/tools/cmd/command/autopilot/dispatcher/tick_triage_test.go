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

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/issuefinder"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

func TestTickTriagesThenAMaintainerDecides(t *testing.T) {
	ctx := context.Background()
	bump := item(bumpKey, "SDK-bump", ledger.Detected)
	field := needsConfig(fieldKey, "config-only", "create-only")
	items := ledgerOf(bump, field)
	p := withTriage(t)
	f := &fakeLauncher{statuses: map[string]Status{}}
	id := ledger.TriageLaunchID(field, 1)

	res := Tick(ctx, Config{Now: now, Policy: &p, Launcher: f}, items)
	assert.Empty(t, res.Problems)
	assert.False(t, res.TriageOff)
	assert.Contains(t, res.Launched, Launch{Key: fieldKey, KeyHash: field.KeyHash, Workflow: triageWorkflow, LaunchID: id})
	var req LaunchRequest
	for _, r := range f.launched {
		if r.Workflow == triageWorkflow {
			req = r
		}
	}
	assert.Equal(t, 30*time.Minute, req.Timeout)
	assert.Equal(t, field.KeyHash, req.Args["work-item"])
	assert.Equal(t, ledger.Detected, field.State)
	require.NotNil(t, field.Triage.Claim)
	assertLints(t, items)

	f.statuses[id] = triageDone(verdictJSON(field.KeyHash, "field-config", "config-only", "op:CreateBucket"))
	res = Tick(ctx, Config{Now: now.Add(30 * time.Minute), Policy: &p, Launcher: f}, items)
	assert.Equal(t, ledger.Triaged, field.State)
	assert.Equal(t, 1, res.Held[HoldAwaitingTriageReview])
	assertLints(t, items)

	// No reply: nothing moves, tick after tick. Phase 1 never auto-accepts.
	for i := 2; i <= 4; i++ {
		Tick(ctx, Config{Now: now.Add(time.Duration(i) * 30 * time.Minute), Policy: &p, Launcher: f}, items)
		assert.Equal(t, ledger.Triaged, field.State)
		assert.True(t, field.Triage.Pending())
		assert.Equal(t, "config-only", field.ChangeClass)
	}

	accept := issuefinder.TriageCommand{Issue: 27, IssueURL: viewIssue, CommentID: 7, Author: "maintainer", Verb: "accept", LaunchID: id}
	res = Tick(ctx, Config{Now: now.Add(3 * time.Hour), Policy: &p, Launcher: f, TriageCommands: []issuefinder.TriageCommand{accept}}, items)
	assert.Empty(t, res.Skipped)
	assert.Equal(t, ledger.Blocked, field.State, "accepted: it waits on its SDK bump")
	assert.Equal(t, []TriageStat{{Mode: "api", Work: "create-only", Decision: "field-config", Proposed: 1, Accepted: 1}}, res.TriageStats)
	assertLints(t, items)
}

func TestTickTriageShadowAndDryRunChangeNothing(t *testing.T) {
	ctx := context.Background()
	field := needsConfig(fieldKey, "config-only", "create-only")
	items := ledgerOf(field)
	p := withTriage(t)
	before := marshalAll(t, items)

	res := Tick(ctx, Config{Now: now, Policy: &p, Launcher: &ShadowLauncher{}}, items)
	require.Len(t, res.Launched, 1)
	assert.Equal(t, Launch{Key: fieldKey, KeyHash: field.KeyHash, Workflow: triageWorkflow, LaunchID: ledger.TriageLaunchID(field, 1), Shadow: true}, res.Launched[0])
	assert.Equal(t, before, marshalAll(t, items))

	f := &fakeLauncher{}
	res = Tick(ctx, Config{Now: now, Policy: &p, Launcher: f, DryRun: true}, items)
	assert.Empty(t, f.launched)
	assert.True(t, res.Launched[0].Shadow)
	assert.Equal(t, before, marshalAll(t, items))
}

func TestTickTriageOff(t *testing.T) {
	field := needsConfig(fieldKey, "config-only", "create-only")
	p := testPolicy(t)
	res := Tick(context.Background(), Config{Now: now, Policy: &p, Launcher: &fakeLauncher{}}, ledgerOf(field))
	assert.True(t, res.TriageOff)
	assert.Empty(t, res.Launched)
	assert.Equal(t, map[string]int{HoldAwaitingTriage: 1}, res.Held)
}

// Plan 7's kill switch stops triage runs too, and launches no new one.
func TestTickAbortInflightStopsTriageRuns(t *testing.T) {
	ctx := context.Background()
	running := triaging(needsConfig("api-change|s3|Bucket|spec:Eta|needs-config", "config-only", "create-only"))
	id := running.Triage.Claim.LaunchID
	waiting := needsConfig(fieldKey, "config-only", "create-only")
	p := withTriage(t)
	p.Switches.AbortInflight = true
	f := &fakeLauncher{statuses: map[string]Status{id: {Phase: PhaseRunning}}}
	res := Tick(ctx, Config{Now: now, Policy: &p, Launcher: f}, ledgerOf(running, waiting))
	assert.Equal(t, []string{id}, f.aborted)
	assert.Nil(t, running.Triage.Claim)
	assert.Equal(t, ledger.Detected, running.State)
	assert.Equal(t, "TRIAGE_FAILED ABORTED_BY_POLICY", last(running).Reason)
	assert.Empty(t, running.Triage.Failed, "one of two runs used: triaged again once the switch is off")
	assert.Empty(t, res.Launched)
	assert.Contains(t, res.Waiting, Wait{Key: waiting.Key, KeyHash: waiting.KeyHash, Cap: CapAborting})
	assertLints(t, ledgerOf(running, waiting))

	// The shadow launcher stops nothing and changes no item.
	again := triaging(needsConfig("api-change|s3|Bucket|spec:Eta|needs-config", "config-only", "create-only"))
	res = Tick(ctx, Config{Now: now, Policy: &p, Launcher: &ShadowLauncher{}}, ledgerOf(again))
	assert.NotNil(t, again.Triage.Claim)
	assert.Contains(t, res.Problems, "abort_inflight: 1 triage run(s) in flight; this launcher aborts nothing")
}

// Plan 7's inventory: a run a triage claim holds is not an orphan, a triage
// candidate's lost-push run is adopted, and a real triage orphan fills a
// triage slot without making its service busy for workflow runs.
func TestTickTriageRunsAreNotOrphans(t *testing.T) {
	inFlight := triaging(needsConfig("api-change|s3|Bucket|spec:Eta|needs-config", "config-only", "create-only"))
	claimed := inFlight.Triage.Claim.LaunchID
	lost := needsConfig(fieldKey, "config-only", "create-only")
	lostID := nextTriageLaunchID(lost)
	bump := item(bumpKey, "SDK-bump", ledger.Queued)
	foreign := "autopilot-1111111111111111-g1-t1"
	p := withTriage(t)
	f := inventoryLauncher{&fakeLauncher{
		statuses: map[string]Status{claimed: {Phase: PhaseRunning}},
		active: []ActiveJob{
			{LaunchID: claimed, Workflow: triageWorkflow, Service: "s3"},
			{LaunchID: lostID, Workflow: triageWorkflow, Service: "s3"},
			{LaunchID: foreign, Workflow: triageWorkflow, Service: "s3"},
		},
	}}
	res := Tick(context.Background(), Config{Now: now, Policy: &p, Launcher: f}, ledgerOf(inFlight, lost, bump))
	assert.Contains(t, res.Problems, "orphan run "+foreign+" (triage) is in flight and no item claims it; it counts against the caps")
	for _, pr := range res.Problems {
		assert.NotContains(t, pr, claimed)
		assert.NotContains(t, pr, lostID)
	}
	ids := map[string]bool{}
	for _, l := range res.Launched {
		ids[l.LaunchID] = true
	}
	assert.True(t, ids[lostID], "the lost push's run is adopted whatever the triage cap")
	assert.True(t, ids[launchID(bump, 1)], "a triage orphan does not make s3 busy for workflow runs")
	require.NotNil(t, lost.Triage.Claim)
}

func TestTriageStats(t *testing.T) {
	a := proposed(t, needsConfig("api-change|s3|Bucket|spec:A|needs-config", "config-only", "create-only"), ledger.DecisionFieldConfig)
	b := proposed(t, needsConfig("api-change|s3|Bucket|spec:B|needs-config", "config-only", "not-generated"), ledger.DecisionFieldConfig)
	c := triaging(needsConfig("api-change|s3|Bucket|spec:C|needs-config", "hand-written-code", "from-sibling"))
	f := &fakeLauncher{statuses: map[string]Status{c.Triage.Claim.LaunchID: triageDone([]byte(`{}`))}}
	items := ledgerOf(a, b, c)
	reconcileTriage(context.Background(), items, f, withTriage(t), now)
	applyTriageCommands(items, cmds(
		reply(a, 1, "accept", ""),
		reply(b, 2, "reject", ""),
		reply(b, 3, "reject", ledger.DecisionFieldHook),
	), replyTS)

	assert.Equal(t, []TriageStat{
		{Mode: "api", Work: "create-only", Decision: "field-config", Proposed: 1, Accepted: 1},
		{Mode: "api", Work: "from-sibling", Decision: "undecided", Proposed: 1, Invalid: 1},
		{Mode: "api", Work: "not-generated", Decision: "field-config", Proposed: 1, Rejected: 1, Overridden: 1},
	}, TriageStats(items), "the last reply to a proposal counts once")
}

// Under abort_inflight nothing is adopted (scheduling launches nothing), so a
// lost push's run, triage or workflow lane, is aborted like an orphan instead
// of running on.
func TestTickAbortInflightAbortsLostPushRuns(t *testing.T) {
	lost := needsConfig(fieldKey, "config-only", "create-only")
	lostID := nextTriageLaunchID(lost)
	bump := item(bumpKey, "SDK-bump", ledger.Queued)
	bumpID := nextLaunchID(bump)
	p := withTriage(t)
	p.Switches.AbortInflight = true
	f := inventoryLauncher{&fakeLauncher{active: []ActiveJob{
		{LaunchID: lostID, Workflow: triageWorkflow, Service: "s3"},
		{LaunchID: bumpID, Workflow: "sdk-bump", Service: "s3"},
	}}}
	items := ledgerOf(lost, bump)
	res := Tick(context.Background(), Config{Now: now, Policy: &p, Launcher: f}, items)
	assert.ElementsMatch(t, []string{lostID, bumpID}, f.aborted)
	assert.Empty(t, res.Launched)
	assert.Nil(t, lost.Triage)
	assert.Nil(t, bump.Claim)
	assert.Contains(t, res.Waiting, Wait{Key: lost.Key, KeyHash: lost.KeyHash, Cap: CapAborting})
	assert.Contains(t, res.Waiting, Wait{Key: bump.Key, KeyHash: bump.KeyHash, Cap: CapAborting})
	assertLints(t, items)
}
