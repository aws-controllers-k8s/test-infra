// Copyright 2020 Amazon.com Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package prowjob

import (
	"os"
	"path/filepath"
	"testing"

	v1 "k8s.io/api/core/v1"
)

// TestLoadWorkflowConfigE2E verifies the `e2e` flag round-trips through the
// loader (defaults false when absent).
func TestLoadWorkflowConfigE2E(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "wf.yaml")
	content := `
workflows:
  add-resource:
    description: test
    image: repo:tag
    command: ["./prow-job.sh"]
    timeout: "45m"
    e2e: true
  no-e2e:
    description: test
    image: repo:tag
    command: ["./prow-job.sh"]
    timeout: "45m"
`
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadWorkflowConfig(p)
	if err != nil {
		t.Fatalf("LoadWorkflowConfig: %v", err)
	}
	if w, _ := cfg.GetWorkflowByName("add-resource"); !w.E2E {
		t.Errorf("add-resource: want E2E=true")
	}
	if w, _ := cfg.GetWorkflowByName("no-e2e"); w.E2E {
		t.Errorf("no-e2e: want E2E=false (default)")
	}
}

// TestApplyE2EPodSettings verifies the inlined DinD/kind provisioning: the
// container is privileged, gets the DinD/RUN_E2E env, and the pod carries the
// docker + kind volumes with matching mounts.
func TestApplyE2EPodSettings(t *testing.T) {
	podSpec := &v1.PodSpec{
		Containers: []v1.Container{{
			Name: "workflow-runner",
			Env:  []v1.EnvVar{{Name: "EXISTING", Value: "keep"}},
		}},
	}
	applyE2EPodSettings(podSpec)

	c := podSpec.Containers[0]
	if c.SecurityContext == nil || c.SecurityContext.Privileged == nil || !*c.SecurityContext.Privileged {
		t.Fatalf("container must be privileged for DinD; got %+v", c.SecurityContext)
	}

	env := map[string]string{}
	for _, e := range c.Env {
		env[e.Name] = e.Value
	}
	if env["EXISTING"] != "keep" {
		t.Errorf("existing env must be preserved, got %v", env)
	}
	for _, want := range []string{"DOCKER_IN_DOCKER_ENABLED", "RUN_E2E"} {
		if env[want] != "true" {
			t.Errorf("env %s: want true, got %q", want, env[want])
		}
	}

	// Every declared volume must have a matching mount and vice versa.
	vols := map[string]bool{}
	for _, v := range podSpec.Volumes {
		vols[v.Name] = true
	}
	mounts := map[string]string{}
	for _, m := range c.VolumeMounts {
		mounts[m.Name] = m.MountPath
	}
	wantMounts := map[string]string{
		"docker-graph": "/docker-graph",
		"docker-root":  "/var/lib/docker",
		"modules":      "/lib/modules",
		"cgroup":       "/sys/fs/cgroup",
	}
	for name, path := range wantMounts {
		if !vols[name] {
			t.Errorf("missing volume %q", name)
		}
		if mounts[name] != path {
			t.Errorf("mount %q: want %q, got %q", name, path, mounts[name])
		}
	}

	// The kind host mounts must be real hostPaths (not emptyDirs).
	for _, v := range podSpec.Volumes {
		if v.Name == "modules" || v.Name == "cgroup" {
			if v.HostPath == nil {
				t.Errorf("volume %q must be a hostPath", v.Name)
			}
		}
	}
}

// TestApplyE2EPodSettingsNilSafe guards the no-container edge case.
func TestApplyE2EPodSettingsNilSafe(t *testing.T) {
	applyE2EPodSettings(nil)
	applyE2EPodSettings(&v1.PodSpec{}) // no containers; must not panic
}
