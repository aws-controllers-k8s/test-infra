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
	"slices"
	"strings"
)

// Commit status contexts and labels the workflow owns.
const (
	ContextGate          = "autopilot/gate-verdict"
	ContextServerDefault = "autopilot/server-default"

	LabelAutoGen              = "prow/auto-gen"
	LabelClass                = "autopilot/class-sdk-bump"
	LabelLevel                = "autopilot/level-human-merge"
	LabelBlockedServerDefault = "autopilot/blocked-server-default"
)

// PR is the part of a pull request the workflow reads.
type PR struct {
	Number  int
	URL     string
	NodeID  string
	State   string // "open" or "closed"
	Merged  bool
	Draft   bool
	HeadSHA string
	HeadRef string
	BaseRef string
	Author  string
	Title   string
	Body    string
	Labels  []string
}

type NewPR struct {
	Title, Body, Head, Base string
	Draft                   bool
}

type CommitStatus struct {
	Context, State, Description, TargetURL string
}

// Forge is every GitHub call the workflow makes. repo is "<owner>/<name>".
type Forge interface {
	// Writes reports whether the forge changes GitHub; only --write may use one.
	Writes() bool
	// PRsForBranch lists the PRs in any state whose head is <owner>:branch,
	// newest first.
	PRsForBranch(ctx context.Context, repo, branch string) ([]PR, error)
	CreatePR(ctx context.Context, repo string, p NewPR) (PR, error)
	EditPR(ctx context.Context, repo string, number int, title, body string) error
	SetDraft(ctx context.Context, repo string, number int, draft bool) error
	AddLabels(ctx context.Context, repo string, number int, labels []string) error
	RemoveLabel(ctx context.Context, repo string, number int, label string) error
	SetStatus(ctx context.Context, repo, sha string, s CommitStatus) error
	// GetStatus is the state of context on sha, "" when it has none.
	GetStatus(ctx context.Context, repo, sha, context string) (string, error)
}

// FakeForge is an in-memory GitHub for tests. Err makes a method fail.
type FakeForge struct {
	Login    string
	PRs      map[string][]*PR        // repo -> PRs; PR n is at index n-1
	Statuses map[string]CommitStatus // repo@sha@context
	Calls    []string                // every write, in order
	Err      map[string]error
}

func NewFakeForge(login string) *FakeForge {
	return &FakeForge{Login: login, PRs: map[string][]*PR{}, Statuses: map[string]CommitStatus{}, Err: map[string]error{}}
}

func (f *FakeForge) Writes() bool { return false }

func (f *FakeForge) call(format string, a ...any) {
	f.Calls = append(f.Calls, fmt.Sprintf(format, a...))
}

// Seed adds p as the repo's next PR and returns it.
func (f *FakeForge) Seed(repo string, p PR) *PR {
	p.Number = len(f.PRs[repo]) + 1
	if p.URL == "" {
		p.URL = fmt.Sprintf("https://github.com/%s/pull/%d", repo, p.Number)
	}
	f.PRs[repo] = append(f.PRs[repo], &p)
	return &p
}

func (f *FakeForge) pr(repo string, n int) (*PR, error) {
	if n < 1 || n > len(f.PRs[repo]) {
		return nil, fmt.Errorf("fake: %s has no PR %d", repo, n)
	}
	return f.PRs[repo][n-1], nil
}

func (f *FakeForge) PRsForBranch(_ context.Context, repo, branch string) ([]PR, error) {
	if err := f.Err["PRsForBranch"]; err != nil {
		return nil, err
	}
	var out []PR
	for i := len(f.PRs[repo]) - 1; i >= 0; i-- {
		if p := *f.PRs[repo][i]; p.HeadRef == branch {
			p.Labels = slices.Clone(p.Labels)
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *FakeForge) CreatePR(_ context.Context, repo string, n NewPR) (PR, error) {
	f.call("CreatePR %s head=%s base=%s draft=%t", repo, n.Head, n.Base, n.Draft)
	if err := f.Err["CreatePR"]; err != nil {
		return PR{}, err
	}
	p := f.Seed(repo, PR{State: "open", Draft: n.Draft, HeadRef: n.Head, BaseRef: n.Base, Author: f.Login, Title: n.Title, Body: n.Body})
	return *p, nil
}

func (f *FakeForge) EditPR(_ context.Context, repo string, n int, title, body string) error {
	f.call("EditPR %s #%d", repo, n)
	if err := f.Err["EditPR"]; err != nil {
		return err
	}
	p, err := f.pr(repo, n)
	if err == nil {
		p.Title, p.Body = title, body
	}
	return err
}

func (f *FakeForge) SetDraft(_ context.Context, repo string, n int, draft bool) error {
	f.call("SetDraft %s #%d %t", repo, n, draft)
	if err := f.Err["SetDraft"]; err != nil {
		return err
	}
	p, err := f.pr(repo, n)
	if err == nil {
		p.Draft = draft
	}
	return err
}

func (f *FakeForge) AddLabels(_ context.Context, repo string, n int, labels []string) error {
	f.call("AddLabels %s #%d %s", repo, n, strings.Join(labels, ","))
	if err := f.Err["AddLabels"]; err != nil {
		return err
	}
	p, err := f.pr(repo, n)
	if err != nil {
		return err
	}
	for _, l := range labels {
		if !slices.Contains(p.Labels, l) {
			p.Labels = append(p.Labels, l)
		}
	}
	return nil
}

func (f *FakeForge) RemoveLabel(_ context.Context, repo string, n int, label string) error {
	f.call("RemoveLabel %s #%d %s", repo, n, label)
	if err := f.Err["RemoveLabel"]; err != nil {
		return err
	}
	p, err := f.pr(repo, n)
	if err == nil {
		p.Labels = slices.DeleteFunc(p.Labels, func(l string) bool { return l == label })
	}
	return err
}

func (f *FakeForge) SetStatus(_ context.Context, repo, sha string, s CommitStatus) error {
	f.call("SetStatus %s %s %s=%s", repo, short(sha), s.Context, s.State)
	if err := f.Err["SetStatus"]; err != nil {
		return err
	}
	f.Statuses[repo+"@"+sha+"@"+s.Context] = s
	return nil
}

func (f *FakeForge) GetStatus(_ context.Context, repo, sha, context string) (string, error) {
	if err := f.Err["GetStatus"]; err != nil {
		return "", err
	}
	return f.Statuses[repo+"@"+sha+"@"+context].State, nil
}

// DryRunForge plans every write and performs none. Reads go to Reader (a
// read-only use of the real forge when a token is available); with no Reader
// there are no PRs and no statuses.
type DryRunForge struct {
	Reader  Forge
	Planned []string
}

func (d *DryRunForge) Writes() bool { return false }

func (d *DryRunForge) plan(format string, a ...any) {
	d.Planned = append(d.Planned, fmt.Sprintf(format, a...))
}

func (d *DryRunForge) PRsForBranch(ctx context.Context, repo, branch string) ([]PR, error) {
	if d.Reader == nil {
		return nil, nil
	}
	return d.Reader.PRsForBranch(ctx, repo, branch)
}

func (d *DryRunForge) CreatePR(_ context.Context, repo string, p NewPR) (PR, error) {
	d.plan("would create a PR in %s from %s to %s (draft=%t): %q", repo, p.Head, p.Base, p.Draft, p.Title)
	return PR{State: "open", Draft: p.Draft, HeadRef: p.Head, BaseRef: p.Base, Title: p.Title, Body: p.Body}, nil
}

func (d *DryRunForge) EditPR(_ context.Context, repo string, n int, title, _ string) error {
	d.plan("would edit %s#%d: %q and its body", repo, n, title)
	return nil
}

func (d *DryRunForge) SetDraft(_ context.Context, repo string, n int, draft bool) error {
	d.plan("would set %s#%d draft=%t", repo, n, draft)
	return nil
}

func (d *DryRunForge) AddLabels(_ context.Context, repo string, n int, labels []string) error {
	d.plan("would add labels %s to %s#%d", strings.Join(labels, ","), repo, n)
	return nil
}

func (d *DryRunForge) RemoveLabel(_ context.Context, repo string, n int, label string) error {
	d.plan("would remove label %s from %s#%d", label, repo, n)
	return nil
}

func (d *DryRunForge) SetStatus(_ context.Context, repo, sha string, s CommitStatus) error {
	d.plan("would set status %s=%s on %s@%s: %s", s.Context, s.State, repo, short(sha), s.Description)
	return nil
}

func (d *DryRunForge) GetStatus(ctx context.Context, repo, sha, context string) (string, error) {
	if d.Reader == nil {
		return "", nil
	}
	return d.Reader.GetStatus(ctx, repo, sha, context)
}
