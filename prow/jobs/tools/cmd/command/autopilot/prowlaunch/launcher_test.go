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
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dispatcher"
)

type fakeArtifacts struct {
	objects map[string][]byte
	err     error
	keys    []string
}

func (f *fakeArtifacts) Get(_ context.Context, key string, max int64) ([]byte, error) {
	f.keys = append(f.keys, key)
	if f.err != nil {
		return nil, f.err
	}
	b, ok := f.objects[key]
	if !ok {
		return nil, ErrNotFound
	}
	if int64(len(b)) > max {
		return nil, ErrTooLarge
	}
	return b, nil
}

func testLauncher(t *testing.T) (*Launcher, *fakeAPI, *fakeArtifacts) {
	t.Helper()
	api := newFakeAPI("tok")
	k, _ := testKube(t, api, "tok")
	art := &fakeArtifacts{objects: map[string][]byte{}}
	l, err := New(testCatalog(t), testConfig, k, art)
	require.NoError(t, err)
	return l, api, art
}

// finish sets a job's state and build id, as Prow would.
func finish(api *fakeAPI, name, state, buildID string) {
	st := api.jobs[name]["status"].(map[string]any)
	st["state"], st["build_id"] = state, buildID
}

// resultFor is the add-field result fixture rewritten for one job.
func resultFor(t *testing.T, outcome, prowJobID, workItem string) []byte {
	t.Helper()
	b, err := os.ReadFile("../dispatcher/testdata/agent-result-v1.json")
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	m["outcome"], m["prow_job_id"], m["work_item"] = outcome, prowJobID, workItem
	out, err := json.Marshal(m)
	require.NoError(t, err)
	return out
}

const addFieldKey = "logs/agent-workflow-add-field/77/artifacts/agent-result.json"

func TestLaunchIsIdempotentAndAdoptsOnlyItsOwnJob(t *testing.T) {
	l, api, _ := testLauncher(t)
	ctx := context.Background()
	require.NoError(t, l.Launch(ctx, goldenAddField))
	require.NoError(t, l.Launch(ctx, goldenAddField), "a repeated launch adopts")
	assert.Len(t, api.jobs, 1)

	other := withArgs(goldenAddField, func(a map[string]string) { a["field"] = "other" })
	assert.ErrorContains(t, l.Launch(ctx, other), "not made from this request")

	api.jobs[goldenAddField.LaunchID]["metadata"].(map[string]any)["labels"].(map[string]any)[LabelManagedBy] = "someone"
	assert.ErrorContains(t, l.Launch(ctx, goldenAddField), "not made from this request")

	bad := goldenAddField
	bad.Workflow = "add-resource"
	assert.ErrorContains(t, l.Launch(ctx, bad), "allowlist")
	assert.Len(t, api.jobs, 1, "nothing created for a refused request")
}

func TestStatusMapsStatesAndCrossChecksTheResult(t *testing.T) {
	id := goldenAddField.LaunchID
	wi := goldenAddField.Args["work-item"]
	for name, tc := range map[string]struct {
		state, buildID string
		object         []byte
		want           dispatcher.Status
	}{
		"triggered":        {"triggered", "", nil, dispatcher.Status{Phase: dispatcher.PhasePending}},
		"pending":          {"pending", "77", nil, dispatcher.Status{Phase: dispatcher.PhaseRunning}},
		"aborted":          {"aborted", "77", nil, dispatcher.Status{Phase: dispatcher.PhaseFinished, Job: dispatcher.JobAborted}},
		"error":            {"error", "", nil, dispatcher.Status{Phase: dispatcher.PhaseFinished, Job: dispatcher.JobErrored}},
		"strange state":    {"exploded", "77", nil, dispatcher.Status{Phase: dispatcher.PhaseUnknown}},
		"no build id":      {"success", "", nil, dispatcher.Status{Phase: dispatcher.PhaseFinished, Job: dispatcher.JobSucceeded, Problem: "RESULT_UNLOCATABLE"}},
		"no result file":   {"failure", "77", nil, dispatcher.Status{Phase: dispatcher.PhaseFinished, Job: dispatcher.JobFailed, Problem: "RESULT_MISSING"}},
		"too large":        {"success", "77", []byte(strings.Repeat(" ", dispatcher.MaxResultBytes+1)), dispatcher.Status{Phase: dispatcher.PhaseFinished, Job: dispatcher.JobSucceeded, Problem: "RESULT_TOO_LARGE"}},
		"not json":         {"success", "77", []byte("{"), dispatcher.Status{Phase: dispatcher.PhaseFinished, Job: dispatcher.JobSucceeded, Problem: "RESULT_INVALID"}},
		"other work item":  {"success", "77", resultFor(t, "pr_opened", id, "sha256:ffffffffffffffff"), dispatcher.Status{Phase: dispatcher.PhaseFinished, Job: dispatcher.JobSucceeded, Problem: "RESULT_INVALID"}},
		"other job's file": {"success", "77", resultFor(t, "pr_opened", "autopilot-0123456789abcdef-g1-r2", wi), dispatcher.Status{Phase: dispatcher.PhaseFinished, Job: dispatcher.JobSucceeded, Problem: "RESULT_FOR_OTHER_JOB"}},
	} {
		t.Run(name, func(t *testing.T) {
			l, api, art := testLauncher(t)
			require.NoError(t, l.Launch(context.Background(), goldenAddField))
			finish(api, id, tc.state, tc.buildID)
			if tc.object != nil {
				art.objects[addFieldKey] = tc.object
			}
			st, err := l.Status(context.Background(), id)
			require.NoError(t, err)
			assert.Equal(t, tc.want, st)
		})
	}

	l, api, art := testLauncher(t)
	require.NoError(t, l.Launch(context.Background(), goldenAddField))
	finish(api, id, "success", "77")
	art.objects[addFieldKey] = resultFor(t, "pr_opened", id, wi)
	st, err := l.Status(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, st.Result)
	assert.Equal(t, "pr_opened", st.Result.Outcome)
	assert.Equal(t, dispatcher.JobSucceeded, st.Job)
	assert.Equal(t, []string{addFieldKey}, art.keys)

	st, err = l.Status(context.Background(), "autopilot-ffffffffffffffff-g1-r1")
	require.NoError(t, err)
	assert.Equal(t, dispatcher.PhaseUnknown, st.Phase, "a missing job is unknown")
}

func TestStatusNeverReadsAForeignJob(t *testing.T) {
	l, api, art := testLauncher(t)
	require.NoError(t, l.Launch(context.Background(), goldenAddField))
	id := goldenAddField.LaunchID
	finish(api, id, "success", "77")
	delete(api.jobs[id]["metadata"].(map[string]any)["labels"].(map[string]any), LabelManagedBy)
	st, err := l.Status(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, dispatcher.Status{Phase: dispatcher.PhaseUnknown}, st)
	assert.Empty(t, art.keys)
}

func TestAbortIsIdempotentAndOnlyForOwnJobs(t *testing.T) {
	l, api, _ := testLauncher(t)
	ctx := context.Background()
	id := goldenAddField.LaunchID
	require.NoError(t, l.Abort(ctx, id), "no job: nothing to do")
	require.NoError(t, l.Launch(ctx, goldenAddField))
	require.NoError(t, l.Abort(ctx, id))
	st := api.jobs[id]["status"].(map[string]any)
	assert.Equal(t, "aborted", st["state"])
	assert.Equal(t, "Aborted by the ACK Autopilot Dispatcher", st["description"])
	n := len(api.requests)
	require.NoError(t, l.Abort(ctx, id), "already aborted")
	assert.Len(t, api.requests, n+1, "one GET, no PATCH")

	finish(api, id, "success", "77")
	require.NoError(t, l.Abort(ctx, id))
	assert.Equal(t, "success", api.jobs[id]["status"].(map[string]any)["state"], "a finished job is left alone")

	finish(api, id, "pending", "77")
	api.jobs[id]["metadata"].(map[string]any)["labels"].(map[string]any)[LabelManagedBy] = "agent-plugin"
	assert.ErrorContains(t, l.Abort(ctx, id), "not made by the Dispatcher")
	assert.Equal(t, "pending", api.jobs[id]["status"].(map[string]any)["state"])
}

func TestActiveListsUnfinishedOwnJobs(t *testing.T) {
	l, api, _ := testLauncher(t)
	ctx := context.Background()
	require.NoError(t, l.Launch(ctx, goldenAddField))
	require.NoError(t, l.Launch(ctx, goldenSDKBump))
	refresh := withArgs(goldenSDKBump, func(a map[string]string) { a["mode"] = ModeRefresh })
	refresh.LaunchID = "autopilot-0123456789abcdef-g1-f0123abcd"
	require.NoError(t, l.Launch(ctx, refresh))
	finish(api, goldenSDKBump.LaunchID, "success", "5")
	active, err := l.Active(ctx)
	require.NoError(t, err)
	assert.ElementsMatch(t, []dispatcher.ActiveJob{
		{LaunchID: goldenAddField.LaunchID, Workflow: "add-field", Service: "s3"},
		{LaunchID: refresh.LaunchID, Workflow: "sdk-bump", Service: "s3", Refresh: true},
	}, active)
}
