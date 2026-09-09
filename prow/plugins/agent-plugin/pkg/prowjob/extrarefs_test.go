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
)

// TestLoadWorkflowConfigExtraRefs verifies the extra_refs YAML round-trips
// through the loader (sigs.k8s.io/yaml uses the json tags).
func TestLoadWorkflowConfigExtraRefs(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "wf.yaml")
	content := `
workflows:
  add-resource:
    description: test
    image: repo:tag
    command: ["./prow-job.sh"]
    timeout: "45m"
    extra_refs:
      - org: aws-controllers-k8s
        repo: code-generator
        base_ref: main
        env: CODEGEN_DIR
      - org: aws-controllers-k8s
        repo: runtime
        base_ref: main
      - org: aws-controllers-k8s
        repo: ack-dev-skills
        env: ACK_DEV_SKILLS_DIR
`
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadWorkflowConfig(p)
	if err != nil {
		t.Fatalf("LoadWorkflowConfig: %v", err)
	}
	w, err := cfg.GetWorkflowByName("add-resource")
	if err != nil {
		t.Fatalf("GetWorkflowByName: %v", err)
	}
	if len(w.ExtraRefs) != 3 {
		t.Fatalf("want 3 extra_refs, got %d", len(w.ExtraRefs))
	}
	if w.ExtraRefs[0].Repo != "code-generator" || w.ExtraRefs[0].Env != "CODEGEN_DIR" {
		t.Errorf("ref[0] = %+v", w.ExtraRefs[0])
	}
	if w.ExtraRefs[1].Env != "" {
		t.Errorf("ref[1] should have no env, got %q", w.ExtraRefs[1].Env)
	}
}

// TestBuildExtraRefs verifies ref translation + path env injection, including
// the base_ref/path_alias defaults and that only refs declaring Env emit a var.
func TestBuildExtraRefs(t *testing.T) {
	refs := []ExtraRef{
		{Org: "aws-controllers-k8s", Repo: "code-generator", BaseRef: "main", Env: "CODEGEN_DIR"},
		{Org: "aws-controllers-k8s", Repo: "runtime", BaseRef: "main"},
		{Org: "aws-controllers-k8s", Repo: "ack-dev-skills", Env: "ACK_DEV_SKILLS_DIR"}, // base defaults
	}
	prowRefs, envs := buildExtraRefs(refs)

	if len(prowRefs) != 3 {
		t.Fatalf("want 3 refs, got %d", len(prowRefs))
	}
	if prowRefs[2].BaseRef != "main" {
		t.Errorf("default base_ref: got %q want main", prowRefs[2].BaseRef)
	}
	if prowRefs[0].PathAlias != "github.com/aws-controllers-k8s/code-generator" {
		t.Errorf("default path_alias: got %q", prowRefs[0].PathAlias)
	}

	if len(envs) != 2 {
		t.Fatalf("want 2 env vars (only refs with Env), got %d", len(envs))
	}
	got := map[string]string{}
	for _, e := range envs {
		got[e.Name] = e.Value
	}
	want := map[string]string{
		"CODEGEN_DIR":        "/home/prow/go/src/github.com/aws-controllers-k8s/code-generator",
		"ACK_DEV_SKILLS_DIR": "/home/prow/go/src/github.com/aws-controllers-k8s/ack-dev-skills",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("env %s: got %q want %q", k, got[k], v)
		}
	}
}

func TestBuildExtraRefsEmpty(t *testing.T) {
	r, e := buildExtraRefs(nil)
	if r != nil || e != nil {
		t.Errorf("want nil,nil for empty input; got refs=%v envs=%v", r, e)
	}
}
