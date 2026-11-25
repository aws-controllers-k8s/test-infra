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

package prowlaunch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dispatcher"
)

// triageCatalog is testCatalog plus the triage fixture (Plan 8).
func triageCatalog(t *testing.T) *Catalog {
	t.Helper()
	aw, err := LoadAgentWorkflows("testdata/agent-workflows.yaml", testVars)
	require.NoError(t, err)
	tr, err := LoadAgentWorkflows("testdata/agent-workflows-triage.yaml", testVars)
	require.NoError(t, err)
	aw["triage"] = tr["triage"]
	image, err := LoadImage("testdata/images_config.yaml", SDKBumpImageKey, testVars)
	require.NoError(t, err)
	c, err := NewCatalog(aw, SDKBump(image, testVars.TestInfraOrg))
	require.NoError(t, err)
	return c
}

// goldenTriage is the triage lane's request for the s3 fixture item (the
// args dispatcher.triageArgs builds; Plan 8 Tasks 1 and 11).
var goldenTriage = dispatcher.LaunchRequest{
	LaunchID: "autopilot-0123456789abcdef-g1-t1",
	Workflow: "triage",
	Args: map[string]string{
		"service": "s3", "resource": "Bucket", "field": "ObjectLockConfiguration.Rule.DefaultRetention.DefaultEventHold",
		"finding-class": "spec-field", "detector-work": "from-sibling", "change-class": "hand-written-code",
		"sdk-to": "service/s3/v1.114.10", "work-item": "sha256:0123456789abcdef",
	},
	Timeout: 30 * time.Minute,
}

func TestTriageGolden(t *testing.T) {
	path := filepath.Join("testdata", "prowjob-triage.golden.json")
	pj, err := triageCatalog(t).Build(testConfig, goldenTriage)
	require.NoError(t, err)
	b, err := json.Marshal(pj)
	require.NoError(t, err)
	got := normalize(t, b)
	if *update {
		require.NoError(t, os.WriteFile(path, got, 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got))
}

func TestTriageJobHasNoSecretAndNoPrivilege(t *testing.T) {
	c := triageCatalog(t)
	pj, err := c.Build(testConfig, goldenTriage)
	require.NoError(t, err)
	assert.Equal(t, goldenTriage.LaunchID, pj.Metadata.Name)
	assert.Equal(t, "agent-workflow-triage", pj.Spec.Job)
	assert.Equal(t, "build", pj.Spec.Cluster)
	assert.Equal(t, "t1", pj.Metadata.Labels[LabelRun])
	assert.Equal(t, "triage", pj.Metadata.Labels[LabelWorkflow])
	assert.Empty(t, pj.Spec.ExtraRefs)
	assert.Equal(t, "30m0s", pj.Spec.DecorationConfig.Timeout)
	pod := pj.Spec.PodSpec
	assert.Equal(t, "workflow-runner", pod.ServiceAccountName)
	assert.Empty(t, pod.Volumes, "no agent-secrets volume without environmentFromSecrets")
	require.Len(t, pod.Containers, 1)
	ctr := pod.Containers[0]
	assert.Equal(t, []string{"/app/triage-job.sh"}, ctr.Command)
	assert.Empty(t, ctr.VolumeMounts)
	assert.Nil(t, ctr.SecurityContext, "e2e: false gives an unprivileged pod")
	for _, e := range ctr.Env {
		assert.Nil(t, e.ValueFrom, "no secret reaches the environment: %s", e.Name)
	}

	// The workflows that read a secret keep their secrets volume first, as
	// their goldens (unchanged) and agent-plugin's job have it.
	for _, req := range []dispatcher.LaunchRequest{goldenAddField, goldenSDKBump} {
		pj, err := c.Build(testConfig, req)
		require.NoError(t, err)
		require.NotEmpty(t, pj.Spec.PodSpec.Volumes, req.Workflow)
		assert.NotNil(t, pj.Spec.PodSpec.Volumes[0].CSI, req.Workflow)
		assert.Equal(t, pj.Spec.PodSpec.Volumes[0].Name, pj.Spec.PodSpec.Containers[0].VolumeMounts[0].Name, req.Workflow)
	}
}

func TestBuildRefusesBadTriageRequests(t *testing.T) {
	c := triageCatalog(t)
	withID := func(r dispatcher.LaunchRequest, id string) dispatcher.LaunchRequest { r.LaunchID = id; return r }
	for name, tc := range map[string]struct {
		req  dispatcher.LaunchRequest
		want string
	}{
		"missing sdk-to":          {withArgs(goldenTriage, func(a map[string]string) { delete(a, "sdk-to") }), `missing required arg "sdk-to"`},
		"undeclared arg":          {withArgs(goldenTriage, func(a map[string]string) { a["model-response-file"] = "/etc/hostname" }), `"model-response-file" is not in`},
		"model has no validator":  {withArgs(goldenTriage, func(a map[string]string) { a["model"] = "x" }), `"model" is not one the Dispatcher may pass`},
		"finding class":           {withArgs(goldenTriage, func(a map[string]string) { a["finding-class"] = "class=odd" }), `"finding-class" has a value`},
		"detector work":           {withArgs(goldenTriage, func(a map[string]string) { a["detector-work"] = "From Sibling" }), `"detector-work" has a value`},
		"class SDK-bump":          {withArgs(goldenTriage, func(a map[string]string) { a["change-class"] = "SDK-bump" }), `"change-class" has a value`},
		"class new-resource":      {withArgs(goldenTriage, func(a map[string]string) { a["change-class"] = "new-resource" }), `"change-class" has a value`},
		"another item":            {withArgs(goldenTriage, func(a map[string]string) { a["work-item"] = "sha256:ffffffffffffffff" }), "launch id's key hash"},
		"run id for triage":       {withID(goldenTriage, "autopilot-0123456789abcdef-g1-r1"), "names a triage run"},
		"triage id for add-field": {withID(goldenAddField, "autopilot-0123456789abcdef-g1-t1"), "names a triage run"},
		"triage id t0":            {withID(goldenTriage, "autopilot-0123456789abcdef-g1-t0"), "not a dispatcher launch id"},
		"triage id g0":            {withID(goldenTriage, "autopilot-0123456789abcdef-g0-t1"), "generation starts at 1"},
		"triage id g01":           {withID(goldenTriage, "autopilot-0123456789abcdef-g01-t1"), "generation starts at 1"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := c.Build(testConfig, tc.req)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
	// add-field keeps its own shapes: triage's do not leak into it.
	_, err := c.Build(testConfig, withArgs(goldenAddField, func(a map[string]string) { a["detector-work"] = "from-sibling" }))
	assert.ErrorContains(t, err, `"detector-work" has a value`)
	_, err = c.Build(testConfig, withArgs(goldenAddField, func(a map[string]string) { a["change-class"] = "hand-written-code" }))
	assert.ErrorContains(t, err, `"change-class" has a value`)
	_, err = testCatalog(t).Build(testConfig, goldenTriage)
	assert.ErrorContains(t, err, "not on the allowlist", "without a triage entry in agent-workflows.yaml, triage cannot start")
}

func TestNewCatalogRefusesATriageWithSecretsE2EOrRefs(t *testing.T) {
	aw, err := LoadAgentWorkflows("testdata/agent-workflows.yaml", testVars)
	require.NoError(t, err)
	tr, err := LoadAgentWorkflows("testdata/agent-workflows-triage.yaml", testVars)
	require.NoError(t, err)
	for name, edit := range map[string]func(w *Workflow){
		"a secret": func(w *Workflow) {
			w.EnvironmentFromSecrets = map[string]SecretKeyRef{"GITHUB_TOKEN": {Name: "agent-github-pat-token", Key: "token"}}
		},
		"e2e":       func(w *Workflow) { w.E2E = true },
		"extra ref": func(w *Workflow) { w.ExtraRefs = []ExtraRef{{Org: "aws-controllers-k8s", Repo: "test-infra"}} },
	} {
		w := tr["triage"]
		edit(&w)
		aw["triage"] = w
		_, err := NewCatalog(aw, SDKBump("r:t", "o"))
		assert.ErrorContains(t, err, "workflow triage must have no", name)
	}
}

// TestTheRealAgentWorkflowsHaveTriage reads the generated file this repo
// deploys (Task 7): triage loads, holds no secret, and declares exactly the
// args the triage lane passes.
func TestTheRealAgentWorkflowsHaveTriage(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "..", "..", "..")
	c, err := FromCheckout(root, testVars)
	require.NoError(t, err)
	tr, ok := c.workflows["triage"]
	require.True(t, ok, "agent-workflows.yaml has the triage entry (Plan 8 Task 7)")
	assert.Empty(t, tr.EnvironmentFromSecrets)
	assert.False(t, tr.E2E)
	assert.Empty(t, tr.ExtraRefs)
	assert.Equal(t, []string{"model"}, tr.OptionalArgs)
	assert.Len(t, tr.RequiredArgs, len(goldenTriage.Args))
	for _, a := range tr.RequiredArgs {
		assert.Contains(t, goldenTriage.Args, a)
	}
	assert.True(t, strings.HasPrefix(tr.Image, "registry.example/prow:add-resource-"), tr.Image)
}

// The verdict survives Plan 7's Status: the file is the job's own, and the
// whole ParseResult (with Triage) is returned for reconcileTriage to check.
func TestStatusKeepsTheTriageVerdict(t *testing.T) {
	api := newFakeAPI("tok")
	k, _ := testKube(t, api, "tok")
	art := &fakeArtifacts{objects: map[string][]byte{}}
	l, err := New(triageCatalog(t), testConfig, k, art)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, l.Launch(ctx, goldenTriage))
	require.NoError(t, l.Launch(ctx, goldenTriage), "a repeated launch adopts")

	active, err := l.Active(ctx)
	require.NoError(t, err)
	assert.Equal(t, []dispatcher.ActiveJob{{LaunchID: goldenTriage.LaunchID, Workflow: "triage", Service: "s3"}}, active)

	finish(api, goldenTriage.LaunchID, "success", "88")
	b, err := os.ReadFile("../dispatcher/testdata/agent-result-triage-v1.json")
	require.NoError(t, err)
	key := "logs/agent-workflow-triage/88/artifacts/agent-result.json"
	art.objects[key] = b
	st, err := l.Status(ctx, goldenTriage.LaunchID)
	require.NoError(t, err)
	assert.Equal(t, dispatcher.JobSucceeded, st.Job)
	require.NotNil(t, st.Result, st.Problem)
	assert.Equal(t, "triage", st.Result.Workflow)
	assert.Equal(t, goldenTriage.LaunchID, st.Result.ProwJobID)
	assert.Contains(t, string(st.Result.Triage), `"decision": "field-hook"`)
	assert.Equal(t, []string{key}, art.keys)
}
