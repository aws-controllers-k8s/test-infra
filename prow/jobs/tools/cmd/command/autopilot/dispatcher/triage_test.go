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
	"encoding/json"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/policy"
)

const viewIssue = "https://github.com/o/ack-private/issues/27"

// needsConfig is a DETECTED needs-config item as issuefinder.Classify builds
// it: key "api-change|s3|<Kind>|<token>:<field>|needs-config".
func needsConfig(key, class, work string) *ledger.WorkItem {
	w := item(key, class, ledger.Detected)
	seg := strings.Split(key, "|")
	_, subject, _ := strings.Cut(seg[3], ":")
	w.GapClass, w.Resource, w.FieldPath, w.DetectorWork, w.PriorityBand = "needs-config", seg[2], subject, work, 4
	w.DependsOn = []string{bumpKey}
	w.Links.ViewIssue = viewIssue
	return w
}

// withTriage is the test policy with the Phase 1 triage lane on
// (policy/testdata/triage.yaml values).
func withTriage(t *testing.T) policy.Policy {
	t.Helper()
	p := testPolicy(t)
	p.Triage = &policy.Triage{Modes: []string{"api"}, MaxConcurrentTriageRuns: 1, MaxTriageLaunchesPerDay: 10,
		MaxTriageRunsPerItem: 2, JobTimeoutMinutes: 30, ReservedReviewSlotsTriaged: 2}
	return p
}

// triaging starts a triage run on w at 08:50 (deadline 09:20).
func triaging(w *ledger.WorkItem) *ledger.WorkItem {
	startTriage(w, LaunchRequest{LaunchID: nextTriageLaunchID(w), Workflow: triageWorkflow, Timeout: 30 * time.Minute},
		mustTime("2026-11-12T08:50:00Z"))
	return w
}

// verdictJSON is a run's triage verdict for key.
func verdictJSON(key, decision, class string, refs ...string) json.RawMessage {
	if refs == nil {
		refs = []string{}
	}
	it := map[string]any{"item_key": key, "decision": decision, "reason": "because", "evidence_refs": refs, "invalid": []string{}}
	if class != "" {
		it["class"] = class
	}
	b, err := json.Marshal(map[string]any{"schema": TriageSchema, "mode": "api", "items": []any{it}})
	if err != nil {
		panic(err)
	}
	return b
}

// triageDone is the status of a finished triage run that reported raw, as
// Plan 7's launcher returns it: the job succeeded (exit 0) and the file is
// the job's own.
func triageDone(raw json.RawMessage) Status {
	return Status{Phase: PhaseFinished, Job: JobSucceeded, Result: &RunResult{Outcome: OutcomeNoChange, Reason: "TRIAGE_VERDICT",
		Workflow: triageWorkflow, Triage: raw, Tokens: 5000}}
}

func TestTriageArgs(t *testing.T) {
	w := needsConfig(fieldKey, "config-only", "create-only")
	args, why := triageArgs(w)
	assert.Empty(t, why)
	assert.Equal(t, map[string]string{
		"service": "s3", "resource": "Bucket", "field": "Mode", "finding-class": "spec-field",
		"detector-work": "create-only", "change-class": "config-only", "sdk-to": "service/s3/v1.2.0",
		"work-item": w.KeyHash,
	}, args)

	status := needsConfig("api-change|s3|Bucket|status:Phase|needs-config", "hand-written-code", "secondary-read")
	args, why = triageArgs(status)
	assert.Empty(t, why)
	assert.Equal(t, "status-field", args["finding-class"])

	for name, edit := range map[string]func(w *ledger.WorkItem){
		"new-resource class": func(w *ledger.WorkItem) { w.ChangeClass = "new-resource" },
		"observe":            func(w *ledger.WorkItem) { w.AutonomyLevel = "observe" },
		"bad resource":       func(w *ledger.WorkItem) { w.Resource = "Bu cket" },
		"bad field":          func(w *ledger.WorkItem) { w.FieldPath = "a..b" },
		"no work":            func(w *ledger.WorkItem) { w.DetectorWork = "" },
		"core SDK target":    func(w *ledger.WorkItem) { w.Evidence.SDKTo = "v1.41.5" },
		// Plan 7's prowlaunch validators would refuse these at launch.
		"hyphenated SDK package": func(w *ledger.WorkItem) { w.Evidence.SDKTo = "service/s-3/v1.2.0" },
		"long resource":          func(w *ledger.WorkItem) { w.Resource = "B" + strings.Repeat("u", 128) },
	} {
		w := needsConfig(fieldKey, "config-only", "create-only")
		edit(w)
		_, why := triageArgs(w)
		assert.NotEmpty(t, why, name)
	}
	odd := needsConfig("api-change|s3|Bucket|class=odd-field:Mode|needs-config", "config-only", "create-only")
	_, why = triageArgs(odd)
	assert.Equal(t, "finding-class", why)
}

// The Dispatcher's arguments are exactly the workflow's required_args (Plan
// 7's launcher refuses anything else).
func TestTriageArgsMatchTheWorkflowManifest(t *testing.T) {
	raw, err := os.ReadFile("../../../../../../agent-workflows/agent-workflows.yaml")
	require.NoError(t, err)
	var m struct {
		Workflows map[string]struct {
			RequiredArgs []string `yaml:"required_args"`
			Timeout      string   `yaml:"timeout"`
		} `yaml:"workflows"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &m))
	wf, ok := m.Workflows[triageWorkflow]
	require.True(t, ok, "agent-workflows.yaml has no triage workflow (Task 7)")
	args, _ := triageArgs(needsConfig(fieldKey, "config-only", "create-only"))
	assert.ElementsMatch(t, wf.RequiredArgs, slices.Collect(maps.Keys(args)))
	assert.Equal(t, "30m", wf.Timeout, "the workflow timeout matches policy/testdata/triage.yaml job_timeout_minutes")
}

func TestScheduleTriage(t *testing.T) {
	a := needsConfig(fieldKey, "config-only", "create-only")
	b := needsConfig("api-change|s3|Bucket|spec:Zeta|needs-config", "hand-written-code", "custom-update")
	b.History[0].TS = "2026-11-12T07:00:00Z"
	bump := item(bumpKey, "SDK-bump", ledger.Detected)
	items := ledgerOf(a, b, bump)

	picks, waits := scheduleTriage(items, withTriage(t), now)
	require.Len(t, picks, 1)
	assert.Equal(t, b.Key, picks[0].item.Key, "oldest first")
	assert.Equal(t, []capped{{a, CapTriageConcurrent}}, waits)

	p := withTriage(t)
	p.Triage.MaxConcurrentTriageRuns = 2
	picks, waits = scheduleTriage(items, p, now)
	assert.Len(t, picks, 2)
	assert.Empty(t, waits)

	for name, tc := range map[string]struct {
		edit func(p *policy.Policy)
		cap  string
	}{
		"paused":         {func(p *policy.Policy) { p.Switches.Paused = true }, CapPaused},
		"abort_inflight": {func(p *policy.Policy) { p.Switches.AbortInflight = true }, CapAborting},
		"repo paused":    {func(p *policy.Policy) { p.Switches.PausedRepos = []string{"aws-controllers-k8s/s3-controller"} }, CapRepoPaused},
		"service off":    {func(p *policy.Policy) { p.Switches.Services = []string{"iam"} }, CapServiceNotEnabled},
		"daily cap":      {func(p *policy.Policy) { p.Triage.MaxTriageLaunchesPerDay = 0 }, CapTriageDaily},
		"concurrency 0":  {func(p *policy.Policy) { p.Triage.MaxConcurrentTriageRuns = 0 }, CapTriageConcurrent},
	} {
		t.Run(name, func(t *testing.T) {
			p := withTriage(t)
			tc.edit(&p)
			picks, waits := scheduleTriage(items, p, now)
			assert.Empty(t, picks)
			require.Len(t, waits, 2)
			assert.Equal(t, tc.cap, waits[0].cap)
		})
	}

	inFlight := triaging(needsConfig("api-change|s3|Bucket|spec:Eta|needs-config", "config-only", "create-only"))
	picks, waits = scheduleTriage(ledgerOf(a, b, inFlight), withTriage(t), now)
	assert.Empty(t, picks, "a run in flight fills the cap")
	assert.Len(t, waits, 2)

	picks, waits = scheduleTriage(items, testPolicy(t), now)
	assert.Empty(t, picks)
	assert.Empty(t, waits, "no triage lane: nothing waits on it")
}

// Plan 7's inventory: triage runs in flight that no item claims fill triage
// slots, and a candidate whose next run is already in flight (its ledger
// push was lost) is adopted whatever the triage caps.
func TestScheduleTriageCountsOrphansAndAdopts(t *testing.T) {
	a := needsConfig(fieldKey, "config-only", "create-only")
	b := needsConfig("api-change|s3|Bucket|spec:Zeta|needs-config", "hand-written-code", "custom-update")
	b.History[0].TS = "2026-11-12T07:00:00Z"
	items := ledgerOf(a, b)

	picks, waits := scheduleTriageWith(items, withTriage(t), now, 1, nil)
	assert.Empty(t, picks, "one orphan fills the one triage slot")
	assert.Len(t, waits, 2)

	adopt := map[string]bool{nextTriageLaunchID(a): true}
	picks, waits = scheduleTriageWith(items, withTriage(t), now, 1, adopt)
	require.Len(t, picks, 1)
	assert.Equal(t, a.Key, picks[0].item.Key, "adopted first, whatever the cap")
	assert.Equal(t, []capped{{b, CapTriageConcurrent}}, waits)

	p := withTriage(t)
	p.Switches.Paused = true
	picks, _ = scheduleTriageWith(items, p, now, 0, adopt)
	assert.Empty(t, picks, "the kill switches still apply")
}

func TestTriageCandidates(t *testing.T) {
	owned := needsConfig(fieldKey, "config-only", "create-only")
	assert.True(t, triageCandidate(owned))

	inFlight := triaging(needsConfig(fieldKey, "config-only", "create-only"))
	failed := needsConfig(fieldKey, "config-only", "create-only")
	failed.Triage = &ledger.Triage{Mode: ledger.TriageModeAPI, Input: ledger.TriageInput(failed), Runs: 2, Launches: 2, Failed: "MODEL_CALL_FAILED"}
	dispatched := needsConfig(fieldKey, "config-only", "create-only")
	dispatched.History = append(dispatched.History, ledger.Transition{TS: t0, To: ledger.Running, By: actor})
	dispatched.History = append(dispatched.History, ledger.Transition{TS: t0, To: ledger.Detected, By: actor})
	queued := needsConfig(fieldKey, "config-only", "create-only")
	queued.State = ledger.Queued
	notTriage := item(fieldKey, "config-only", ledger.Detected) // no gap class: not a finder item
	for name, w := range map[string]*ledger.WorkItem{
		"in flight": inFlight, "failed": failed, "dispatched once": dispatched, "queued": queued, "not a triage item": notTriage,
	} {
		assert.False(t, triageCandidate(w), name)
	}
}

func TestStartTriageClaimsWithoutDispatching(t *testing.T) {
	w := needsConfig(fieldKey, "config-only", "create-only")
	id := nextTriageLaunchID(w)
	assert.Equal(t, "autopilot-"+strings.TrimPrefix(w.KeyHash, "sha256:")+"-g1-t1", id)
	startTriage(w, LaunchRequest{LaunchID: id, Workflow: triageWorkflow, Timeout: 30 * time.Minute}, now)
	assert.Equal(t, ledger.Detected, w.State)
	assert.True(t, w.FinderOwned(), "the issue finder still owns an item being triaged")
	require.NotNil(t, w.Triage.Claim)
	assert.Equal(t, ledger.Claim{LaunchID: id, Workflow: triageWorkflow, TS: "2026-11-12T09:00:00Z", Deadline: "2026-11-12T09:30:00Z"}, *w.Triage.Claim)
	assert.Equal(t, []string{id}, w.Links.ProwJobs)
	assert.Equal(t, "TRIAGE_STARTED "+id, last(w).Reason)
	assert.Nil(t, w.Claim, "the item's own claim is for workflow runs")
	assert.Equal(t, 1, w.Triage.Runs)
	assertLints(t, ledgerOf(w))
	assert.Equal(t, "autopilot-"+strings.TrimPrefix(w.KeyHash, "sha256:")+"-g1-t2", nextTriageLaunchID(w))
}
