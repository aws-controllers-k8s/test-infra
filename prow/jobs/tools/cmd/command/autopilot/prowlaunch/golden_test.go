package prowlaunch

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dispatcher"
)

var update = flag.Bool("update", false, "rewrite testdata/prowjob-sdk-bump.golden.json")

var testConfig = Config{
	Bucket: "s3://example-prow-logs", Org: "aws-controllers-k8s",
	Now: func() time.Time { return time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC) },
}

// goldenAddField is the request both builders get: the config-only lane's
// args for a server-default item (agent-plugin is given the same map).
var goldenAddField = dispatcher.LaunchRequest{
	LaunchID: "autopilot-0123456789abcdef-g1-r1",
	Workflow: "add-field",
	Args: map[string]string{
		"service": "s3", "resource": "Bucket", "field": "accelerateConfiguration.status",
		"work-item": "sha256:0123456789abcdef", "change-class": "config-only", "detector-work": "server-default",
	},
	Timeout: 5 * time.Hour,
}

var goldenSDKBump = dispatcher.LaunchRequest{
	LaunchID: "autopilot-0123456789abcdef-g1-r2",
	Workflow: "sdk-bump",
	Args: map[string]string{
		"service": "s3", "sdk-to": "service/s3/v1.97.3",
		"work-item": "sha256:0123456789abcdef", "change-class": "SDK-bump",
	},
	Timeout: 5 * time.Hour,
}

// normalize turns a ProwJob's JSON into the form both builders must agree on:
// no name and no timestamps (agent-plugin's are random and time.Now), args
// and env sorted (agent-plugin ranges over maps), the Dispatcher's own
// ack-autopilot/ labels and annotations removed. The same function is in
// prow/plugins/agent-plugin/pkg/prowjob/autopilot_golden_test.go.
func normalize(t *testing.T, b []byte) []byte {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
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
	require.NoError(t, err)
	return append(out, '\n')
}

func buildJSON(t *testing.T, req dispatcher.LaunchRequest) []byte {
	t.Helper()
	pj, err := testCatalog(t).Build(testConfig, req)
	require.NoError(t, err)
	b, err := json.Marshal(pj)
	require.NoError(t, err)
	return b
}

// TestAddFieldMatchesAgentPlugin compares with the golden file agent-plugin's
// generator writes (its autopilot_golden_test.go checks the same file).
func TestAddFieldMatchesAgentPlugin(t *testing.T) {
	want, err := os.ReadFile("testdata/prowjob-add-field.golden.json")
	require.NoError(t, err)
	assert.Equal(t, string(want), string(normalize(t, buildJSON(t, goldenAddField))))
}

func TestSDKBumpGolden(t *testing.T) {
	path := filepath.Join("testdata", "prowjob-sdk-bump.golden.json")
	got := normalize(t, buildJSON(t, goldenSDKBump))
	if *update {
		require.NoError(t, os.WriteFile(path, got, 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got))
}

func TestBuildAddsTheDispatcherLabels(t *testing.T) {
	var pj ProwJob
	require.NoError(t, json.Unmarshal(buildJSON(t, goldenAddField), &pj))
	assert.Equal(t, "autopilot-0123456789abcdef-g1-r1", pj.Metadata.Name)
	assert.Equal(t, "2026-11-17T09:00:00Z", pj.Status.StartTime)
	for k, v := range map[string]string{
		LabelManagedBy: ManagedBy, LabelKeyHash: "0123456789abcdef", LabelGeneration: "1",
		LabelRun: "r1", LabelService: "s3", LabelWorkflow: "add-field",
	} {
		assert.Equal(t, v, pj.Metadata.Labels[k], k)
	}
	assert.Equal(t, "sha256:0123456789abcdef", pj.Metadata.Annotations[AnnotationWorkItem])
	assert.Regexp(t, `^[0-9a-f]{64}$`, pj.Metadata.Annotations[AnnotationRequest])
	again := buildJSON(t, goldenAddField)
	assert.Equal(t, string(buildJSON(t, goldenAddField)), string(again), "deterministic")
}
