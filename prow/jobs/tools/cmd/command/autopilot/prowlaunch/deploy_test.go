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
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dispatcher"
)

// deployDir holds the manifests a human applies (Plan 7); these tests read
// them as files and never talk to a cluster.
var deployDir = filepath.Join("..", "..", "..", "..", "..", "..", "autopilot", "deploy")

func docs(t *testing.T, name string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(deployDir, name))
	require.NoError(t, err)
	dec := yaml.NewDecoder(bytes.NewReader(b))
	var out []map[string]any
	for {
		var m map[string]any
		err := dec.Decode(&m)
		if errors.Is(err, io.EOF) {
			return out
		}
		require.NoError(t, err)
		out = append(out, m)
	}
}

func TestRoleIsProwJobsOnly(t *testing.T) {
	d := docs(t, "rbac.yaml")
	require.Len(t, d, 2)
	role, binding := d[0], d[1]
	assert.Equal(t, "Role", role["kind"], "namespaced, never a ClusterRole")
	assert.Equal(t, "prow", role["metadata"].(map[string]any)["namespace"])
	assert.Equal(t, []any{map[string]any{
		"apiGroups": []any{"prow.k8s.io"},
		"resources": []any{"prowjobs"},
		"verbs":     []any{"create", "get", "list", "patch"},
	}}, role["rules"])

	assert.Equal(t, "RoleBinding", binding["kind"])
	assert.Equal(t, "prow", binding["metadata"].(map[string]any)["namespace"])
	assert.Equal(t, map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": "autopilot-dispatcher"}, binding["roleRef"])
	assert.Equal(t, []any{map[string]any{"kind": "ServiceAccount", "name": "autopilot-dispatcher", "namespace": "test-pods"}}, binding["subjects"])

	sa := docs(t, "serviceaccount.yaml")
	require.Len(t, sa, 1)
	assert.Equal(t, map[string]any{"name": "autopilot-dispatcher", "namespace": "test-pods",
		"labels": map[string]any{"app.kubernetes.io/part-of": "ack-autopilot"}}, sa[0]["metadata"])
}

func TestAdmissionPolicyCoversTheAllowlist(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(deployDir, "admission-policy.yaml"))
	require.NoError(t, err)
	s := string(b)
	for _, job := range Allowed {
		assert.Contains(t, s, "'"+job+"'", "every allowlisted job has its pod rule")
	}
	assert.Contains(t, s, "system:serviceaccount:test-pods:autopilot-dispatcher")
	m := regexp.MustCompile(`name\.matches\('([^']+)'\)`).FindStringSubmatch(s)
	require.NotNil(t, m, "the policy checks the job name")
	nameRule := regexp.MustCompile(m[1])
	for _, id := range []string{
		"autopilot-0123456789abcdef-g1-r1", "autopilot-0123456789abcdef-g12-f0123abcd",
		"autopilot-0123456789abcdef-g1-r0", "periodic-agent-add-field-1", "autopilot-0123456789abcdef-g1-r1x",
	} {
		assert.Equal(t, launchIDRE.MatchString(id), nameRule.MatchString(id), "the policy's name rule is the launcher's: %s", id)
	}
	tokens := regexp.MustCompile(`\$\{[A-Z_]+\}`).FindAllString(s, -1)
	set := map[string]bool{}
	for _, tok := range tokens {
		set[tok] = true
	}
	var got []string
	for tok := range set {
		got = append(got, tok)
	}
	sort.Strings(got)
	assert.Equal(t, []string{"${PROW_IMAGES_REPO_URI}", "${PROW_LOGS_BUCKET}", "${TEST_INFRA_ORG}"}, got)
	d := docs(t, "admission-policy.yaml")
	require.Len(t, d, 2)
	assert.Equal(t, "Fail", d[0]["spec"].(map[string]any)["failurePolicy"])
	assert.Equal(t, []any{"Deny"}, d[1]["spec"].(map[string]any)["validationActions"])
}

func TestPodIdentityIsNarrow(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(deployDir, "pod-identity.yaml"))
	require.NoError(t, err)
	s := string(b)
	assert.NotContains(t, s, `"Resource": "*"`)
	assert.NotContains(t, s, `"Action": "*"`)
	assert.NotContains(t, s, "s3:*")
	assert.NotContains(t, s, "secret:ack/prow/*")
	for _, job := range Allowed {
		assert.Contains(t, s, "/logs/"+job+"/*/artifacts/agent-result.json")
	}
}

// TestNothingDeploysThisDirectory: the manifests are applied by a human, so
// no Argo CD Application may point at them.
func TestNothingDeploysThisDirectory(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..", "..", "..", "argocd", "applications", "values.yaml"))
	require.NoError(t, err)
	for _, line := range strings.Split(string(b), "\n") {
		if p, ok := strings.CutPrefix(strings.TrimSpace(line), "path:"); ok {
			p = strings.TrimSpace(p)
			assert.False(t, p == "prow" || strings.HasPrefix("prow/autopilot", p) || strings.HasPrefix(p, "prow/autopilot"), p)
		}
	}
}

// TestAdmissionPolicyAllowsWhatBuildMakes: the policy's annotation allowlist
// names every annotation Build puts on either job (Prow copies a job's
// annotations to its pod), and its volume rule names the sdk-bump censor
// secret. A new annotation or volume in Build fails here, not at the first
// live create (Open risk 2).
func TestAdmissionPolicyAllowsWhatBuildMakes(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(deployDir, "admission-policy.yaml"))
	require.NoError(t, err)
	s := string(b)
	m := regexp.MustCompile(`(?s)annotationKeys\n\s+expression: "\[([^\]]*)\]"`).FindStringSubmatch(s)
	require.NotNil(t, m, "the policy has an annotationKeys variable")
	allowed := map[string]bool{}
	for _, k := range strings.Split(m[1], ",") {
		allowed[strings.Trim(strings.TrimSpace(k), "'")] = true
	}
	c := testCatalog(t)
	for name, req := range map[string]dispatcher.LaunchRequest{"add-field": goldenAddField, "sdk-bump": goldenSDKBump} {
		pj, err := c.Build(testConfig, req)
		require.NoError(t, err)
		for k := range pj.Metadata.Annotations {
			assert.True(t, allowed[k], "%s: annotation %s is not in the policy's annotationKeys", name, k)
		}
		for _, v := range pj.Spec.PodSpec.Volumes {
			if v.Secret != nil {
				assert.Contains(t, s, "'"+v.Name+"'", name)
				assert.Contains(t, s, "'"+v.Secret.SecretName+"'", name)
			}
		}
	}
	assert.True(t, allowed[AnnotationDoNotDisrupt])
	assert.Contains(t, s, "variables.sdkBump && object.metadata.annotations['"+AnnotationDoNotDisrupt+"'] == 'true'")
}
