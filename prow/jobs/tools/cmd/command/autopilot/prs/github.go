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

// Package prs is the Dispatcher's GitHub pull-request access (Plan 9): a
// read-only dispatcher.PRClient over go-github, and the one write it makes,
// marking a gated draft ready for review.
package prs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/go-github/v63/github"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dispatcher"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/sdkbump"
)

// Bounds on what one tick reads per PR.
const (
	maxBodyBytes = 64 << 10
	perPage      = 100
	// maxPages bounds every list; GitHub returns at most 250 commits of a
	// PR anyway.
	maxPages = 5
)

// Reader is the read-only dispatcher.PRClient.
type Reader struct {
	Client *github.Client
}

var _ dispatcher.PRClient = (*Reader)(nil)

func split(repo string) (string, string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", fmt.Errorf("repo %q is not <owner>/<name>", repo)
	}
	return owner, name, nil
}

func ts(t *github.Timestamp) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func toPR(p *github.PullRequest) dispatcher.PR {
	body := p.GetBody()
	if len(body) > maxBodyBytes {
		// The Gate-Verdict trailer is at the end.
		body = body[len(body)-maxBodyBytes:]
	}
	out := dispatcher.PR{
		Number: p.GetNumber(), State: p.GetState(), Merged: p.MergedAt != nil, Draft: p.GetDraft(),
		Author: p.GetUser().GetLogin(), HeadSHA: p.GetHead().GetSHA(), HeadRef: p.GetHead().GetRef(),
		BaseSHA: p.GetBase().GetSHA(), Body: body,
		CreatedAt: ts(p.CreatedAt), MergedAt: ts(p.MergedAt), ClosedAt: ts(p.ClosedAt), MergeSHA: p.GetMergeCommitSHA(),
	}
	if !out.Merged {
		out.MergeSHA = ""
	}
	for _, l := range p.Labels {
		out.Labels = append(out.Labels, l.GetName())
	}
	return out
}

func (r *Reader) PR(ctx context.Context, repo string, number int) (dispatcher.PR, error) {
	owner, name, err := split(repo)
	if err != nil {
		return dispatcher.PR{}, err
	}
	p, _, err := r.Client.PullRequests.Get(ctx, owner, name, number)
	if err != nil {
		return dispatcher.PR{}, err
	}
	return toPR(p), nil
}

func (r *Reader) Commits(ctx context.Context, repo string, number int) ([]dispatcher.PRCommit, error) {
	owner, name, err := split(repo)
	if err != nil {
		return nil, err
	}
	var out []dispatcher.PRCommit
	opt := &github.ListOptions{PerPage: perPage}
	for page := 0; page < maxPages; page++ {
		cs, resp, err := r.Client.PullRequests.ListCommits(ctx, owner, name, number, opt)
		if err != nil {
			return nil, err
		}
		for _, c := range cs {
			out = append(out, dispatcher.PRCommit{SHA: c.GetSHA(), Author: c.GetAuthor().GetLogin(),
				Date: ts(c.GetCommit().GetCommitter().Date)})
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		opt.Page = resp.NextPage
	}
	return nil, fmt.Errorf("%s#%d has more than %d commits", repo, number, maxPages*perPage)
}

func (r *Reader) Reviews(ctx context.Context, repo string, number int) ([]dispatcher.PRReview, error) {
	owner, name, err := split(repo)
	if err != nil {
		return nil, err
	}
	var out []dispatcher.PRReview
	opt := &github.ListOptions{PerPage: perPage}
	for page := 0; page < maxPages; page++ {
		rs, resp, err := r.Client.PullRequests.ListReviews(ctx, owner, name, number, opt)
		if err != nil {
			return nil, err
		}
		for _, rv := range rs {
			out = append(out, dispatcher.PRReview{ID: rv.GetID(), Author: rv.GetUser().GetLogin(),
				State: rv.GetState(), SubmittedAt: ts(rv.SubmittedAt)})
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		opt.Page = resp.NextPage
	}
	// Fail closed: an uncounted review could hide a change request.
	return nil, fmt.Errorf("%s#%d has more than %d reviews", repo, number, maxPages*perPage)
}

func (r *Reader) TreeSHA(ctx context.Context, repo, commit string) (string, error) {
	owner, name, err := split(repo)
	if err != nil {
		return "", err
	}
	c, _, err := r.Client.Git.GetCommit(ctx, owner, name, commit)
	if unknownCommit(err) {
		// Plan 9 review F4: a tree mismatch, so the gate env-retries.
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return c.GetTree().GetSHA(), nil
}

// IsAncestor compares base...commit: "ahead" or "identical" means base is
// an ancestor of commit.
func (r *Reader) IsAncestor(ctx context.Context, repo, base, commit string) (bool, error) {
	owner, name, err := split(repo)
	if err != nil {
		return false, err
	}
	c, _, err := r.Client.Repositories.CompareCommits(ctx, owner, name, base, commit, &github.ListOptions{PerPage: 1})
	if unknownCommit(err) {
		// Plan 9 review F4: an unknown base or commit is not an ancestor.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	s := c.GetStatus()
	return s == "ahead" || s == "identical", nil
}

// unknownCommit reports whether GitHub answered 404 or 422: it does not know
// the commit (or the two commits share no history).
func unknownCommit(err error) bool {
	var ge *github.ErrorResponse
	if !errors.As(err, &ge) || ge.Response == nil {
		return false
	}
	return ge.Response.StatusCode == http.StatusNotFound || ge.Response.StatusCode == http.StatusUnprocessableEntity
}

func (r *Reader) OpenPRs(ctx context.Context, repo string) ([]dispatcher.PR, error) {
	owner, name, err := split(repo)
	if err != nil {
		return nil, err
	}
	var out []dispatcher.PR
	opt := &github.PullRequestListOptions{State: "open", ListOptions: github.ListOptions{PerPage: perPage}}
	for page := 0; page < maxPages; page++ {
		ps, resp, err := r.Client.PullRequests.List(ctx, owner, name, opt)
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			out = append(out, toPR(p))
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		opt.Page = resp.NextPage
	}
	return nil, fmt.Errorf("%s has more than %d open PRs", repo, maxPages*perPage)
}

// Writer is the dispatcher.PRWriter: the Dispatcher's only write to a
// controller repo. It uses sdkbump's GraphQL ready mutation.
type Writer struct {
	Forge *sdkbump.GitHubForge
}

var _ dispatcher.PRWriter = (*Writer)(nil)

func (w *Writer) MarkReady(ctx context.Context, repo string, number int) error {
	return w.Forge.SetDraft(ctx, repo, number, false)
}
