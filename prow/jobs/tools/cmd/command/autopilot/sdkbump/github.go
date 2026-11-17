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
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/google/go-github/v63/github"
)

// GitHubForge is the real Forge. It is built only for --write (or as the
// read-only Reader of a DryRunForge).
type GitHubForge struct {
	Client *github.Client
}

func (f *GitHubForge) Writes() bool { return true }

func splitRepo(repo string) (string, string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", fmt.Errorf("repo %q is not <owner>/<name>", repo)
	}
	return owner, name, nil
}

func toPR(p *github.PullRequest) PR {
	out := PR{
		Number: p.GetNumber(), URL: p.GetHTMLURL(), NodeID: p.GetNodeID(), State: p.GetState(),
		Merged: p.MergedAt != nil, Draft: p.GetDraft(), HeadSHA: p.GetHead().GetSHA(), HeadRef: p.GetHead().GetRef(),
		BaseRef: p.GetBase().GetRef(), Author: p.GetUser().GetLogin(), Title: p.GetTitle(), Body: p.GetBody(),
	}
	for _, l := range p.Labels {
		out.Labels = append(out.Labels, l.GetName())
	}
	return out
}

func (f *GitHubForge) PRsForBranch(ctx context.Context, repo, branch string) ([]PR, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	prs, _, err := f.Client.PullRequests.List(ctx, owner, name, &github.PullRequestListOptions{
		State: "all", Head: owner + ":" + branch, Base: "main", ListOptions: github.ListOptions{PerPage: 50},
	})
	if err != nil {
		return nil, err
	}
	out := make([]PR, 0, len(prs))
	for _, p := range prs {
		out = append(out, toPR(p))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number > out[j].Number })
	return out, nil
}

// CreatePR refuses any head but an autopilot bump branch and any base but main.
func (f *GitHubForge) CreatePR(ctx context.Context, repo string, p NewPR) (PR, error) {
	if !headRE.MatchString(p.Head) || p.Base != "main" {
		return PR{}, fmt.Errorf("%w: PR head %q, base %q", ErrRefNotAllowed, p.Head, p.Base)
	}
	owner, name, err := splitRepo(repo)
	if err != nil {
		return PR{}, err
	}
	pr, _, err := f.Client.PullRequests.Create(ctx, owner, name, &github.NewPullRequest{
		Title: github.String(p.Title), Head: github.String(p.Head), Base: github.String(p.Base),
		Body: github.String(p.Body), Draft: github.Bool(p.Draft),
	})
	if err != nil {
		return PR{}, err
	}
	return toPR(pr), nil
}

func (f *GitHubForge) EditPR(ctx context.Context, repo string, number int, title, body string) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	_, _, err = f.Client.PullRequests.Edit(ctx, owner, name, number, &github.PullRequest{Title: github.String(title), Body: github.String(body)})
	return err
}

const (
	gqlReady = `mutation($id: ID!) { markPullRequestReadyForReview(input: {pullRequestId: $id}) { pullRequest { isDraft } } }`
	gqlDraft = `mutation($id: ID!) { convertPullRequestToDraft(input: {pullRequestId: $id}) { pullRequest { isDraft } } }`
)

// SetDraft uses GraphQL: REST has no draft/ready endpoint.
func (f *GitHubForge) SetDraft(ctx context.Context, repo string, number int, draft bool) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	pr, _, err := f.Client.PullRequests.Get(ctx, owner, name, number)
	if err != nil {
		return err
	}
	q := gqlReady
	if draft {
		q = gqlDraft
	}
	req, err := f.Client.NewRequest(http.MethodPost, "graphql", map[string]any{
		"query": q, "variables": map[string]any{"id": pr.GetNodeID()},
	})
	if err != nil {
		return err
	}
	var resp struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if _, err := f.Client.Do(ctx, req, &resp); err != nil {
		return err
	}
	if len(resp.Errors) > 0 {
		return fmt.Errorf("graphql: %s", resp.Errors[0].Message)
	}
	return nil
}

func (f *GitHubForge) AddLabels(ctx context.Context, repo string, number int, labels []string) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	_, _, err = f.Client.Issues.AddLabelsToIssue(ctx, owner, name, number, labels)
	return err
}

func (f *GitHubForge) RemoveLabel(ctx context.Context, repo string, number int, label string) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	resp, err := f.Client.Issues.RemoveLabelForIssue(ctx, owner, name, number, label)
	if resp != nil && resp.StatusCode == http.StatusNotFound {
		return nil // already gone
	}
	return err
}

func (f *GitHubForge) SetStatus(ctx context.Context, repo, sha string, s CommitStatus) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	st := &github.RepoStatus{State: github.String(s.State), Context: github.String(s.Context), Description: github.String(s.Description)}
	if s.TargetURL != "" {
		st.TargetURL = github.String(s.TargetURL)
	}
	_, _, err = f.Client.Repositories.CreateStatus(ctx, owner, name, sha, st)
	return err
}

func (f *GitHubForge) GetStatus(ctx context.Context, repo, sha, context string) (string, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return "", err
	}
	cs, _, err := f.Client.Repositories.GetCombinedStatus(ctx, owner, name, sha, &github.ListOptions{PerPage: 100})
	if err != nil {
		return "", err
	}
	for _, s := range cs.Statuses {
		if s.GetContext() == context {
			return s.GetState(), nil // newest first
		}
	}
	return "", nil
}
