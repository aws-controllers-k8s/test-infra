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

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/issuefinder"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

const replyTS = "2026-11-12T09:30:00Z"

// proposed runs a triage of w to a pending proposal of decision.
func proposed(t *testing.T, w *ledger.WorkItem, decision string) *ledger.WorkItem {
	t.Helper()
	triaging(w)
	var refs []string
	if decision == ledger.DecisionResource {
		refs = []string{"op:CreateBucketMode"}
	}
	f := &fakeLauncher{statuses: map[string]Status{
		w.Triage.Claim.LaunchID: triageDone(verdictJSON(w.KeyHash, decision, ledger.DecisionClass[decision], refs...)),
	}}
	reconcileTriage(context.Background(), ledgerOf(w), f, withTriage(t), now)
	require.True(t, w.Triage.Pending())
	return w
}

func reply(w *ledger.WorkItem, id int64, verb, decision string) issuefinder.TriageCommand {
	return issuefinder.TriageCommand{Issue: 27, IssueURL: w.Links.ViewIssue, CommentID: id, Author: "maintainer",
		Verb: verb, LaunchID: w.Triage.Proposal.LaunchID, Decision: decision}
}

func cmds(c ...issuefinder.TriageCommand) []issuefinder.TriageCommand { return c }

func TestAcceptAppliesTheProposalsClass(t *testing.T) {
	w := proposed(t, needsConfig(fieldKey, "config-only", "create-only"), ledger.DecisionFieldHook)
	items := ledgerOf(item(bumpKey, "SDK-bump", ledger.Queued), w)
	assert.Empty(t, applyTriageCommands(items, cmds(reply(w, 1, "accept", "")), replyTS))
	assert.Equal(t, "hand-written-code", w.ChangeClass)
	assert.Equal(t, ledger.Triaged, w.State)
	assert.Equal(t, "TRIAGE_ACCEPTED field-hook by maintainer", last(w).Reason)
	assert.Equal(t, &ledger.TriageReview{LaunchID: w.Triage.Proposal.LaunchID, Outcome: ledger.ReviewAccepted,
		Decision: ledger.DecisionFieldHook, By: "maintainer", CommentID: 1, TS: replyTS}, w.Triage.Review)
	assertLints(t, items)
	held := route(items, replyTS)
	assert.Equal(t, 1, held[HoldAwaitingWorkflow], "no hand-written-code lane in Phase 1")
}

func TestAcceptedFieldConfigWaitsOnTheBump(t *testing.T) {
	bump := item(bumpKey, "SDK-bump", ledger.Queued)
	w := proposed(t, needsConfig(fieldKey, "config-only", "create-only"), ledger.DecisionFieldConfig)
	items := ledgerOf(bump, w)
	applyTriageCommands(items, cmds(reply(w, 1, "accept", "")), replyTS)
	route(items, replyTS)
	assert.Equal(t, ledger.Blocked, w.State)
	bump.State = ledger.Merged
	bump.History = append(bump.History, ledger.Transition{TS: replyTS, To: ledger.Merged, By: "test"})
	route(items, "2026-11-12T10:00:00Z")
	assert.Equal(t, ledger.Queued, w.State)
	assertLints(t, items)
}

func TestAcceptKeepsAClampedClass(t *testing.T) {
	w := proposed(t, needsConfig(fieldKey, "hand-written-code", "custom-update"), ledger.DecisionFieldConfig)
	require.True(t, w.Triage.Proposal.Clamped)
	applyTriageCommands(ledgerOf(w), cmds(reply(w, 1, "accept", "")), replyTS)
	assert.Equal(t, "hand-written-code", w.ChangeClass, "stricter-only: the detector's class stays")
}

func TestRejectWithADecisionIsAMaintainersDecision(t *testing.T) {
	w := proposed(t, needsConfig(fieldKey, "hand-written-code", "custom-update"), ledger.DecisionFieldHook)
	applyTriageCommands(ledgerOf(w), cmds(reply(w, 1, "reject", ledger.DecisionFieldConfig)), replyTS)
	assert.Equal(t, "config-only", w.ChangeClass, "a person may loosen the detector's class")
	assert.Equal(t, "TRIAGE_REJECTED field-config by maintainer", last(w).Reason)
	e := w.Triage.Log[len(w.Triage.Log)-1]
	assert.Equal(t, ledger.TriageEvent{TS: replyTS, Event: ledger.ReviewRejected, LaunchID: w.Triage.Proposal.LaunchID,
		Decision: ledger.DecisionFieldHook, Override: ledger.DecisionFieldConfig, By: "maintainer"}, e)
	assertLints(t, ledgerOf(w))
}

func TestBareRejectHoldsUntilADecidingReply(t *testing.T) {
	// Plan 2's route moves a TRIAGED needs-config item on whether or not it
	// is decided, so nothing holds it as triage-rejected yet.
	w := proposed(t, needsConfig(fieldKey, "config-only", "create-only"), ledger.DecisionFieldHook)
	items := ledgerOf(item(bumpKey, "SDK-bump", ledger.Queued), w)
	applyTriageCommands(items, cmds(reply(w, 1, "reject", "")), replyTS)
	assert.Equal(t, "", w.Triage.Decided())
	assert.Equal(t, 1, route(items, replyTS)[HoldTriageRejected])

	all := cmds(reply(w, 1, "reject", ""), reply(w, 2, "reject", ledger.DecisionFieldConfig), reply(w, 3, "accept", ""))
	assert.Empty(t, applyTriageCommands(items, all, "2026-11-12T10:00:00Z"))
	assert.Equal(t, ledger.DecisionFieldConfig, w.Triage.Decided(), "the first deciding reply wins")
	assert.Equal(t, int64(2), w.Triage.Review.CommentID)
	route(items, "2026-11-12T10:00:00Z")
	assert.Equal(t, ledger.Blocked, w.State)
	assertLints(t, items)
}

func TestNotWorthModellingClosesTheItem(t *testing.T) {
	w := proposed(t, needsConfig(fieldKey, "config-only", "create-only"), ledger.DecisionNotWorthModelling)
	applyTriageCommands(ledgerOf(w), cmds(reply(w, 1, "accept", "")), replyTS)
	assert.Equal(t, ledger.WontFix, w.State)
	assert.Equal(t, &ledger.Verdict{Outcome: "wont_fix:product_decision", Reason: "TRIAGE_NOT_WORTH_MODELLING"}, w.Verdict)
	assert.Equal(t, "TRIAGE_NOT_WORTH_MODELLING by maintainer", last(w).Reason)
	assertLints(t, ledgerOf(w))
}

func TestAServerDefaultItemIsNotClosedByTriage(t *testing.T) {
	w := proposed(t, needsConfig(fieldKey, "config-only", serverDefaultWork), ledger.DecisionFieldConfig)
	skipped := applyTriageCommands(ledgerOf(w), cmds(reply(w, 1, "reject", ledger.DecisionNotWorthModelling)), replyTS)
	require.Len(t, skipped, 1)
	assert.Contains(t, skipped[0], "server-default")
	assert.Nil(t, w.Triage.Review)
}

func TestAcceptedResourceIsANewResourceDraft(t *testing.T) {
	w := proposed(t, needsConfig(fieldKey, "hand-written-code", "custom-update"), ledger.DecisionResource)
	items := ledgerOf(item(bumpKey, "SDK-bump", ledger.Queued), w)
	applyTriageCommands(items, cmds(reply(w, 1, "accept", "")), replyTS)
	assert.Equal(t, []any{"new-resource", "draft", 6}, []any{w.ChangeClass, w.AutonomyLevel, w.PriorityBand})
	assert.Equal(t, 1, route(items, replyTS)[HoldAwaitingWorkflow], "no plan-resource workflow in Phase 1")
}

func TestAnUndecidedProposalIsDecidedByAReject(t *testing.T) {
	w := proposed(t, needsConfig(fieldKey, "config-only", "not-generated"), ledger.DecisionUndecided)
	skipped := applyTriageCommands(ledgerOf(w), cmds(reply(w, 1, "accept", "")), replyTS)
	require.Len(t, skipped, 1)
	assert.Contains(t, skipped[0], "cannot be accepted")
	applyTriageCommands(ledgerOf(w), cmds(reply(w, 2, "reject", ledger.DecisionFieldHook)), replyTS)
	assert.Equal(t, "hand-written-code", w.ChangeClass)
}

func TestRepliesAreBoundToTheProposal(t *testing.T) {
	w := proposed(t, needsConfig(fieldKey, "config-only", "create-only"), ledger.DecisionFieldConfig)
	items := ledgerOf(item(bumpKey, "SDK-bump", ledger.Queued), w)

	elsewhere := reply(w, 1, "accept", "")
	elsewhere.IssueURL = "https://github.com/o/ack-private/issues/99"
	unknown := reply(w, 2, "accept", "")
	unknown.LaunchID = "autopilot-ffffffffffffffff-g1-t1"
	skipped := applyTriageCommands(items, cmds(elsewhere, unknown), replyTS)
	require.Len(t, skipped, 2)
	assert.Contains(t, skipped[0], "another issue")
	assert.Contains(t, skipped[1], "matches no triage proposal")
	assert.Nil(t, w.Triage.Review)

	first, later := reply(w, 3, "accept", ""), reply(w, 4, "reject", ledger.DecisionFieldHook)
	assert.Empty(t, applyTriageCommands(items, cmds(first, later), replyTS))
	assert.Equal(t, ledger.DecisionFieldConfig, w.Triage.Decided())
	before := marshalAll(t, items)
	assert.Empty(t, applyTriageCommands(items, cmds(first, later), "2026-11-12T10:00:00Z"))
	assert.Equal(t, before, marshalAll(t, items), "replies are idempotent across ticks")

	// The detector reclassifies the item and triage runs again: the old
	// replies name a past proposal, are silent, and accept nothing.
	w.DetectorWork, w.ChangeClass, w.State = "custom-update", "hand-written-code", ledger.Detected
	w.History = append(w.History, ledger.Transition{TS: "2026-11-12T10:30:00Z", To: ledger.Detected, By: "issue-finder", Reason: "DETECTOR_RECLASSIFIED"})
	reconcileTriage(context.Background(), items, &fakeLauncher{}, withTriage(t), mustTime("2026-11-12T10:30:00Z"))
	proposed(t, w, ledger.DecisionFieldHook)
	assert.Empty(t, applyTriageCommands(items, cmds(first, later), "2026-11-12T11:00:00Z"))
	assert.True(t, w.Triage.Pending(), "an old accept never accepts a new proposal")
}
