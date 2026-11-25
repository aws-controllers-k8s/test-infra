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
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dispatcher"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

func testOptions(forge Forge) Options {
	return Options{
		Inputs: Inputs{Service: svc, SDKTo: to, WorkItem: wi, ChangeClass: "SDK-bump"},
		Mode:   ModeBump, Org: "aws-controllers-k8s",
		BotName: "ack-bot", BotEmail: botEmail, BotLogin: "ack-bot",
		Forge: forge, Log: io.Discard,
	}
}

func TestPickPR(t *testing.T) {
	o := testOptions(nil)
	mine := PR{Number: 3, URL: "u3", State: "open", BaseRef: "main", Author: "ack-bot"}
	open, r := pickPR([]PR{mine}, o)
	require.Nil(t, r)
	assert.Equal(t, 3, open.Number)

	for name, c := range map[string]struct {
		prs    []PR
		result Result
	}{
		"two open":       {[]PR{mine, mine}, Result{OutcomeNeedsHuman, "DUPLICATE_PRS", "u3"}},
		"someone else's": {[]PR{{URL: "u", State: "open", BaseRef: "main", Author: "mallory"}}, Result{OutcomeNeedsHuman, "IDENTITY_MISMATCH", "u"}},
		"other base":     {[]PR{{URL: "u", State: "open", BaseRef: "release", Author: "ack-bot"}}, Result{OutcomeNeedsHuman, "BRANCH_MISMATCH", "u"}},
		"closed, same item": {[]PR{{URL: "u", State: "closed", Body: "x\n\nWork-Item: " + wi + "\n"}},
			Result{OutcomeNeedsHuman, "PR_CLOSED_BY_HUMAN", "u"}},
		"merged, same item": {[]PR{{URL: "u", State: "closed", Merged: true, Body: "x\n\nWork-Item: " + wi}},
			Result{OutcomeNoChange, "ALREADY_MERGED", "u"}},
	} {
		open, r := pickPR(c.prs, o)
		assert.Nil(t, open, name)
		require.NotNil(t, r, name)
		assert.Equal(t, c.result, r.Result, name)
	}

	open, r = pickPR([]PR{{State: "closed", Body: "x\n\nWork-Item: sha256:ffffffffffffffff"}}, o)
	assert.Nil(t, open)
	assert.Nil(t, r, "a closed PR for an older item does not stop a newer one")
}

func TestUpsertPRCreatesOnceAndEditsOnChange(t *testing.T) {
	ctx := context.Background()
	f := NewFakeForge("ack-bot")
	o := testOptions(f)
	p, err := upsertPR(ctx, o, nil, "t", "b")
	require.NoError(t, err)
	assert.True(t, p.Draft)
	p, err = upsertPR(ctx, o, &p, "t", "b")
	require.NoError(t, err)
	p.Body = "b\r\n" // GitHub may store CRLF
	_, err = upsertPR(ctx, o, &p, "t", "b\n")
	require.NoError(t, err)
	_, err = upsertPR(ctx, o, &p, "t2", "b\n")
	require.NoError(t, err)
	assert.Equal(t, []string{"CreatePR " + testRepo + " head=autopilot/s3/sdk-bump base=main draft=true", "EditPR " + testRepo + " #1"}, f.Calls)
}

func TestApplyMergeBlock(t *testing.T) {
	ctx := context.Background()
	f := NewFakeForge("ack-bot")
	o := testOptions(f)
	pr := *f.Seed(testRepo, PR{State: "open", Draft: true, HeadRef: Branch(svc), BaseRef: "main", Author: "ack-bot"})
	blocked := Dependents{Known: true, Items: []Dependent{{Resource: "Bucket", FieldPath: "logging.prefix", State: ledger.Blocked}}}
	key := testRepo + "@" + sha + "@" + ContextServerDefault

	require.NoError(t, applyMergeBlock(ctx, o, pr, sha, true, blocked, []string{dod.LabelSensitivePath, "x/unknown"}))
	assert.Equal(t, CommitStatus{Context: ContextServerDefault, State: "failure",
		Description: "1 of 1 server-default field(s) need a decision"}, f.Statuses[key])
	got := f.PRs[testRepo][0]
	assert.ElementsMatch(t, []string{LabelAutoGen, LabelClass, LabelLevel, dod.LabelSensitivePath, LabelBlockedServerDefault}, got.Labels)
	assert.True(t, got.Draft)

	resolved := Dependents{Known: true, Items: []Dependent{{Resource: "Bucket", FieldPath: "logging.prefix", Resolved: true, How: "h"}}}
	require.NoError(t, applyMergeBlock(ctx, o, *got, sha, true, resolved, nil))
	assert.Equal(t, "success", f.Statuses[key].State)
	assert.NotContains(t, got.Labels, LabelBlockedServerDefault)
	assert.Contains(t, got.Labels, dod.LabelSensitivePath, "the block never removes other labels")
	assert.True(t, got.Draft, "gate passed and nothing blocks: still draft, the Dispatcher marks it ready (Plan 9)")
	assert.NotContains(t, strings.Join(f.Calls, "\n"), "SetDraft")

	got.Draft = false // the Dispatcher marked it ready
	require.NoError(t, applyMergeBlock(ctx, o, *got, sha, false, resolved, nil))
	assert.True(t, got.Draft, "a failed gate goes back to draft")

	require.NoError(t, applyMergeBlock(ctx, o, *got, sha, true, Dependents{}, nil))
	assert.Equal(t, CommitStatus{Context: ContextServerDefault, State: "failure",
		Description: "server-default dependents unknown: the ledger was not read"}, f.Statuses[key])
	assert.True(t, got.Draft)
}

func TestSetGateStatus(t *testing.T) {
	ctx := context.Background()
	f := NewFakeForge("ack-bot")
	o := testOptions(f)
	o.JobURL = "https://prow.example/view/1"
	digest := "sha256:0123456789abcdef0123"
	require.NoError(t, setGateStatus(ctx, o, sha, &dod.Verdict{Verdict: dod.StatusPass}, digest))
	assert.Equal(t, CommitStatus{Context: ContextGate, State: "success", Description: "ack-dod pass 0123456789ab",
		TargetURL: o.JobURL}, f.Statuses[testRepo+"@"+sha+"@"+ContextGate])
	long := make([]string, 40)
	for i := range long {
		long[i] = "path-allowlist:PATH_NOT_ALLOWED"
	}
	require.NoError(t, setGateStatus(ctx, o, sha, &dod.Verdict{Verdict: dod.StatusFail, Reasons: long}, digest))
	s := f.Statuses[testRepo+"@"+sha+"@"+ContextGate]
	assert.Equal(t, "failure", s.State)
	assert.LessOrEqual(t, len(s.Description), maxStatusDescription)
}

func TestLoadDependentsFailsClosed(t *testing.T) {
	o := testOptions(nil)
	assert.False(t, loadDependents(o, nil).Known, "no --ledger-dir")
	o.LedgerDir = t.TempDir()
	assert.False(t, loadDependents(o, nil).Known, "empty ledger")
	require.NoError(t, ledger.Save(o.LedgerDir, itemsOf(bumpItem(ledger.Queued),
		dependent("Bucket", "logging.prefix", ServerDefaultWork, ledger.Blocked))))
	d := loadDependents(o, nil)
	assert.True(t, d.Known)
	assert.Equal(t, 1, d.Unresolved())
	o.WorkItem = "sha256:ffffffffffffffff"
	assert.False(t, loadDependents(o, nil).Known, "the ledger's bump is another item")
}

// TestBlockLabelIsTheDispatchers: the Dispatcher leaves a PR draft while this
// label is on it (Plan 9).
func TestBlockLabelIsTheDispatchers(t *testing.T) {
	assert.Equal(t, dispatcher.LabelBlockedServerDefault, LabelBlockedServerDefault)
}
