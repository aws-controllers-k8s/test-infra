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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dispatcher"
)

var goldenAddResource = dispatcher.LaunchRequest{
	LaunchID: "autopilot-0123456789abcdef-g1-r1",
	Workflow: "add-resource",
	Args: map[string]string{
		"service": "s3", "resource": "Widget",
		"work-item": "sha256:0123456789abcdef", "change-class": "new-resource",
	},
	Timeout: 5 * time.Hour,
}

var goldenDod = dispatcher.LaunchRequest{
	LaunchID: "autopilot-0123456789abcdef-g1-d1",
	Workflow: "dod",
	Args: map[string]string{
		"service": "s3", "pr": "7", "head-sha": "1111111111111111111111111111111111111111",
		"base-sha": "2222222222222222222222222222222222222222", "pr-author": "ack-agent",
		"work-item": "sha256:0123456789abcdef", "change-class": "config-only",
	},
	Timeout: 2 * time.Hour,
}

func TestAddResourceMatchesAgentPlugin(t *testing.T) {
	want, err := os.ReadFile("testdata/prowjob-add-resource.golden.json")
	require.NoError(t, err)
	assert.Equal(t, string(want), string(normalize(t, buildJSON(t, goldenAddResource))))
}

func TestDodGolden(t *testing.T) {
	path := filepath.Join("testdata", "prowjob-dod.golden.json")
	got := normalize(t, buildJSON(t, goldenDod))
	if *update {
		require.NoError(t, os.WriteFile(path, got, 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got))
}

// TestDodJobHoldsOnlyItsSigningKey: the dod job's one volume is the verdict
// signing key (Plan 9 review): a secret volume of mode 0400, mounted read
// only. It has no other secret, no env valueFrom and no ServiceAccount token.
func TestDodJobHoldsOnlyItsSigningKey(t *testing.T) {
	var pj ProwJob
	require.NoError(t, json.Unmarshal(buildJSON(t, goldenDod), &pj))
	pod := pj.Spec.PodSpec
	assert.Equal(t, "autopilot-dod", pj.Spec.Job)
	assert.Equal(t, "default", pj.Spec.Cluster)
	assert.Equal(t, "pre-submit-service-account", pod.ServiceAccountName)
	require.Len(t, pod.Volumes, 1, "no secrets volume, no CSI, no host path: only the signing key")
	v := pod.Volumes[0]
	assert.Equal(t, "autopilot-dod-signing", v.Name)
	require.NotNil(t, v.Secret)
	assert.Equal(t, "autopilot-dod-signing-key", v.Secret.SecretName)
	require.NotNil(t, v.Secret.DefaultMode)
	assert.Equal(t, int32(0o400), *v.Secret.DefaultMode)
	assert.Nil(t, v.CSI)
	assert.Nil(t, v.HostPath)
	assert.Nil(t, v.EmptyDir)
	// Plan 9 review F14: no ServiceAccount token in the pod either.
	require.NotNil(t, pod.AutomountServiceAccountToken)
	assert.False(t, *pod.AutomountServiceAccountToken)
	var ar ProwJob
	require.NoError(t, json.Unmarshal(buildJSON(t, goldenAddResource), &ar))
	assert.Nil(t, ar.Spec.PodSpec.AutomountServiceAccountToken, "agent-plugin's pod shape is unchanged")
	c := pod.Containers[0]
	assert.Equal(t, []VolumeMount{{Name: "autopilot-dod-signing", MountPath: "/etc/autopilot-dod-signing", ReadOnly: true}}, c.VolumeMounts)
	assert.Nil(t, c.SecurityContext, "unprivileged")
	assert.Empty(t, c.Args, "args travel as environment variables")
	env := map[string]string{}
	for _, e := range c.Env {
		assert.Nil(t, e.ValueFrom, e.Name)
		assert.NotContains(t, strings.ToUpper(e.Name), "TOKEN", e.Name)
		env[e.Name] = e.Value
	}
	assert.Equal(t, "1111111111111111111111111111111111111111", env["AUTOPILOT_DOD_HEAD_SHA"])
	assert.Equal(t, "config-only", env["AUTOPILOT_DOD_CLASS"])
	assert.Equal(t, "ack-agent", env["AUTOPILOT_DOD_PR_AUTHOR"])
	var repos []string
	for _, r := range pj.Spec.ExtraRefs {
		repos = append(repos, r.Repo)
	}
	assert.Equal(t, []string{"code-generator", "runtime", "test-infra", "s3-controller"}, repos)
	assert.Equal(t, "true", pj.Metadata.Annotations[AnnotationDoNotDisrupt])
}

func TestBuildRefusesMismatchedDodIDs(t *testing.T) {
	c := testCatalog(t)
	bad := goldenDod
	bad.LaunchID = "autopilot-0123456789abcdef-g1-r1"
	_, err := c.Build(testConfig, bad)
	assert.ErrorContains(t, err, "d<n>")
	notDod := goldenAddResource
	notDod.LaunchID = "autopilot-0123456789abcdef-g1-d1"
	_, err = c.Build(testConfig, notDod)
	assert.ErrorContains(t, err, "d<n>")
}

func TestBuildChecksThePlan9Args(t *testing.T) {
	c := testCatalog(t)
	for name, edit := range map[string]func(a map[string]string){
		"short sha":         func(a map[string]string) { a["head-sha"] = "abc" },
		"pr zero":           func(a map[string]string) { a["pr"] = "0" },
		"bad author":        func(a map[string]string) { a["pr-author"] = "a b" },
		"other class":       func(a map[string]string) { a["change-class"] = "hand-written-code" },
		"missing base":      func(a map[string]string) { delete(a, "base-sha") },
		"undeclared arg":    func(a map[string]string) { a["mode"] = "bump" },
		"another work item": func(a map[string]string) { a["work-item"] = "sha256:fedcba9876543210" },
	} {
		t.Run(name, func(t *testing.T) {
			req := goldenDod
			req.Args = map[string]string{}
			for k, v := range goldenDod.Args {
				req.Args[k] = v
			}
			edit(req.Args)
			_, err := c.Build(testConfig, req)
			assert.Error(t, err)
		})
	}
	ar := goldenAddResource
	ar.Args = map[string]string{"service": "s3", "resource": "Widget", "work-item": "sha256:0123456789abcdef", "change-class": "config-only"}
	_, err := c.Build(testConfig, ar)
	assert.ErrorContains(t, err, "change-class")
}

func TestStatusOfADodJobReadsNoResult(t *testing.T) {
	l, api, art := testLauncher(t)
	require.NoError(t, l.Launch(context.Background(), goldenDod))
	finish(api, goldenDod.LaunchID, "success", "41")
	st, err := l.Status(context.Background(), goldenDod.LaunchID)
	require.NoError(t, err)
	assert.Equal(t, dispatcher.Status{Phase: dispatcher.PhaseFinished, Job: dispatcher.JobSucceeded}, st)
	assert.Empty(t, art.keys)
}

func TestVerdictReadsTheJobsVerdict(t *testing.T) {
	l, api, art := testLauncher(t)
	ctx := context.Background()
	require.NoError(t, l.Launch(ctx, goldenDod))

	_, _, err := l.Verdict(ctx, goldenDod.LaunchID)
	assert.ErrorIs(t, err, dispatcher.ErrNoVerdict, "not finished")

	finish(api, goldenDod.LaunchID, "failure", "41")
	_, _, err = l.Verdict(ctx, goldenDod.LaunchID)
	assert.ErrorIs(t, err, dispatcher.ErrNoVerdict, "dod exit 2 leaves no file")

	art.objects["logs/autopilot-dod/41/artifacts/verdict.json"] = []byte(`{"schema":"x"}`)
	got, sig, err := l.Verdict(ctx, goldenDod.LaunchID)
	require.NoError(t, err)
	assert.Equal(t, `{"schema":"x"}`, string(got))
	assert.Nil(t, sig, "no verdict.sig: a nil signature and no error; gate decides")

	art.objects["logs/autopilot-dod/41/artifacts/verdict.json"] = make([]byte, dispatcher.MaxVerdictBytes+1)
	_, _, err = l.Verdict(ctx, goldenDod.LaunchID)
	assert.ErrorIs(t, err, dispatcher.ErrNoVerdict, "too large")

	_, _, err = l.Verdict(ctx, "autopilot-0123456789abcdef-g1-d9")
	assert.ErrorIs(t, err, dispatcher.ErrNoVerdict, "gone")

	require.NoError(t, l.Launch(ctx, goldenAddResource))
	finish(api, goldenAddResource.LaunchID, "success", "42")
	_, _, err = l.Verdict(ctx, goldenAddResource.LaunchID)
	assert.ErrorContains(t, err, "not a Dispatcher sdk-bump or dod job", "an agent's pod could write a verdict.json")
}

// TestVerdictReadsTheDodSignature (Plan 9 review): a dod job's verdict.sig
// comes back with its verdict, at most MaxSignatureBytes; sdk-bump's is
// never read (its verdict is bound to the PR body's trailer).
func TestVerdictReadsTheDodSignature(t *testing.T) {
	l, api, art := testLauncher(t)
	ctx := context.Background()
	require.NoError(t, l.Launch(ctx, goldenDod))
	finish(api, goldenDod.LaunchID, "success", "41")
	art.objects["logs/autopilot-dod/41/artifacts/verdict.json"] = []byte(`{"schema":"x"}`)
	art.objects["logs/autopilot-dod/41/artifacts/verdict.sig"] = []byte("c2ln\n")
	v, sig, err := l.Verdict(ctx, goldenDod.LaunchID)
	require.NoError(t, err)
	assert.Equal(t, `{"schema":"x"}`, string(v))
	assert.Equal(t, "c2ln\n", string(sig))

	art.objects["logs/autopilot-dod/41/artifacts/verdict.sig"] = make([]byte, dispatcher.MaxSignatureBytes+1)
	v, sig, err = l.Verdict(ctx, goldenDod.LaunchID)
	require.NoError(t, err, "a too large signature is no signature: gate escalates")
	assert.NotNil(t, v)
	assert.Nil(t, sig)
	assert.Equal(t, int64(1<<10), int64(dispatcher.MaxSignatureBytes))

	art.err = errors.New("s3: throttled")
	_, _, err = l.Verdict(ctx, goldenDod.LaunchID)
	assert.ErrorContains(t, err, "throttled")
	art.err = nil

	require.NoError(t, l.Launch(ctx, goldenSDKBump))
	finish(api, goldenSDKBump.LaunchID, "success", "43")
	art.objects["logs/autopilot-sdk-bump/43/artifacts/verdict.json"] = []byte(`{"schema":"y"}`)
	art.objects["logs/autopilot-sdk-bump/43/artifacts/verdict.sig"] = []byte("c2ln\n")
	art.keys = nil
	v, sig, err = l.Verdict(ctx, goldenSDKBump.LaunchID)
	require.NoError(t, err)
	assert.Equal(t, `{"schema":"y"}`, string(v))
	assert.Nil(t, sig)
	assert.Equal(t, []string{"logs/autopilot-sdk-bump/43/artifacts/verdict.json"}, art.keys, "sdk-bump never reads a signature")
}

func TestFeedbackActiveSelectsAgentPluginsJob(t *testing.T) {
	l, api, _ := testLauncher(t)
	ctx := context.Background()
	job := func(state string) map[string]any {
		return map[string]any{
			"metadata": map[string]any{"name": "feedback-1", "labels": map[string]any{
				"workflow-type": "address-feedback", "prow.k8s.io/refs.org": "aws-controllers-k8s",
				"prow.k8s.io/refs.repo": "s3-controller", "workflow-agent/issue-number": "7",
			}},
			"spec": map[string]any{}, "status": map[string]any{"state": state},
		}
	}
	api.jobs["feedback-1"] = job("pending")
	got, err := l.FeedbackActive(ctx, "aws-controllers-k8s/s3-controller", 7)
	require.NoError(t, err)
	assert.True(t, got)

	got, err = l.FeedbackActive(ctx, "aws-controllers-k8s/s3-controller", 8)
	require.NoError(t, err)
	assert.False(t, got, "another PR's job")

	api.jobs["feedback-1"] = job("success")
	got, err = l.FeedbackActive(ctx, "aws-controllers-k8s/s3-controller", 7)
	require.NoError(t, err)
	assert.False(t, got)

	_, err = l.FeedbackActive(ctx, "a,b/c", 7)
	assert.Error(t, err)
}
