package issuefinder

import (
	"context"
	"fmt"

	"github.com/google/go-github/v63/github"
)

// FetchIssues lists every issue, open or closed, carrying the detector's label.
// Pull requests are skipped. Comments are fetched for open issues only.
// It only reads from GitHub.
//
// The listing is not a snapshot, and only part of the drift is detected.
// An issue filed or labelled while pages are being read shifts later pages
// right, so one issue is listed twice; FetchIssues fails when any number
// repeats. An issue removed or unlabelled mid-listing shifts later pages left
// and can skip an issue, and nothing here notices. That issue is missed for
// this run only and is picked up by the next one.
func FetchIssues(ctx context.Context, c *github.Client, owner, repo string) ([]Issue, error) {
	opt := &github.IssueListByRepoOptions{
		State: "all", Labels: []string{apiChangeLabel},
		ListOptions: github.ListOptions{PerPage: 100},
	}
	var out []Issue
	seen := map[int]bool{}
	for {
		page, resp, err := c.Issues.ListByRepo(ctx, owner, repo, opt)
		if err != nil {
			return nil, fmt.Errorf("listing %s/%s issues: %w", owner, repo, err)
		}
		for _, gi := range page {
			// Pull requests share the number space, so they count too.
			n := gi.GetNumber()
			if seen[n] {
				return nil, fmt.Errorf("listing %s/%s issues: #%d listed twice; results shifted during listing", owner, repo, n)
			}
			seen[n] = true
			if gi.IsPullRequest() {
				continue
			}
			is := Issue{
				Number: gi.GetNumber(), URL: gi.GetHTMLURL(), Open: gi.GetState() == "open",
				Author: gi.GetUser().GetLogin(), Body: gi.GetBody(),
			}
			for _, l := range gi.Labels {
				is.Labels = append(is.Labels, l.GetName())
			}
			if is.Open {
				if is.Comments, err = fetchComments(ctx, c, owner, repo, is.Number); err != nil {
					return nil, err
				}
			}
			out = append(out, is)
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		opt.Page = resp.NextPage
	}
}

func fetchComments(ctx context.Context, c *github.Client, owner, repo string, number int) ([]Comment, error) {
	opt := &github.IssueListCommentsOptions{ListOptions: github.ListOptions{PerPage: 100}}
	var out []Comment
	for {
		page, resp, err := c.Issues.ListComments(ctx, owner, repo, number, opt)
		if err != nil {
			return nil, fmt.Errorf("listing comments on #%d: %w", number, err)
		}
		for _, ic := range page {
			out = append(out, Comment{Author: ic.GetUser().GetLogin(), Body: ic.GetBody()})
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		opt.Page = resp.NextPage
	}
}

// FetchTeam returns the logins of an org team's members. An empty login is
// never added, so an authorless comment cannot match a member.
func FetchTeam(ctx context.Context, c *github.Client, org, slug string) (map[string]bool, error) {
	opt := &github.TeamListTeamMembersOptions{ListOptions: github.ListOptions{PerPage: 100}}
	out := map[string]bool{}
	for {
		page, resp, err := c.Teams.ListTeamMembersBySlug(ctx, org, slug, opt)
		if err != nil {
			return nil, fmt.Errorf("listing members of %s/%s: %w", org, slug, err)
		}
		for _, u := range page {
			if l := u.GetLogin(); l != "" {
				out[l] = true
			}
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		opt.Page = resp.NextPage
	}
}
