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

package autopilot

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dispatcher"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/policy"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/prowlaunch"
)

func envMap(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLauncherForNeedsBothSwitches(t *testing.T) {
	called := 0
	saved := newProwLauncher
	t.Cleanup(func() { newProwLauncher = saved })
	newProwLauncher = func(context.Context, string, func(string) string) (dispatcher.Launcher, error) {
		called++
		return &dispatcher.ShadowLauncher{}, nil
	}
	ctx := context.Background()
	shadow := policy.Policy{Switches: policy.Switches{Launcher: policy.LauncherShadow}}
	prow := policy.Policy{Switches: policy.Switches{Launcher: policy.LauncherProw}}

	l, err := launcherFor(ctx, shadow, "", envMap(map[string]string{launcherEnabledEnv: "true"}))
	require.NoError(t, err)
	assert.True(t, l.Shadow())

	_, err = launcherFor(ctx, prow, ".", envMap(nil))
	assert.ErrorContains(t, err, `AUTOPILOT_LAUNCHER_ENABLED is not "true"`)
	_, err = launcherFor(ctx, prow, ".", envMap(map[string]string{launcherEnabledEnv: "yes"}))
	assert.Error(t, err)
	_, err = launcherFor(ctx, prow, "", envMap(map[string]string{launcherEnabledEnv: "true"}))
	assert.ErrorContains(t, err, "--test-infra-dir")
	assert.Zero(t, called)

	_, err = launcherFor(ctx, prow, ".", envMap(map[string]string{launcherEnabledEnv: "true"}))
	require.NoError(t, err)
	assert.Equal(t, 1, called)

	newProwLauncher = func(context.Context, string, func(string) string) (dispatcher.Launcher, error) {
		return nil, errors.New("not in a cluster")
	}
	l, err = launcherFor(ctx, prow, ".", envMap(map[string]string{launcherEnabledEnv: "true"}))
	assert.ErrorContains(t, err, "not in a cluster")
	assert.Nil(t, l)
}

// usePolicy sets policy/autopilot.yaml's launcher in a ledger dir.
func usePolicy(t *testing.T, dir, launcher string) {
	t.Helper()
	p := filepath.Join(dir, "policy", "autopilot.yaml")
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(p, []byte(strings.Replace(string(b), "launcher: shadow", "launcher: "+launcher, 1)), 0o644))
}

func TestDispatchProwLauncherOffLaunchesNothing(t *testing.T) {
	saved := getenv
	t.Cleanup(func() { getenv = saved })
	getenv = envMap(nil)
	dir := t.TempDir()
	copyPolicy(t, dir)
	usePolicy(t, dir, policy.LauncherProw)
	o := opts(dir)
	o.DryRun = true
	var out strings.Builder
	require.NoError(t, dispatch(context.Background(), fakeGitHub(t), o, tickTime, &out))
	assert.Contains(t, out.String(), `problem policy: policy names launcher "prow" but AUTOPILOT_LAUNCHER_ENABLED is not "true" in this job; nothing reconciled or launched`)
	assert.NotContains(t, out.String(), "\nlaunch ")
}

func TestLaunchPreview(t *testing.T) {
	dir := t.TempDir()
	copyPolicy(t, dir)
	key := "api-change|s3|Bucket|spec:AccelerateConfiguration.Status|needs-config"
	w := &ledger.WorkItem{
		Schema: ledger.Schema, Key: key, KeyHash: ledger.KeyHash(key), Generation: 1, Source: "api-change",
		Service: "s3", Repo: "aws-controllers-k8s/s3-controller", Resource: "Bucket", FieldPath: "accelerateConfiguration.status",
		ChangeClass: "config-only", AutonomyLevel: "human-merge", PriorityBand: 4, DetectorWork: "server-default",
		State: ledger.Queued, History: []ledger.Transition{{TS: "2026-11-17T08:00:00Z", To: ledger.Queued, By: "test"}},
	}
	require.NoError(t, ledger.Save(dir, map[string]*ledger.WorkItem{key: w}))
	var out strings.Builder
	o := previewOptions{LedgerDir: dir, KeyHash: w.KeyHash, TestInfraDir: filepath.Join("..", "..", "..", "..", "..", ".."),
		ImagesRepo: "registry.example/prow", Org: "aws-controllers-k8s", Bucket: "s3://example-prow-logs"}
	require.NoError(t, launchPreview(o, &out))
	var pj prowlaunch.ProwJob
	require.NoError(t, json.Unmarshal([]byte(out.String()), &pj))
	assert.Equal(t, "autopilot-"+strings.TrimPrefix(w.KeyHash, "sha256:")+"-g1-r1", pj.Metadata.Name)
	assert.Equal(t, "agent-workflow-add-field", pj.Spec.Job)
	assert.Contains(t, pj.Spec.PodSpec.Containers[0].Args, "server-default")

	o.KeyHash = "sha256:ffffffffffffffff"
	assert.ErrorContains(t, launchPreview(o, &out), "no item")
}
