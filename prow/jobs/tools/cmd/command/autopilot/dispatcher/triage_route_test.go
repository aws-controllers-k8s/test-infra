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
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

func TestRouteTriageHolds(t *testing.T) {
	key := func(f string) string { return "api-change|s3|Bucket|spec:" + f + "|needs-config" }
	awaiting := needsConfig(key("A"), "config-only", "create-only")
	inFlight := triaging(needsConfig(key("B"), "config-only", "create-only"))
	failed := needsConfig(key("C"), "config-only", "create-only")
	failed.Triage = &ledger.Triage{Mode: ledger.TriageModeAPI, Input: ledger.TriageInput(failed), Runs: 2, Launches: 2, Failed: "MODEL_CALL_FAILED"}
	ineligible := needsConfig("api-change|s3|Bucket|class=odd-field:D|needs-config", "config-only", "create-only")
	pending := proposed(t, needsConfig(key("E"), "config-only", "create-only"), ledger.DecisionFieldConfig)
	rejected := proposed(t, needsConfig(key("F"), "config-only", "create-only"), ledger.DecisionFieldConfig)
	applyTriageCommands(ledgerOf(rejected), cmds(reply(rejected, 1, "reject", "")), replyTS)
	edited := needsConfig(key("G"), "config-only", "create-only")
	edited.State = ledger.Queued
	edited.History = append(edited.History, ledger.Transition{TS: t0, To: ledger.Queued, By: "hand-edit"})
	editedPending := proposed(t, needsConfig(key("H"), "config-only", "create-only"), ledger.DecisionFieldConfig)
	editedPending.State = ledger.Queued
	editedPending.History = append(editedPending.History, ledger.Transition{TS: t0, To: ledger.Queued, By: "hand-edit"})
	bareTriaged := needsConfig(key("I"), "config-only", "create-only")
	bareTriaged.State = ledger.Triaged
	bareTriaged.History = append(bareTriaged.History, ledger.Transition{TS: t0, To: ledger.Triaged, By: "hand-edit"})
	items := ledgerOf(item(bumpKey, "SDK-bump", ledger.Queued), awaiting, inFlight, failed, ineligible, pending, rejected,
		edited, editedPending, bareTriaged)

	held := route(items, replyTS)
	assert.Equal(t, map[string]int{
		HoldAwaitingTriage: 3, HoldTriageRunning: 1, HoldTriageFailed: 1, HoldTriageIneligible: 1,
		HoldAwaitingTriageReview: 2, HoldTriageRejected: 1,
	}, held)
	// A needs-config item never runs without a decision. Without a proposal
	// to decide on, it goes back to DETECTED to be triaged again; with a
	// pending one, to TRIAGED to wait for the reply.
	for _, w := range []*ledger.WorkItem{edited, bareTriaged} {
		assert.Equal(t, ledger.Detected, w.State, w.Key)
		assert.Equal(t, "TRIAGE_REQUIRED", last(w).Reason, w.Key)
		assert.True(t, triageCandidate(w), w.Key)
	}
	assert.Equal(t, ledger.Triaged, editedPending.State)
	assert.Equal(t, "TRIAGE_REQUIRED", last(editedPending).Reason)
	assert.True(t, editedPending.Triage.Pending())
	for _, w := range []*ledger.WorkItem{awaiting, inFlight, failed, ineligible, pending, rejected} {
		assert.Contains(t, []ledger.State{ledger.Detected, ledger.Triaged}, w.State, w.Key)
	}
	assertLints(t, items)
}

func TestScheduleReservesReviewSlotsForTriagedItems(t *testing.T) {
	var review []*ledger.WorkItem
	for _, svc := range []string{"iam", "rds", "ec2", "eks"} {
		review = append(review, item("sdk-bump|"+svc+"|*|service/"+svc+"/v1.2.0", "SDK-bump", ledger.HumanReview))
	}
	bump := item(bumpKey, "SDK-bump", ledger.Queued)
	field := needsConfig("api-change|iam|Role|spec:Mode|needs-config", "config-only", "create-only")
	field.State = ledger.Queued
	field.History = append(field.History, ledger.Transition{TS: t0, To: ledger.Queued, By: actor})
	all := func(extra ...*ledger.WorkItem) map[string]*ledger.WorkItem {
		return ledgerOf(slices.Concat(review, extra)...)
	}

	picks, waits := schedule(all(bump, field), withTriage(t), now)
	require.Len(t, picks, 1)
	assert.Equal(t, field.Key, picks[0].item.Key)
	assert.Equal(t, []capped{{bump, CapReviewReserved}}, waits, "4 of 6 slots taken, 2 reserved")

	picks, waits = schedule(all(bump), withTriage(t), now)
	require.Len(t, picks, 1, "no triaged item queued: no reservation")
	assert.Equal(t, bump.Key, picks[0].item.Key)
	assert.Empty(t, waits)

	picks, _ = schedule(all(bump, field), testPolicy(t), now)
	assert.Len(t, picks, 2, "no triage lane: no reservation")
}
