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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

const (
	bumpKey  = "sdk-bump|s3|*|service/s3/v1.2.0"
	fieldKey = "api-change|s3|Bucket|spec:Mode|needs-config"
)

func states(w *ledger.WorkItem) []ledger.State {
	var out []ledger.State
	for _, h := range w.History {
		out = append(out, h.To)
	}
	return out
}

func TestRouteQueuesSDKBump(t *testing.T) {
	bump := item(bumpKey, "SDK-bump", ledger.Detected)
	items := ledgerOf(bump)
	held := route(items, "2026-11-12T09:00:00Z")
	assert.Empty(t, held)
	assert.Equal(t, ledger.Queued, bump.State)
	assert.Equal(t, []ledger.State{ledger.Detected, ledger.Triaged, ledger.Queued}, states(bump))
	assert.Equal(t, "NO_TRIAGE_NEEDED", bump.History[1].Reason)
	assert.Equal(t, actor, bump.History[2].By)

	// Steady state: no new history.
	route(items, "2026-11-12T09:30:00Z")
	assert.Len(t, bump.History, 3)
}

func TestRouteHolds(t *testing.T) {
	field := item(fieldKey, "config-only", ledger.Detected)
	res := item("api-change|s3|Table|new-resource", "new-resource", ledger.Detected)
	res.AutonomyLevel = "draft"
	manual := item("api-change|s3|Quote|transient:Quote|manual-review", "unclassified", ledger.Detected)
	manual.AutonomyLevel = "observe"
	plan := item("api-change|s3|Index|new-resource", "new-resource", ledger.PlanPending)
	held := route(ledgerOf(field, res, manual, plan), "2026-11-12T09:00:00Z")
	assert.Equal(t, map[string]int{
		HoldAwaitingTriage: 1, HoldAwaitingPlanWorkflow: 1, HoldObserve: 1, HoldAwaitingPlanApproval: 1,
	}, held)
	for _, w := range []*ledger.WorkItem{field, res, manual, plan} {
		assert.Len(t, w.History, 1, w.Key)
	}
}

func TestRouteBlocksUntilDependencyLands(t *testing.T) {
	bump := item(bumpKey, "SDK-bump", ledger.Queued)
	field := item(fieldKey, "config-only", ledger.Triaged)
	field.DependsOn = []string{bumpKey}
	items := ledgerOf(bump, field)

	route(items, "2026-11-12T09:00:00Z")
	assert.Equal(t, ledger.Blocked, field.State)
	assert.Equal(t, "DEPENDS_ON "+bump.KeyHash+" QUEUED", field.History[1].Reason)

	bump.State = ledger.HumanReview
	route(items, "2026-11-12T09:30:00Z")
	assert.Len(t, field.History, 2, "still blocked: no new entry")

	bump.State = ledger.Merged
	route(items, "2026-11-12T10:00:00Z")
	assert.Equal(t, ledger.Queued, field.State)
	assert.Equal(t, "DEPENDENCIES_MET", field.History[2].Reason)
}

func TestRouteMissingDependency(t *testing.T) {
	field := item(fieldKey, "config-only", ledger.Triaged)
	field.DependsOn = []string{bumpKey}
	route(ledgerOf(field), "2026-11-12T09:00:00Z")
	assert.Equal(t, ledger.Blocked, field.State)
	assert.Equal(t, "DEPENDS_ON "+ledger.KeyHash(bumpKey)+" missing", field.History[1].Reason)
}

func TestRouteEscalatesClassWithoutLane(t *testing.T) {
	hook := item("api-change|s3|Bucket|spec:Policy|needs-config", "hand-written-code", ledger.Triaged)
	route(ledgerOf(hook), "2026-11-12T09:00:00Z")
	assert.Equal(t, ledger.Escalated, hook.State)
	assert.Equal(t, &ledger.Verdict{Outcome: "needs_human", Reason: "NO_WORKFLOW"}, hook.Verdict)
}

func TestRouteLeavesDispatchedAndTerminalItems(t *testing.T) {
	run := running(item(bumpKey, "SDK-bump", ledger.Queued), t0, 1, 3)
	done := item("sdk-bump|iam|*|v9", "SDK-bump", ledger.Done)
	route(ledgerOf(run, done), "2026-11-12T09:00:00Z")
	assert.Equal(t, ledger.Running, run.State)
	assert.Equal(t, ledger.Done, done.State)
	assert.Len(t, done.History, 1)
}
