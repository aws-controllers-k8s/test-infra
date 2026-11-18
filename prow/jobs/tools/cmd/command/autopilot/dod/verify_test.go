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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod/dodtest"
)

func TestVerify(t *testing.T) {
	ctx := context.Background()
	r := dodtest.NewRepo(t)
	r.Write("a.txt", "a\n")
	base := r.Commit("base")
	r.Write("a.txt", "b\n")
	head := r.Commit("head")

	v := sampleVerdict()
	v.Subject.BaseSHA, v.Subject.HeadTree = base, r.Git("rev-parse", head+"^{tree}")
	data, err := Marshal(v)
	require.NoError(t, err)

	_, err = Verify(ctx, r.Dir, head, data, Digest(data))
	require.NoError(t, err)

	// A message-only amend (adding the trailer) keeps the tree: still valid.
	r.Git("commit", "--quiet", "--amend", "-m", "head\n\n"+Trailer(data))
	_, err = Verify(ctx, r.Dir, "HEAD", data, Digest(data))
	require.NoError(t, err)

	tampered := append([]byte{}, data...)
	tampered[len(tampered)-3] = ' '
	_, err = Verify(ctx, r.Dir, "HEAD", tampered, Digest(data))
	assert.Error(t, err)

	_, err = Verify(ctx, r.Dir, base, data, Digest(data))
	assert.True(t, errors.Is(err, ErrTreeMismatch), "%v", err)

	r.Git("checkout", "--quiet", "-b", "side", base)
	r.Write("a.txt", "side\n")
	side := r.Commit("side")
	v.Subject.BaseSHA = side
	other, err := Marshal(v)
	require.NoError(t, err)
	_, err = Verify(ctx, r.Dir, head, other, Digest(other))
	assert.True(t, errors.Is(err, ErrBaseNotAncestor), "%v", err)

	_, err = Verify(ctx, r.Dir, head, data, "sha256:00")
	assert.True(t, errors.Is(err, ErrDigestMismatch), "%v", err)
}

// fakeCommits answers Commits from maps.
type fakeCommits struct {
	trees     map[string]string
	ancestors map[[2]string]bool
	err       error
}

func (f fakeCommits) TreeSHA(_ context.Context, c string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.trees[c], nil
}

func (f fakeCommits) IsAncestor(_ context.Context, base, c string) (bool, error) {
	return f.ancestors[[2]string{base, c}], nil
}

func TestVerifyWith(t *testing.T) {
	v := &Verdict{Schema: Schema, Verdict: StatusPass, Subject: Subject{BaseSHA: "base", HeadTree: "tree1"}}
	data, err := Marshal(v)
	require.NoError(t, err)
	want := Digest(data)
	good := fakeCommits{trees: map[string]string{"head": "tree1"}, ancestors: map[[2]string]bool{{"base", "head"}: true}}

	got, err := VerifyWith(context.Background(), good, "head", data, want)
	require.NoError(t, err)
	assert.Equal(t, StatusPass, got.Verdict)

	_, err = VerifyWith(context.Background(), good, "head", data, "sha256:other")
	assert.ErrorIs(t, err, ErrDigestMismatch)
	_, err = VerifyWith(context.Background(), fakeCommits{trees: map[string]string{"head": "tree2"}}, "head", data, want)
	assert.ErrorIs(t, err, ErrTreeMismatch)
	_, err = VerifyWith(context.Background(), fakeCommits{trees: map[string]string{"head": "tree1"}}, "head", data, want)
	assert.ErrorIs(t, err, ErrBaseNotAncestor)
	_, err = VerifyWith(context.Background(), fakeCommits{err: errors.New("API down")}, "head", data, want)
	assert.ErrorContains(t, err, "API down")
}
