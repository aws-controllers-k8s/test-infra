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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testRepo = "aws-controllers-k8s/s3-controller"

func TestFakeForge(t *testing.T) {
	ctx := context.Background()
	f := NewFakeForge("ack-bot")
	f.Seed(testRepo, PR{State: "closed", HeadRef: "other"})
	p, err := f.CreatePR(ctx, testRepo, NewPR{Title: "t", Body: "b", Head: Branch(svc), Base: "main", Draft: true})
	require.NoError(t, err)
	assert.Equal(t, 2, p.Number)
	assert.Equal(t, "https://github.com/"+testRepo+"/pull/2", p.URL)
	require.NoError(t, f.AddLabels(ctx, testRepo, 2, []string{"a", "b", "a"}))
	require.NoError(t, f.RemoveLabel(ctx, testRepo, 2, "a"))
	require.NoError(t, f.SetDraft(ctx, testRepo, 2, false))
	require.NoError(t, f.SetStatus(ctx, testRepo, sha, CommitStatus{Context: ContextGate, State: "success"}))
	prs, err := f.PRsForBranch(ctx, testRepo, Branch(svc))
	require.NoError(t, err)
	require.Len(t, prs, 1)
	assert.Equal(t, []string{"b"}, prs[0].Labels)
	assert.False(t, prs[0].Draft)
	assert.Equal(t, "ack-bot", prs[0].Author)
	st, err := f.GetStatus(ctx, testRepo, sha, ContextGate)
	require.NoError(t, err)
	assert.Equal(t, "success", st)
	assert.False(t, f.Writes())
	assert.Equal(t, "CreatePR "+testRepo+" head=autopilot/s3/sdk-bump base=main draft=true", f.Calls[0])
}

func TestDryRunForgeReadsButNeverWrites(t *testing.T) {
	ctx := context.Background()
	reader := NewFakeForge("ack-bot")
	reader.Seed(testRepo, PR{State: "open", HeadRef: Branch(svc), BaseRef: "main", Author: "ack-bot"})
	d := &DryRunForge{Reader: reader}
	prs, err := d.PRsForBranch(ctx, testRepo, Branch(svc))
	require.NoError(t, err)
	assert.Len(t, prs, 1)
	p, err := d.CreatePR(ctx, testRepo, NewPR{Title: "t", Head: Branch(svc), Base: "main", Draft: true})
	require.NoError(t, err)
	assert.Zero(t, p.Number)
	require.NoError(t, d.EditPR(ctx, testRepo, 1, "t", "b"))
	require.NoError(t, d.SetDraft(ctx, testRepo, 1, false))
	require.NoError(t, d.AddLabels(ctx, testRepo, 1, []string{LabelClass}))
	require.NoError(t, d.RemoveLabel(ctx, testRepo, 1, LabelBlockedServerDefault))
	require.NoError(t, d.SetStatus(ctx, testRepo, sha, CommitStatus{Context: ContextServerDefault, State: "failure"}))
	assert.Empty(t, reader.Calls, "no write reaches the reader")
	assert.Len(t, d.Planned, 6)
	assert.False(t, d.Writes())

	empty := &DryRunForge{}
	prs, err = empty.PRsForBranch(ctx, testRepo, Branch(svc))
	require.NoError(t, err)
	assert.Empty(t, prs)
}
