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
	"encoding/json"
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
		"autopilot-0123456789abcdef-g1-t1", "autopilot-0123456789abcdef-g1-t0",
		"autopilot-0123456789abcdef-g1-r0", "periodic-agent-add-field-1", "autopilot-0123456789abcdef-g1-r1x",
		"autopilot-0123456789abcdef-g1-d1", "autopilot-0123456789abcdef-g1-d0",
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
	// The one "Resource": "*" is PutMetricData (Plan 10), which has no
	// resource-level permissions: TestPodIdentityMetricsAreScoped.
	assert.Equal(t, 1, strings.Count(s, `"Resource": "*"`))
	assert.NotContains(t, s, `"Action": "*"`)
	assert.NotContains(t, s, "s3:*")
	assert.NotContains(t, s, "secret:ack/prow/*")
	// Exactly the files the launcher reads (Status, Verdict).
	files := map[string][]string{
		"add-field": {"agent-result.json"}, "sdk-bump": {"agent-result.json", "verdict.json"},
		"triage": {"agent-result.json"}, "add-resource": {"agent-result.json"}, "dod": {"verdict.json", "verdict.sig"},
	}
	require.Len(t, files, len(Allowed))
	want := 0
	for wf, job := range Allowed {
		for _, f := range files[wf] {
			assert.Contains(t, s, "/logs/"+job+"/*/artifacts/"+f)
			want++
		}
	}
	assert.Equal(t, want, strings.Count(s, "/artifacts/"), "no other artifact is readable")
}

// TestPodIdentityMetricsAreScoped (Plan 10): every statement on all resources
// is cloudwatch:PutMetricData limited to namespace ACK/Autopilot.
func TestPodIdentityMetricsAreScoped(t *testing.T) {
	type statement struct {
		Effect    string
		Action    []string
		Resource  any
		Condition map[string]map[string]string
	}
	found := 0
	for _, d := range docs(t, "pod-identity.yaml") {
		spec, _ := d["spec"].(map[string]any)
		policies, _ := spec["inlinePolicies"].(map[string]any)
		for name, doc := range policies {
			var p struct{ Statement []statement }
			require.NoError(t, json.Unmarshal([]byte(doc.(string)), &p), name)
			for _, st := range p.Statement {
				if st.Resource != "*" {
					continue
				}
				found++
				assert.Equal(t, "Allow", st.Effect, name)
				assert.Equal(t, []string{"cloudwatch:PutMetricData"}, st.Action, name)
				assert.Equal(t, map[string]map[string]string{"StringEquals": {"cloudwatch:namespace": "ACK/Autopilot"}}, st.Condition, name)
			}
		}
	}
	assert.Equal(t, 1, found)
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
	for name, req := range map[string]dispatcher.LaunchRequest{
		"add-field": goldenAddField, "sdk-bump": goldenSDKBump, "add-resource": goldenAddResource, "dod": goldenDod,
	} {
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
	assert.Contains(t, s, "(variables.sdkBump || variables.dod) && object.metadata.annotations['"+AnnotationDoNotDisrupt+"'] == 'true'")
}

// TestAdmissionPolicyDodEnvIsBuilds (Plan 9 review F14): the policy's dod
// rule lists exactly the environment variables Build gives the dod job, and
// requires no ServiceAccount token and a service alias for SERVICE.
func TestAdmissionPolicyDodEnvIsBuilds(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(deployDir, "admission-policy.yaml"))
	require.NoError(t, err)
	s := string(b)
	m := regexp.MustCompile(`(?s)name: dodEnv\n\s+expression: "\[([^\]]*)\]"`).FindStringSubmatch(s)
	require.NotNil(t, m, "the policy has a dodEnv variable")
	var allowed []string
	for _, k := range strings.Split(m[1], ",") {
		allowed = append(allowed, strings.Trim(strings.TrimSpace(k), "'"))
	}
	sort.Strings(allowed)
	pj, err := testCatalog(t).Build(testConfig, goldenDod)
	require.NoError(t, err)
	var got []string
	for _, e := range pj.Spec.PodSpec.Containers[0].Env {
		got = append(got, e.Name)
	}
	sort.Strings(got)
	assert.Equal(t, got, allowed, "every env name Build gives the dod job, and no other")
	assert.Contains(t, s, "variables.container.env.all(e, e.name in variables.dodEnv)")
	assert.Contains(t, s, "has(variables.pod.automountServiceAccountToken) && variables.pod.automountServiceAccountToken == false")
	assert.Contains(t, s, `e.value.matches('^[a-z0-9][a-z0-9-]{0,62}$')`)
}

// dodRule is the admission policy's autopilot-dod CREATE rule.
func dodRule(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(deployDir, "admission-policy.yaml"))
	require.NoError(t, err)
	m := regexp.MustCompile(`(?s)request\.operation != 'CREATE' \|\| !variables\.dod \|\|(.*?)\n    message:`).FindStringSubmatch(string(b))
	require.NotNil(t, m, "the policy has a dod rule")
	return m[1]
}

// TestAdmissionPolicyDodVolumeIsTheSigningKey (Plan 9 review): the dod job's
// only volume is its verdict signing key exactly as Build makes it (secret
// autopilot-dod-signing-key, mode 0400, no items, owner or other source),
// mounted read-only once at /etc/autopilot-dod-signing; and the general
// volume rule allows that secret for autopilot-dod only.
func TestAdmissionPolicyDodVolumeIsTheSigningKey(t *testing.T) {
	rule := dodRule(t)
	pj, err := testCatalog(t).Build(testConfig, goldenDod)
	require.NoError(t, err)
	require.Len(t, pj.Spec.PodSpec.Volumes, 1)
	v := pj.Spec.PodSpec.Volumes[0]
	vm := pj.Spec.PodSpec.Containers[0].VolumeMounts
	require.Len(t, vm, 1)
	for _, want := range []string{
		"size(variables.pod.volumes) == 1", "size(variables.container.volumeMounts) == 1",
		"variables.pod.volumes[0].name == '" + v.Name + "'",
		"variables.pod.volumes[0].secret.secretName == '" + v.Secret.SecretName + "'",
		"variables.pod.volumes[0].secret.defaultMode == 256",
		"!has(variables.pod.volumes[0].secret.items)", "!has(variables.pod.volumes[0].secret.defaultUser)",
		"variables.container.volumeMounts[0].name == '" + vm[0].Name + "'",
		"variables.container.volumeMounts[0].mountPath == '" + vm[0].MountPath + "'",
		"variables.container.volumeMounts[0].readOnly == true",
		"!has(variables.container.volumeMounts[0].subPath)",
	} {
		assert.Contains(t, rule, want)
	}
	assert.Equal(t, int32(256), *v.Secret.DefaultMode)
	assert.Contains(t, rule, "'autopilot-dod-signing-key'")
	assert.NotContains(t, rule, "!has(variables.pod.volumes) && !has(variables.container.volumeMounts)")
	assert.Contains(t, rule, "!has(variables.container.env) || variables.container.env.all(e, !has(e.valueFrom))", "still no env valueFrom")

	// No other volume source: every source the ProwJob CRD knows but secret.
	root := filepath.Join(deployDir, "..", "..", "..")
	b, err := os.ReadFile(filepath.Join(root, "flux", "prow", "crds", "prowjob_customresourcedefinition.yaml"))
	require.NoError(t, err)
	var crd struct {
		Spec struct {
			Versions []struct {
				Schema struct {
					OpenAPIV3Schema struct {
						Properties struct {
							Spec struct {
								Properties struct {
									PodSpec struct {
										Properties struct {
											Volumes struct {
												Items struct {
													Properties map[string]any `yaml:"properties"`
												} `yaml:"items"`
											} `yaml:"volumes"`
										} `yaml:"properties"`
									} `yaml:"pod_spec"`
								} `yaml:"properties"`
							} `yaml:"spec"`
						} `yaml:"properties"`
					} `yaml:"openAPIV3Schema"`
				} `yaml:"schema"`
			} `yaml:"versions"`
		} `yaml:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(b, &crd))
	require.NotEmpty(t, crd.Spec.Versions)
	sources := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties.Spec.Properties.PodSpec.Properties.Volumes.Items.Properties
	require.Contains(t, sources, "hostPath")
	for src := range sources {
		if src == "name" || src == "secret" {
			continue
		}
		assert.Contains(t, rule, "!has(variables.pod.volumes[0]."+src+")", "the dod volume has no %s source", src)
	}

	pol, err := os.ReadFile(filepath.Join(deployDir, "admission-policy.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(pol), "(variables.dod && v.name == 'autopilot-dod-signing' && has(v.secret) &&\n         v.secret.secretName == 'autopilot-dod-signing-key')")
}
