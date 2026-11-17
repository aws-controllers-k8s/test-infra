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
	"fmt"
)

var (
	ErrDigestMismatch  = errors.New("the verdict's digest is not the Gate-Verdict value")
	ErrTreeMismatch    = errors.New("the verdict's head_tree is not the commit's tree")
	ErrBaseNotAncestor = errors.New("the verdict's base_sha is not an ancestor of the commit")
)

// Verify checks a Gate-Verdict value against a verdict file and a commit:
// the digest matches the exact bytes, the commit has the verdict's head tree,
// and the verdict's base is an ancestor of the commit (Plan 10 and, in Phase
// 5, the Merger).
func Verify(ctx context.Context, repoDir, commit string, data []byte, want string) (*Verdict, error) {
	v, err := ParseVerdict(data)
	if err != nil {
		return nil, err
	}
	if got := Digest(data); got != want {
		return v, fmt.Errorf("%w: file %s, trailer %s", ErrDigestMismatch, got, want)
	}
	g := gitRepo{dir: repoDir}
	tree, err := g.treeSHA(ctx, commit)
	if err != nil {
		return v, err
	}
	if tree != v.Subject.HeadTree {
		return v, fmt.Errorf("%w: %s has %s, the verdict says %s", ErrTreeMismatch, commit, tree, v.Subject.HeadTree)
	}
	if _, err := g.run(ctx, "merge-base", "--is-ancestor", "--end-of-options", v.Subject.BaseSHA, commit); err != nil {
		return v, fmt.Errorf("%w: %s", ErrBaseNotAncestor, v.Subject.BaseSHA)
	}
	return v, nil
}
