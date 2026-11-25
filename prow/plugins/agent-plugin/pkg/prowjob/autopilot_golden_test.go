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
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/prow/pkg/github"
)

// autopilotTestdata holds the fixture and golden file ACK Autopilot's own
// ProwJob builder (prow/jobs/tools/cmd/command/autopilot/prowlaunch) is
// tested against. This test proves the golden file is what this generator
// builds; prowlaunch's TestAddFieldMatchesAgentPlugin proves its builder
// matches it. Regenerate with AUTOPILOT_GOLDEN_UPDATE=1.
var autopilotTestdata = filepath.Join("..", "..", "..", "..", "jobs", "tools", "cmd", "command", "autopilot", "prowlaunch", "testdata")

// autopilotNormalize is prowlaunch's normalize (golden_test.go): no name, no
// timestamps, args and env sorted, ack-autopilot/ keys removed.
func autopilotNormalize(t *testing.T, b []byte) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	md := m["metadata"].(map[string]any)
	delete(md, "name")
	if v, ok := md["creationTimestamp"]; ok && v == nil {
		delete(md, "creationTimestamp")
	}
	for _, k := range []string{"labels", "annotations"} {
		mm, ok := md[k].(map[string]any)
		if !ok {
			continue
		}
		for key := range mm {
			if strings.HasPrefix(key, "ack-autopilot/") {
				delete(mm, key)
			}
		}
		if a, ok := mm["workflow-agent/command-args"].(string); ok {
			parts := strings.Split(a, ",")
			sort.Strings(parts)
			mm["workflow-agent/command-args"] = strings.Join(parts, ",")
		}
	}
	if st, ok := m["status"].(map[string]any); ok {
		delete(st, "startTime")
	}
	c := m["spec"].(map[string]any)["pod_spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	if args, ok := c["args"].([]any); ok {
		type pair struct{ k, v any }
		var ps []pair
		for i := 0; i+1 < len(args); i += 2 {
			ps = append(ps, pair{args[i], args[i+1]})
		}
		sort.Slice(ps, func(i, j int) bool { return ps[i].k.(string) < ps[j].k.(string) })
		out := make([]any, 0, len(args))
		for _, p := range ps {
			out = append(out, p.k, p.v)
		}
		c["args"] = out
	}
	if env, ok := c["env"].([]any); ok {
		sort.SliceStable(env, func(i, j int) bool {
			return env[i].(map[string]any)["name"].(string) < env[j].(map[string]any)["name"].(string)
		})
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

// autopilotCases are the agent-plugin workflows the Dispatcher builds jobs for
// itself: add-field (Plan 7) and add-resource (Plan 9).
var autopilotCases = []struct {
	workflow, golden string
	args             map[string]string
}{
	{"add-field", "prowjob-add-field.golden.json", map[string]string{
		"service": "s3", "resource": "Bucket", "field": "accelerateConfiguration.status",
		"work-item": "sha256:0123456789abcdef", "change-class": "config-only", "detector-work": "server-default",
	}},
	{"add-resource", "prowjob-add-resource.golden.json", map[string]string{
		"service": "s3", "resource": "Widget",
		"work-item": "sha256:0123456789abcdef", "change-class": "new-resource",
	}},
}

func TestAutopilotGolden(t *testing.T) {
	gen := NewGenerator(NewWorkflowConfigLoader(filepath.Join(autopilotTestdata, "agent-workflows.yaml")))
	issue := github.Issue{Number: 0, User: github.User{Login: "ack-autopilot"}}
	repo := github.Repo{Owner: github.User{Login: "aws-controllers-k8s"}, Name: "s3-controller"}
	for _, c := range autopilotCases {
		t.Run(c.workflow, func(t *testing.T) {
			pj, err := gen.CreateWorkflowProwJob(c.workflow, c.args, nil, issue, repo, "5h", "prow", "s3://example-prow-logs")
			if err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(pj)
			if err != nil {
				t.Fatal(err)
			}
			got := autopilotNormalize(t, b)
			path := filepath.Join(autopilotTestdata, c.golden)
			if os.Getenv("AUTOPILOT_GOLDEN_UPDATE") == "1" {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Errorf("agent-plugin's %s job differs from %s:\n%s", c.workflow, path, got)
			}
		})
	}
}
