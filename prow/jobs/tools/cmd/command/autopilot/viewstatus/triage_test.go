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

// Package viewstatus renders the Dispatcher's status comment on an api-change
// issue and plans the one edit that keeps it current. The detector stays the
// only writer of the issue body (design 4.5).
package viewstatus

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/issuefinder"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

const triageKey = "api-change|s3|Bucket|spec:Mode|needs-config"

// pendingAt is a TRIAGED item with a pending proposal.
func pendingAt(key, decision, class string, clamped bool) *ledger.WorkItem {
	w := wi(key, ledger.Triaged)
	w.Generation, w.ChangeClass, w.DetectorWork = 1, "config-only", "create-only"
	w.Triage = &ledger.Triage{Mode: ledger.TriageModeAPI, Proposal: &ledger.TriageProposal{
		LaunchID: ledger.TriageLaunchID(w, 1), Decision: decision, Class: class, Clamped: clamped,
		Reason: "use `is_immutable` @team\nnow", EvidenceRefs: []string{"op:CreateBucket"}, TS: "2026-11-12T09:30:00Z",
	}}
	return w
}

func TestRenderTriage(t *testing.T) {
	w := pendingAt(triageKey, ledger.DecisionFieldConfig, "config-only", false)
	id := w.Triage.Proposal.LaunchID
	got := RenderTriage(w)
	assert.True(t, strings.HasPrefix(got, "<!-- ack-autopilot-triage v1 "+id+" -->\n"))
	assert.Contains(t, got, "- Proposal: **`field-config`** → class `config-only`\n")
	assert.Contains(t, got, "- Model's reason (unverified text): `use 'is_immutable' @team now`\n")
	assert.Contains(t, got, "- Evidence (checked against the SDK model and the CRD): `op:CreateBucket`\n")
	assert.Contains(t, got, "/autopilot triage accept "+id+"\n")
	assert.Contains(t, got, "/autopilot triage reject "+id+" field-hook\n")
	assert.NotContains(t, got, "/autopilot triage reject "+id+" field-config\n", "no reply that repeats the proposal")

	before, rest, ok := strings.Cut(got, "```\n")
	require.True(t, ok)
	_, after, ok := strings.Cut(rest, "```\n")
	require.True(t, ok)
	assert.False(t, regexp.MustCompile(`(?m)^/autopilot `).MatchString(before+after), "reply lines only inside the fence")

	// The issue finder reads none of it, even if a team member posts it.
	is := issuefinder.Issue{Number: 27, URL: "u", Open: true, Author: "ack-bot",
		Labels:   []string{"ack/api-change-detected", "service/s3"},
		Comments: []issuefinder.Comment{{ID: 1, Author: "maintainer", Body: got}}}
	cmds, _ := issuefinder.TriageCommands(issuefinder.Config{BotLogin: "ack-bot", Team: map[string]bool{"maintainer": true}}, []issuefinder.Issue{is})
	assert.Empty(t, cmds)
}

func TestRenderTriageVariants(t *testing.T) {
	clamped := RenderTriage(pendingAt(triageKey, ledger.DecisionFieldConfig, "hand-written-code", true))
	assert.Contains(t, clamped, "(the detector's class is stricter and is kept)")

	undecided := pendingAt(triageKey, ledger.DecisionUndecided, "", false)
	undecided.Triage.Proposal.Invalid = []string{"UNKNOWN_REF op:Nope"}
	u := RenderTriage(undecided)
	assert.NotContains(t, u, "triage accept")
	assert.Contains(t, u, "- Why undecided: `UNKNOWN_REF op:Nope`\n")

	sd := pendingAt(triageKey, ledger.DecisionFieldConfig, "config-only", false)
	sd.DetectorWork = "server-default"
	s := RenderTriage(sd)
	assert.NotContains(t, s, " not-worth-modelling\n")
	assert.NotContains(t, s, " resource\n")

	assert.Contains(t, RenderTriage(pendingAt(triageKey, ledger.DecisionNotWorthModelling, "", false)), "wont_fix:product_decision")
}

func TestPlanTriage(t *testing.T) {
	pending := pendingAt(triageKey, ledger.DecisionFieldConfig, "config-only", false)
	forged := pendingAt("api-change|s3|Bucket|spec:Zeta|needs-config", ledger.DecisionFieldHook, "hand-written-code", false)
	reviewed := pendingAt("api-change|s3|Bucket|spec:Eta|needs-config", ledger.DecisionFieldHook, "hand-written-code", false)
	reviewed.Triage.Review = &ledger.TriageReview{LaunchID: reviewed.Triage.Proposal.LaunchID, Outcome: ledger.ReviewAccepted,
		Decision: ledger.DecisionFieldHook, By: "m", CommentID: 3, TS: "2026-11-12T10:00:00Z"}
	closed := pendingAt("api-change|s3|Bucket|spec:Theta|needs-config", ledger.DecisionFieldHook, "hand-written-code", false)
	closed.State = ledger.WontFix
	is := issuefinder.Issue{Number: 27, Comments: []issuefinder.Comment{
		{ID: 1, Author: "ack-bot", Body: TriageMarker(pending.Triage.Proposal.LaunchID) + "\nalready posted"},
		{ID: 2, Author: "someone", Body: TriageMarker(forged.Triage.Proposal.LaunchID) + "\nnot ours"},
	}}
	acts := PlanTriage(is, "ack-bot", []*ledger.WorkItem{pending, forged, reviewed, closed})
	require.Len(t, acts, 1)
	assert.Equal(t, forged.Triage.Proposal.LaunchID, acts[0].LaunchID)
	assert.Equal(t, Create, acts[0].Kind)
	assert.Equal(t, 27, acts[0].Issue)
	assert.Equal(t, RenderTriage(forged), acts[0].Body)
	assert.Nil(t, PlanTriage(is, "", []*ledger.WorkItem{forged}), "an empty login posts nothing")
}

func TestRenderShowsTriageState(t *testing.T) {
	pending := pendingAt(triageKey, ledger.DecisionFieldConfig, "config-only", false)
	running := wi("api-change|s3|Bucket|spec:A|needs-config", ledger.Detected)
	running.Triage = &ledger.Triage{Mode: ledger.TriageModeAPI, Claim: &ledger.Claim{LaunchID: "x"}}
	accepted := pendingAt("api-change|s3|Bucket|spec:B|needs-config", ledger.DecisionFieldHook, "hand-written-code", false)
	accepted.Triage.Review = &ledger.TriageReview{Outcome: ledger.ReviewAccepted}
	failed := wi("api-change|s3|Bucket|spec:C|needs-config", ledger.Detected)
	failed.Triage = &ledger.Triage{Mode: ledger.TriageModeAPI, Failed: "MODEL_CALL_FAILED"}
	got := Render([]*ledger.WorkItem{pending, running, accepted, failed})
	assert.Contains(t, got, " triage `field-config` `pending`\n")
	assert.Contains(t, got, " triage `running`\n")
	assert.Contains(t, got, " triage `field-hook` `accepted`\n")
	assert.Contains(t, got, " triage `failed` `MODEL_CALL_FAILED`\n")
}
