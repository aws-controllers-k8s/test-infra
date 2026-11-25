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

package dod

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod/dodtest"
)

func TestGatherAndCommits(t *testing.T) {
	ctx := context.Background()
	r := dodtest.NewRepo(t)
	r.Write("a.txt", "one\n")
	r.Write("c.txt", "gone\n")
	r.Write("pkg/resource/x/sdk.go", dodtest.GoHeader+"package x\n")
	base := r.Commit("base")
	r.Write("a.txt", "one\n+++ looks like a header\ntwo\n")
	r.Write("b.txt", "new\n")
	r.Remove("c.txt")
	r.Write("pkg/resource/x/sdk.go", dodtest.GoHeader+"package x\n\nvar V = 1\n")
	head := r.CommitAs(dodtest.Bot, "bump\n\nProducer: autopilot/sdk-bump\nWork-Item: sha256:0123456789abcdef")

	g := gitRepo{dir: r.Dir}
	mb, err := g.mergeBase(ctx, base, head)
	require.NoError(t, err)
	assert.Equal(t, base, mb)

	files, err := gather(ctx, g, base, head)
	require.NoError(t, err)
	require.Len(t, files, 4)
	byPath := map[string]changedFile{}
	for _, f := range files {
		byPath[f.Path] = f
	}
	assert.Equal(t, byte('M'), byPath["a.txt"].Status)
	assert.Equal(t, []addedLine{{N: 2, Text: "+++ looks like a header"}, {N: 3, Text: "two"}}, byPath["a.txt"].Added)
	assert.Equal(t, byte('A'), byPath["b.txt"].Status)
	assert.Nil(t, byPath["b.txt"].Base)
	assert.Equal(t, byte('D'), byPath["c.txt"].Status)
	assert.Nil(t, byPath["c.txt"].Head)
	assert.True(t, byPath["pkg/resource/x/sdk.go"].Generated)
	assert.False(t, byPath["a.txt"].Generated)

	cs, err := g.commits(ctx, base, head)
	require.NoError(t, err)
	require.Len(t, cs, 1)
	assert.Equal(t, dodtest.Bot, cs[0].AuthorEmail)
	assert.Equal(t, []string{"autopilot/sdk-bump"}, parseTrailers(cs[0].Message)["Producer"])
	assert.False(t, cs[0].Merge)

	tree, err := g.treeSHA(ctx, head)
	require.NoError(t, err)
	assert.Equal(t, r.Git("rev-parse", head+"^{tree}"), tree)

	_, err = g.commitSHA(ctx, "--output=/tmp/x")
	assert.Error(t, err, "a ref is never read as an option")
}

func TestScratchCloneIsIndependent(t *testing.T) {
	ctx := context.Background()
	r := dodtest.NewRepo(t)
	r.Write("a.txt", "base\n")
	base := r.Commit("base")
	r.Write("a.txt", "head\n")
	head := r.Commit("head")

	s, cleanup, err := cloneScratch(ctx, gitRepo{dir: r.Dir}, head)
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(s.dir, "a.txt"))
	require.NoError(t, err)
	assert.Equal(t, "head\n", string(got))
	_, err = s.commitSHA(ctx, base)
	assert.NoError(t, err, "the base is reachable from the scratch clone")

	require.NoError(t, os.WriteFile(filepath.Join(s.dir, "a.txt"), []byte("dirty\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(s.dir, "new.txt"), []byte("x\n"), 0o644))
	require.NoError(t, s.reset(ctx, head))
	got, _ = os.ReadFile(filepath.Join(s.dir, "a.txt"))
	assert.Equal(t, "head\n", string(got))
	assert.NoFileExists(t, filepath.Join(s.dir, "new.txt"))

	cleanup()
	assert.NoDirExists(t, s.dir)
	src, _ := os.ReadFile(filepath.Join(r.Dir, "a.txt"))
	assert.Equal(t, "head\n", string(src), "the source checkout is untouched")
}
