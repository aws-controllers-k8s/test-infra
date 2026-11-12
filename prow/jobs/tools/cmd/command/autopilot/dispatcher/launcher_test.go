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

package dispatcher

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/policy"
)

func TestShadowLauncherRecordsAndReportsUnknown(t *testing.T) {
	l, err := NewLauncher(policy.Policy{Switches: policy.Switches{Launcher: policy.LauncherShadow}})
	require.NoError(t, err)
	assert.True(t, l.Shadow())
	req := LaunchRequest{LaunchID: "autopilot-x-r1", Workflow: "sdk-bump"}
	require.NoError(t, l.Launch(context.Background(), req))
	assert.Equal(t, []LaunchRequest{req}, l.(*ShadowLauncher).Requests)
	st, err := l.Status(context.Background(), "autopilot-x-r1")
	require.NoError(t, err)
	assert.Equal(t, PhaseUnknown, st.Phase)

	_, err = NewLauncher(policy.Policy{Switches: policy.Switches{Launcher: "prowjob"}})
	assert.Error(t, err)
}

func TestLaunchIDIsStablePerRun(t *testing.T) {
	w := item("sdk-bump|s3|*|v1", "SDK-bump", ledger.Queued)
	id := launchID(w, 2)
	assert.Equal(t, "autopilot-"+w.KeyHash[len("sha256:"):]+"-g1-r2", id)
	assert.Equal(t, id, launchID(w, 2))
	assert.LessOrEqual(t, len(id), 63, "must fit a Kubernetes object name")
	reopened := *w
	reopened.Generation = 2
	assert.NotEqual(t, id, launchID(&reopened, 2))
}

func TestLaneArgs(t *testing.T) {
	bump := item("sdk-bump|s3|*|service/s3/v1.2.0", "SDK-bump", ledger.Queued)
	assert.Equal(t, map[string]string{"service": "s3", "sdk-to": "service/s3/v1.2.0", "work-item": bump.KeyHash},
		lanes["SDK-bump"].args(bump))
	field := item("api-change|s3|Bucket|spec:Mode|needs-config", "config-only", ledger.Queued)
	field.Resource, field.FieldPath = "Bucket", "Mode"
	assert.Equal(t, map[string]string{"service": "s3", "resource": "Bucket", "field": "Mode"},
		lanes["config-only"].args(field))
	assert.True(t, lanes["config-only"].LLM)
	assert.False(t, lanes["SDK-bump"].LLM)
}

func TestRunningHelperLintsClean(t *testing.T) {
	root := t.TempDir()
	w := running(item("sdk-bump|s3|*|v1", "SDK-bump", ledger.Queued), t0, 1, 3)
	require.NoError(t, ledger.Save(root, ledgerOf(w)))
	assert.Empty(t, ledger.Lint(root))
}
