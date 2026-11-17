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

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod"
)

func TestCommitBumpIsDeterministic(t *testing.T) {
	ctx := context.Background()
	r := newRepos(t)
	g := gitOps{dir: r.ctrl.Dir, run: dod.ExecRunner}
	base, err := g.revParse(ctx, "HEAD")
	require.NoError(t, err)

	r.ctrl.Write("go.mod", goMod("v1.97.3"))
	a, err := g.commitBump(ctx, base, "bump\n\nProducer: autopilot/sdk-bump", "ack-bot", botEmail)
	require.NoError(t, err)
	require.NoError(t, g.checkout(ctx, base))
	r.ctrl.Write("go.mod", goMod("v1.97.3"))
	b, err := g.commitBump(ctx, base, "bump\n\nProducer: autopilot/sdk-bump", "ack-bot", botEmail)
	require.NoError(t, err)
	assert.Equal(t, a, b, "same tree, parent, message and date give the same commit")
	assert.Equal(t, botEmail+" "+botEmail, r.ctrl.Git("show", "-s", "--format=%ae %ce", a))
}

func TestRemoteBranchAndForeignCommits(t *testing.T) {
	ctx := context.Background()
	r := newRepos(t)
	g := gitOps{dir: r.ctrl.Dir, run: dod.ExecRunner}
	base, _ := g.revParse(ctx, "HEAD")

	_, exists, err := g.remoteBranch(ctx, r.remote, "s3")
	require.NoError(t, err)
	assert.False(t, exists)

	r.ctrl.Write("go.mod", goMod("v1.97.3"))
	bot, err := g.commitBump(ctx, base, "bump", "ack-bot", botEmail)
	require.NoError(t, err)
	require.NoError(t, g.push(ctx, r.remote, "s3", bot, ""))
	head, exists, err := g.remoteBranch(ctx, r.remote, "s3")
	require.NoError(t, err)
	assert.True(t, exists)
	assert.Equal(t, bot, head)
	foreign, err := g.foreignCommits(ctx, base, head, botEmail)
	require.NoError(t, err)
	assert.Empty(t, foreign)

	r.ctrl.Write("generator.yaml", "resources: {}\n")
	human := r.ctrl.Commit("a maintainer's fix") // dodtest.Human
	require.NoError(t, g.push(ctx, r.remote, "s3", human, bot))
	head, _, err = g.remoteBranch(ctx, r.remote, "s3")
	require.NoError(t, err)
	foreign, err = g.foreignCommits(ctx, base, head, botEmail)
	require.NoError(t, err)
	assert.Equal(t, []string{human}, foreign)
}

func TestPushLeaseRefusesAMovedBranch(t *testing.T) {
	ctx := context.Background()
	r := newRepos(t)
	g := gitOps{dir: r.ctrl.Dir, run: dod.ExecRunner}
	base, _ := g.revParse(ctx, "HEAD")
	r.ctrl.Write("go.mod", goMod("v1.97.3"))
	first, _ := g.commitBump(ctx, base, "one", "ack-bot", botEmail)
	require.NoError(t, g.push(ctx, r.remote, "s3", first, ""))
	require.NoError(t, g.checkout(ctx, base))
	r.ctrl.Write("go.mod", goMod("v1.98.0"))
	second, _ := g.commitBump(ctx, base, "two", "ack-bot", botEmail)
	assert.Error(t, g.push(ctx, r.remote, "s3", second, ""), "lease says absent, branch exists")
	assert.Error(t, g.push(ctx, r.remote, "s3", second, base), "lease names the wrong old head")
	assert.NoError(t, g.push(ctx, r.remote, "s3", second, first))
}

func TestSameBumpIgnoresOnlyBuildDate(t *testing.T) {
	ctx := context.Background()
	r := newRepos(t)
	g := gitOps{dir: r.ctrl.Dir, run: dod.ExecRunner}
	base, _ := g.revParse(ctx, "HEAD")
	commit := func(date, s3 string) string {
		require.NoError(t, g.checkout(ctx, base))
		r.ctrl.Write(metaPath, genMeta(r.cgSHA, "v1.97.3", date))
		r.ctrl.Write("go.mod", goMod(s3))
		c, err := g.commitBump(ctx, base, "bump", "ack-bot", botEmail)
		require.NoError(t, err)
		return c
	}
	a := commit("2026-11-16T00:00:01Z", "v1.97.3")
	b := commit("2026-11-16T00:00:02Z", "v1.97.3")
	c := commit("2026-11-16T00:00:03Z", "v1.97.4")
	same, err := g.sameBump(ctx, base, a, b, metaPath)
	require.NoError(t, err)
	assert.True(t, same)
	same, err = g.sameBump(ctx, base, a, c, metaPath)
	require.NoError(t, err)
	assert.False(t, same)
	same, err = g.sameBump(ctx, a, b, b, metaPath)
	require.NoError(t, err)
	assert.False(t, same, "the remote head must be one commit on this base")
}

func TestWorktreeChecksOutARevision(t *testing.T) {
	ctx := context.Background()
	r := newRepos(t)
	dir, done, err := worktree(ctx, dod.ExecRunner, r.runtime.Dir, "v0.68.0")
	require.NoError(t, err)
	assert.FileExists(t, dir+"/config/crd/common/a.yaml")
	done()
	assert.NoDirExists(t, dir)
	_, _, err = worktree(ctx, dod.ExecRunner, r.runtime.Dir, "v9.9.9")
	assert.Error(t, err)
}

func TestPushRunsOnlyTheGuardedRefspec(t *testing.T) {
	var seen []string
	run := func(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, []byte, error) {
		seen = append(seen, name+" "+strings.Join(args, " "))
		return nil, nil, nil
	}
	g := gitOps{dir: t.TempDir(), run: run}
	require.NoError(t, g.push(context.Background(), "file:///r.git", "s3", sha, ""))
	assert.Equal(t, []string{"git push --quiet --no-verify --porcelain " +
		"--force-with-lease=refs/heads/autopilot/s3/sdk-bump: file:///r.git " + sha + ":refs/heads/autopilot/s3/sdk-bump"}, seen)
	assert.Error(t, g.push(context.Background(), "file:///r.git", "../main", sha, ""))
	assert.Len(t, seen, 1, "a refused refspec runs nothing")
}
