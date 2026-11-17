package prowlaunch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestEnvFrom(t *testing.T) {
	good := map[string]string{
		"PROW_IMAGES_REPO_URI": "registry.example/prow", "TEST_INFRA_ORG": "aws-controllers-k8s", "AWS_REGION": "us-west-2",
		"JOB_SPEC": `{"type":"periodic","job":"autopilot-dispatch","buildid":"1","prowjobid":"x","decoration_config":{"gcs_configuration":{"bucket":"s3://example-prow-logs","path_strategy":"explicit"}}}`,
	}
	e, err := EnvFrom(envOf(good))
	require.NoError(t, err)
	assert.Equal(t, Env{Vars: testVars, Bucket: "s3://example-prow-logs", Region: "us-west-2"}, e)

	for name, edit := range map[string][2]string{
		"no job spec":      {"JOB_SPEC", ""},
		"no decoration":    {"JOB_SPEC", `{"type":"periodic"}`},
		"gcs bucket":       {"JOB_SPEC", `{"decoration_config":{"gcs_configuration":{"bucket":"gs://x"}}}`},
		"no region":        {"AWS_REGION", ""},
		"no images repo":   {"PROW_IMAGES_REPO_URI", ""},
		"unresolved token": {"PROW_IMAGES_REPO_URI", "${PROW_IMAGES_REPO_URI}"},
	} {
		m := map[string]string{}
		for k, v := range good {
			m[k] = v
		}
		m[edit[0]] = edit[1]
		_, err := EnvFrom(envOf(m))
		assert.Error(t, err, name)
	}
}
