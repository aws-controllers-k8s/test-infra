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

package sdkbump

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dispatcher"
)

const goldenWI = "sha256:0123456789abcdef"

var goldenResult = Result{Outcome: OutcomePROpened, PR: "https://github.com/aws-controllers-k8s/s3-controller/pull/241"}

func TestResultMatchesTheSharedGolden(t *testing.T) {
	want, err := os.ReadFile("testdata/agent-result-sdk-bump-v1.json")
	require.NoError(t, err)
	got, err := marshalResult(goldenWI, "autopilot-0123456789abcdef-g1-r1", goldenResult)
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got))

	rr, err := dispatcher.ParseResult(got, goldenWI)
	require.NoError(t, err)
	assert.Equal(t, dispatcher.RunResult{Outcome: "pr_opened", PR: goldenResult.PR}, rr)
}

func TestWriteResultReplacesTheFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "out", "agent-result.json")
	require.NoError(t, WriteResult(p, "", "", Result{Outcome: OutcomeFailed, Reason: "JOB_INCOMPLETE"}))
	require.NoError(t, WriteResult(p, goldenWI, "", Result{Outcome: OutcomeNeedsHuman, Reason: "DRY_RUN_NO_WRITES"}))
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	rr, err := dispatcher.ParseResult(b, goldenWI)
	require.NoError(t, err)
	assert.Equal(t, "DRY_RUN_NO_WRITES", rr.Reason)
	fi, err := os.Stat(p)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), fi.Mode().Perm())
}

func TestMarshalResultRejects(t *testing.T) {
	for name, c := range map[string]struct {
		wi, job string
		r       Result
	}{
		"pr_updated":         {r: Result{Outcome: "pr_updated", PR: goldenResult.PR}},
		"wont_fix":           {r: Result{Outcome: "wont_fix:not_real"}},
		"pr_opened, no pr":   {r: Result{Outcome: OutcomePROpened}},
		"reason":             {r: Result{Outcome: OutcomeFailed, Reason: "not snake"}},
		"pr in another host": {r: Result{Outcome: OutcomePROpened, PR: "https://example.com/a/b/pull/1"}},
		"work item":          {wi: "sha256:xyz", r: Result{Outcome: OutcomeFailed}},
		"job id":             {job: "a b", r: Result{Outcome: OutcomeFailed}},
	} {
		_, err := marshalResult(c.wi, c.job, c.r)
		assert.Error(t, err, name)
	}
}
