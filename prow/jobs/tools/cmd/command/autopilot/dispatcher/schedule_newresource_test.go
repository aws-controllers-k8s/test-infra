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
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/policy"
)

func withNewResource(p policy.Policy, enabled bool) policy.Policy {
	p.NewResource = &policy.NewResource{Enabled: enabled, MaxLaunchesPerDay: 1, MaxOpenPRsPerRepo: 1}
	return p
}

func TestNewResourceLaneIsOffByDefault(t *testing.T) {
	q := newResource("Table", ledger.Queued)
	for name, p := range map[string]policy.Policy{
		"no file":  testPolicy(t),
		"disabled": withNewResource(testPolicy(t), false),
	} {
		picks, waits := schedule(ledgerOf(q), p, now)
		assert.Empty(t, picks, name)
		require.Len(t, waits, 1, name)
		assert.Equal(t, CapNewResourceOff, waits[0].cap, name)
	}
}

func TestNewResourceLaunchesAddResource(t *testing.T) {
	q := newResource("Table", ledger.Queued)
	picks, _ := schedule(ledgerOf(q), withNewResource(testPolicy(t), true), now)
	require.Len(t, picks, 1)
	assert.Equal(t, "add-resource", picks[0].lane.Workflow)
	assert.Equal(t, map[string]string{
		"service": "s3", "resource": "Table", "work-item": q.KeyHash, "change-class": "new-resource",
	}, picks[0].lane.args(q))
}

func TestNewResourceCaps(t *testing.T) {
	p := withNewResource(testPolicy(t), true)
	// One a day, org-wide: a second Kind in another repo waits.
	a := newResource("Table", ledger.Queued)
	b := item("api-change|iam|Widget|new-resource", "new-resource", ledger.Queued)
	b.Resource, b.AutonomyLevel, b.PriorityBand = "Widget", "draft", 6
	picks, waits := schedule(ledgerOf(a, b), p, now)
	require.Len(t, picks, 1)
	require.Len(t, waits, 1)
	assert.Equal(t, CapNewResourceDaily, waits[0].cap)

	// One open new-resource PR per repo, even after the day has passed.
	open := newResource("Index", ledger.HumanReview)
	open.History = []ledger.Transition{{TS: "2026-11-01T00:00:00Z", To: ledger.HumanReview, By: actor}}
	_, waits = schedule(ledgerOf(open, a), p, now)
	require.Len(t, waits, 1)
	assert.Equal(t, CapNewResourceOpenPR, waits[0].cap)
}

func TestDraftPRsCountAgainstReviewBackpressure(t *testing.T) {
	p := testPolicy(t)
	var ws []*ledger.WorkItem
	for i := 0; i < p.Budgets.MaxPRsAwaitingReview; i++ {
		w := item("api-change|svc"+string(rune('a'+i))+"|Kind|new-resource", "new-resource", ledger.HumanReview)
		w.AutonomyLevel = "draft"
		ws = append(ws, w)
	}
	q := item(bumpKey, "SDK-bump", ledger.Queued)
	_, waits := schedule(ledgerOf(append(ws, q)...), p, now)
	require.Len(t, waits, 1)
	assert.Equal(t, CapReview, waits[0].cap)
}

// TestGatingHoldsNoLLMOrLaneSlot (Plan 9 review F3): a GATING add-resource
// item runs no LLM, so it takes neither an LLM nor a lane slot, but its repo
// is still busy.
func TestGatingHoldsNoLLMOrLaneSlot(t *testing.T) {
	p := withNewResource(testPolicy(t), true)
	p.Budgets.MaxConcurrentLLMRuns = 1
	g := item("api-change|iam|Widget|new-resource", "new-resource", ledger.Queued)
	g.Resource, g.AutonomyLevel, g.PriorityBand = "Widget", "draft", 6
	gating(running(g, "2026-11-10T08:30:00Z", 1, 3))
	q := newResource("Table", ledger.Queued)
	picks, waits := schedule(ledgerOf(g, q), p, now)
	assert.Empty(t, waits)
	require.Len(t, picks, 1)
	assert.Equal(t, q, picks[0].item)

	same := item("api-change|iam|Gadget|new-resource", "new-resource", ledger.Queued)
	same.Resource, same.AutonomyLevel, same.PriorityBand = "Gadget", "draft", 6
	_, waits = schedule(ledgerOf(g, same), p, now)
	require.Len(t, waits, 1)
	assert.Equal(t, CapRepoBusy, waits[0].cap, "GATING still makes its repo busy")
}

// TestNewResourceOpenPRCapCountsEveryOpenPR (Plan 9 review F10): an item
// whose PR is still open counts against the repo's new-resource PR cap in any
// state (here ESCALATED), but a QUEUED item is not blocked by its own PR.
func TestNewResourceOpenPRCapCountsEveryOpenPR(t *testing.T) {
	p := withNewResource(testPolicy(t), true)
	esc := newResource("Index", ledger.Escalated)
	esc.History = []ledger.Transition{{TS: "2026-11-01T00:00:00Z", To: ledger.Escalated, By: actor}}
	esc.Links.PR = "https://github.com/aws-controllers-k8s/s3-controller/pull/9"
	q := newResource("Table", ledger.Queued)
	_, waits := schedule(ledgerOf(esc, q), p, now)
	require.Len(t, waits, 1)
	assert.Equal(t, CapNewResourceOpenPR, waits[0].cap)

	own := newResource("Table", ledger.Queued)
	own.Links.PR = s3PR
	picks, waits := schedule(ledgerOf(own), p, now)
	assert.Empty(t, waits)
	assert.Len(t, picks, 1, "its own PR does not block it")
}
