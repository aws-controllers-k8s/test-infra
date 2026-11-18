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

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

// TestTickLifecycle is Plan 9's exit criterion in one ledger: an SDK bump's
// run opens its PR, gates, is marked ready, is merged by a person, and its
// BLOCKED config-only dependent is launched.
func TestTickLifecycle(t *testing.T) {
	p := testPolicy(t)
	bump := running(item(bumpKey, "SDK-bump", ledger.Queued), "2026-11-12T08:30:00Z", 1, 3)
	field := item(fieldKey, "config-only", ledger.Blocked)
	field.Resource, field.FieldPath, field.PriorityBand = "Bucket", "Mode", 4
	field.DependsOn = []string{bumpKey}
	field.History = append(field.History, ledger.Transition{TS: t0, To: ledger.Blocked, By: actor, Reason: "DEPENDS_ON x"})
	items := ledgerOf(bump, field)

	v := verdictFor(t, bump, dod.StatusPass, "")
	gh := newFakeGH()
	pr := openPR("ack-bot")
	pr.Body = "card\n\n" + dod.Trailer(v)
	gh.prs[7] = pr
	run := bump.Claim.LaunchID
	l := gateLauncher{fakeLauncher: &fakeLauncher{statuses: map[string]Status{
		run: *finishedAs(JobSucceeded, "pr_opened", "", s3PR),
	}}, verdicts: map[string][]byte{run: v}}
	cfg := Config{Now: now, Policy: &p, Launcher: l, GitHub: gh, PRWriter: gh, Team: map[string]bool{"alice": true},
		PRAuthors: map[string]string{"sdk-bump": "ack-bot", "add-field": "ack-agent"}, BotLogins: []string{"ack-bot", "ack-agent"}}

	res := Tick(context.Background(), cfg, items)
	assert.Empty(t, res.Problems)
	assert.Equal(t, ledger.HumanReview, bump.State)
	assert.Equal(t, []ledger.State{ledger.Gating, ledger.PRReady, ledger.HumanReview}, states(bump)[len(bump.History)-3:])
	assert.Equal(t, []int{7}, gh.ready)
	assert.Equal(t, ledger.Blocked, field.State)
	assertLints(t, items)

	pr = gh.prs[7]
	pr.State, pr.Merged, pr.MergedAt, pr.MergeSHA = "closed", true, "2026-11-12T10:00:00Z", head2
	gh.prs[7] = pr
	cfg.Now = now.Add(2 * time.Hour)
	res = Tick(context.Background(), cfg, items)
	assert.Empty(t, res.Problems)
	assert.Equal(t, ledger.Done, bump.State)
	assert.Equal(t, ledger.Running, field.State, "the merged bump unblocks and launches its dependent")
	require.Len(t, l.launched, 1)
	assert.Equal(t, "add-field", l.launched[0].Workflow)
	assertLints(t, items)
}

func TestTickDryRunMarksNothingReady(t *testing.T) {
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
	cfg := Config{Now: now, Policy: &p, Launcher: l, DryRun: true, GitHub: gh, PRWriter: gh,
		PRAuthors: map[string]string{"sdk-bump": "ack-bot"}, BotLogins: []string{"ack-bot"}}
	Tick(context.Background(), cfg, ledgerOf(bump))
	assert.Equal(t, ledger.HumanReview, bump.State, "a dry run reads the verdict through the real launcher")
	assert.Empty(t, gh.ready, "a dry run never writes to a PR")
}
