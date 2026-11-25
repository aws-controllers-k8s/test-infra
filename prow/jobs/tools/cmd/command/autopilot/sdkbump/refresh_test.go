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
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

func sectionOf(body, name string) string {
	i, j := strings.Index(body, sectionBegin(name)), strings.Index(body, sectionEnd(name))
	if i < 0 || j < i {
		return ""
	}
	return body[i:j]
}

func refreshFixture(t *testing.T) (*bumpFixture, Report) {
	f := newBumpFixture(t)
	first := Run(context.Background(), f.options())
	require.Equal(t, OutcomePROpened, first.Outcome)
	f.forge.PRs[testRepo][0].HeadSHA = first.Head
	f.fresh()
	return f, first
}

func TestRefreshClearsTheBlockWhenADependentCloses(t *testing.T) {
	f, first := refreshFixture(t)
	f.saveLedger(bumpItem(ledger.HumanReview), dependent("Bucket", "logging.prefix", ServerDefaultWork, ledger.WontFix))
	o := f.options()
	o.Mode, o.CodegenRepo, o.RuntimeRepo = ModeRefresh, "", ""
	builds := f.builds
	rep := Run(context.Background(), o)
	assert.Equal(t, Result{Outcome: OutcomePROpened, Reason: "REFRESHED", PR: first.PR}, rep.Result)
	assert.Equal(t, builds, f.builds, "refresh never regenerates")
	pr := f.forge.PRs[testRepo][0]
	assert.False(t, pr.Draft)
	assert.Contains(t, pr.Body, "- [x] `Bucket` `logging.prefix`")
	assert.NotContains(t, pr.Labels, LabelBlockedServerDefault)
	assert.Equal(t, "success", f.status(first.Head, ContextServerDefault))
	assert.Equal(t, sectionOf(first.Body, sectionGate), sectionOf(pr.Body, sectionGate), "the gate section is untouched")
}

func TestRefreshStops(t *testing.T) {
	for name, c := range map[string]struct {
		setup  func(f *bumpFixture)
		result string
	}{
		"no open PR":      {func(f *bumpFixture) { f.forge.PRs[testRepo][0].State = "closed"; f.forge.PRs[testRepo][0].Body = "x" }, "completed_no_change NO_OPEN_PR"},
		"branch moved":    {func(f *bumpFixture) { f.forge.PRs[testRepo][0].HeadSHA = sha }, "failed BRANCH_MOVED"},
		"markers removed": {func(f *bumpFixture) { f.forge.PRs[testRepo][0].Body = "edited by a human" }, "needs_human BODY_MARKERS_MISSING"},
	} {
		f, _ := refreshFixture(t)
		c.setup(f)
		o := f.options()
		o.Mode = ModeRefresh
		rep := Run(context.Background(), o)
		assert.Equal(t, c.result, rep.Outcome+" "+rep.Reason, name)
	}
}
