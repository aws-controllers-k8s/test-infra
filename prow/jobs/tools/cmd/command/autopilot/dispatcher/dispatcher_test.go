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
