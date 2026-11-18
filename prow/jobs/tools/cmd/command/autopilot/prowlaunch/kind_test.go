//go:build kindintegration

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

// Run only by hand against a throwaway kind cluster (Plan 7 Task 17 Step 6),
// never against a real cluster:
//   AUTOPILOT_KIND_SERVER=https://127.0.0.1:<port> AUTOPILOT_KIND_TOKEN_FILE=... \
//   AUTOPILOT_KIND_CA_FILE=... go test -tags kindintegration -run Kind ./prow/jobs/tools/cmd/command/autopilot/prowlaunch/

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func kindKube(t *testing.T, namespace string) *Kube {
	t.Helper()
	server := os.Getenv("AUTOPILOT_KIND_SERVER")
	u, err := url.Parse(server)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", u.Hostname(), "only a local kind cluster")
	ca, err := os.ReadFile(os.Getenv("AUTOPILOT_KIND_CA_FILE"))
	require.NoError(t, err)
	k, err := newKube(server, namespace, os.Getenv("AUTOPILOT_KIND_TOKEN_FILE"), ca)
	require.NoError(t, err)
	return k
}

// rawCreate posts a job after edit changes its JSON, to try what the
// launcher itself would never build.
func rawCreate(t *testing.T, k *Kube, pj *ProwJob, name string, edit func(m map[string]any)) error {
	t.Helper()
	b, err := json.Marshal(pj)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	m["metadata"].(map[string]any)["name"] = name
	edit(m)
	b, err = json.Marshal(m)
	require.NoError(t, err)
	_, _, err = k.do(context.Background(), http.MethodPost, k.base, b, "application/json")
	return err
}

func TestKindRBACAndAdmissionPolicy(t *testing.T) {
	ctx := context.Background()
	k := kindKube(t, Namespace)
	cat := testCatalog(t)
	l, err := New(cat, testConfig, k, &fakeArtifacts{})
	require.NoError(t, err)

	// Allowed: the two jobs the launcher builds, and their abort.
	require.NoError(t, l.Launch(ctx, goldenAddField))
	require.NoError(t, l.Launch(ctx, goldenAddField), "adopt")
	require.NoError(t, l.Launch(ctx, goldenSDKBump))
	require.NoError(t, l.Abort(ctx, goldenSDKBump.LaunchID))
	got, err := k.Get(ctx, goldenSDKBump.LaunchID)
	require.NoError(t, err)
	assert.Equal(t, "aborted", got.Status.State)
	active, err := l.Active(ctx)
	require.NoError(t, err)
	assert.Len(t, active, 1)

	pj, err := cat.Build(testConfig, goldenAddField)
	require.NoError(t, err)
	n := 0
	next := func() string { n++; return fmt.Sprintf("autopilot-%016x-g1-r1", n) }
	container := func(m map[string]any) map[string]any {
		return m["spec"].(map[string]any)["pod_spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	}
	for name, edit := range map[string]func(map[string]any){
		"other image":   func(m map[string]any) { container(m)["image"] = "docker.io/evil/image:latest" },
		"other command": func(m map[string]any) { container(m)["command"] = []any{"/bin/sh", "-c", "env"} },
		"other secret": func(m map[string]any) {
			container(m)["env"] = append(container(m)["env"].([]any), map[string]any{"name": "X",
				"valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "prowjob-github-pat-token", "key": "token"}}})
		},
		"pod namespace":  func(m map[string]any) { m["spec"].(map[string]any)["namespace"] = "kube-system" },
		"other job name": func(m map[string]any) { m["spec"].(map[string]any)["job"] = "postsubmit-release" },
		"no managed label": func(m map[string]any) {
			delete(m["metadata"].(map[string]any)["labels"].(map[string]any), LabelManagedBy)
		},
		"host path /": func(m map[string]any) {
			ps := m["spec"].(map[string]any)["pod_spec"].(map[string]any)
			ps["volumes"] = append(ps["volumes"].([]any), map[string]any{"name": "root", "hostPath": map[string]any{"path": "/"}})
		},
		"credentials secret": func(m map[string]any) {
			m["spec"].(map[string]any)["decoration_config"].(map[string]any)["s3_credentials_secret"] = "prowjob-github-pat-token"
		},
		"utility image": func(m map[string]any) {
			m["spec"].(map[string]any)["decoration_config"].(map[string]any)["utility_images"].(map[string]any)["sidecar"] = "docker.io/evil/sidecar"
		},
	} {
		err := rawCreate(t, k, pj, next(), edit)
		assert.Error(t, err, name)
	}

	// Patch beyond an abort is denied.
	err = k.Patch(ctx, goldenAddField.LaunchID, []byte(`{"spec":{"pod_spec":{"serviceAccountName":"post-submit-service-account"}}}`))
	assert.Error(t, err)
	err = k.Patch(ctx, goldenAddField.LaunchID, []byte(`{"status":{"state":"success"}}`))
	assert.Error(t, err)

	// RBAC: no delete, no other namespace, no other resource.
	_, _, err = k.do(ctx, http.MethodDelete, k.base+"/"+goldenAddField.LaunchID, nil, "")
	assert.ErrorContains(t, err, "403")
	_, err = kindKube(t, "default").List(ctx, LabelManagedBy+"="+ManagedBy)
	assert.ErrorContains(t, err, "403")
	secrets := strings.Replace(k.base, "/apis/prow.k8s.io/v1/namespaces/prow/prowjobs", "/api/v1/namespaces/test-pods/secrets", 1)
	_, _, err = k.do(ctx, http.MethodGet, secrets, nil, "")
	assert.ErrorContains(t, err, "403")
}

func TestKindAdmitsOnlyTheTriageJobBuildMakes(t *testing.T) {
	ctx := context.Background()
	k := kindKube(t, Namespace)
	cat := triageCatalog(t)
	l, err := New(cat, testConfig, k, &fakeArtifacts{})
	require.NoError(t, err)
	require.NoError(t, l.Launch(ctx, goldenTriage))
	require.NoError(t, l.Abort(ctx, goldenTriage.LaunchID))

	pj, err := cat.Build(testConfig, goldenTriage)
	require.NoError(t, err)
	n := 100
	next := func() string { n++; return fmt.Sprintf("autopilot-%016x-g1-t1", n) }
	podSpec := func(m map[string]any) map[string]any {
		return m["spec"].(map[string]any)["pod_spec"].(map[string]any)
	}
	container := func(m map[string]any) map[string]any { return podSpec(m)["containers"].([]any)[0].(map[string]any) }
	for name, edit := range map[string]func(map[string]any){
		"secrets volume": func(m map[string]any) {
			podSpec(m)["volumes"] = []any{map[string]any{"name": "agent-secrets", "csi": map[string]any{
				"driver": "secrets-store.csi.k8s.io", "volumeAttributes": map[string]any{"secretProviderClass": "agent-secrets"}}}}
			container(m)["volumeMounts"] = []any{map[string]any{"name": "agent-secrets", "mountPath": "/mnt/secrets-store"}}
		},
		"a token": func(m map[string]any) {
			container(m)["env"] = append(container(m)["env"].([]any), map[string]any{"name": "GITHUB_TOKEN",
				"valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "agent-github-pat-token", "key": "token"}}})
		},
		"privileged":    func(m map[string]any) { container(m)["securityContext"] = map[string]any{"privileged": true} },
		"other command": func(m map[string]any) { container(m)["command"] = []any{"/app/prow-job.sh"} },
		"extra refs": func(m map[string]any) {
			m["spec"].(map[string]any)["extra_refs"] = []any{map[string]any{"org": "aws-controllers-k8s", "repo": "test-infra", "base_ref": "main"}}
		},
		"run id": func(m map[string]any) { m["metadata"].(map[string]any)["name"] = "autopilot-00000000000000ff-g1-r1" },
	} {
		assert.Error(t, rawCreate(t, k, pj, next(), edit), name)
	}
}

// TestKindAdmitsThePlan9Jobs: add-resource has add-field's shape, and the dod
// job holds no secret, no volume and no other repo (Plan 9).
func TestKindAdmitsThePlan9Jobs(t *testing.T) {
	ctx := context.Background()
	k := kindKube(t, Namespace)
	cat := testCatalog(t)
	l, err := New(cat, testConfig, k, &fakeArtifacts{})
	require.NoError(t, err)
	// goldenAddResource shares add-field's launch id, which
	// TestKindRBACAndAdmissionPolicy has already created: use generation 2.
	addResource := goldenAddResource
	addResource.LaunchID = "autopilot-0123456789abcdef-g2-r1"
	require.NoError(t, l.Launch(ctx, addResource))
	require.NoError(t, l.Launch(ctx, goldenDod))
	require.NoError(t, l.Abort(ctx, addResource.LaunchID))
	require.NoError(t, l.Abort(ctx, goldenDod.LaunchID))

	podSpec := func(m map[string]any) map[string]any {
		return m["spec"].(map[string]any)["pod_spec"].(map[string]any)
	}
	container := func(m map[string]any) map[string]any { return podSpec(m)["containers"].([]any)[0].(map[string]any) }
	dod, err := cat.Build(testConfig, goldenDod)
	require.NoError(t, err)
	n := 200
	nextDod := func() string { n++; return fmt.Sprintf("autopilot-%016x-g1-d1", n) }
	for name, edit := range map[string]func(map[string]any){
		"a token": func(m map[string]any) {
			container(m)["env"] = append(container(m)["env"].([]any), map[string]any{"name": "GITHUB_TOKEN",
				"valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "agent-github-pat-token", "key": "token"}}})
		},
		"secrets volume": func(m map[string]any) {
			podSpec(m)["volumes"] = []any{map[string]any{"name": "agent-secrets", "csi": map[string]any{
				"driver": "secrets-store.csi.k8s.io", "volumeAttributes": map[string]any{"secretProviderClass": "agent-secrets"}}}}
			container(m)["volumeMounts"] = []any{map[string]any{"name": "agent-secrets", "mountPath": "/mnt/secrets-store"}}
		},
		"privileged": func(m map[string]any) { container(m)["securityContext"] = map[string]any{"privileged": true} },
		"another SA": func(m map[string]any) { podSpec(m)["serviceAccountName"] = "periodic-service-account" },
		"other command": func(m map[string]any) {
			container(m)["command"] = []any{"/bin/bash", "-c", "./scripts/autopilot-sdk-bump.sh"}
		},
		"build cluster": func(m map[string]any) { m["spec"].(map[string]any)["cluster"] = "build" },
		"another controller": func(m map[string]any) {
			refs := m["spec"].(map[string]any)["extra_refs"].([]any)
			refs[len(refs)-1].(map[string]any)["repo"] = "iam-controller"
		},
		// Plan 9 review F14.
		"a BASH_ENV env var": func(m map[string]any) {
			container(m)["env"] = append(container(m)["env"].([]any), map[string]any{"name": "BASH_ENV", "value": "/tmp/x"})
		},
		"a ServiceAccount token": func(m map[string]any) { podSpec(m)["automountServiceAccountToken"] = true },
		"a SERVICE path": func(m map[string]any) {
			for _, e := range container(m)["env"].([]any) {
				if e.(map[string]any)["name"] == "SERVICE" {
					e.(map[string]any)["value"] = "../x"
				}
			}
		},
		"a pinned PR ref": func(m map[string]any) {
			refs := m["spec"].(map[string]any)["extra_refs"].([]any)
			refs[len(refs)-1].(map[string]any)["pulls"] = []any{map[string]any{"number": 7, "sha": "1111111111111111111111111111111111111111", "author": "x"}}
		},
	} {
		assert.Error(t, rawCreate(t, k, dod, nextDod(), edit), name)
	}
	ar, err := cat.Build(testConfig, goldenAddResource)
	require.NoError(t, err)
	assert.Error(t, rawCreate(t, k, ar, "autopilot-00000000000000ff-g1-d1", func(map[string]any) {}), "a dod id on an add-resource job")
	assert.Error(t, rawCreate(t, k, ar, "autopilot-00000000000000fe-g1-r1", func(m map[string]any) {
		container(m)["image"] = "docker.io/evil/image:latest"
	}), "add-resource with another image")
}
