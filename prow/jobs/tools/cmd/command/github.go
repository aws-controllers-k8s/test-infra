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

package command

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/go-github/v63/github"
)

// githubRequestTimeout bounds each GitHub API request; go-github's default
// client has no timeout.
const githubRequestTimeout = 60 * time.Second

const (
// baseBranch default is now controlled via --base-branch flag (OptBaseBranch)
)

var (
	defaultProwAutoGenLabel = "prow/auto-gen"
)

// getRef returns the commit branch reference object if it exists or creates it
// from the base branch before returning it.
func getGitRef(ctx context.Context, client *github.Client, sourceOwner, sourceRepo, commitBranch, baseBranch string) (ref *github.Reference, err error) {
	if ref, _, err = client.Git.GetRef(ctx, sourceOwner, sourceRepo, "refs/heads/"+commitBranch); err == nil {
		return ref, nil
	}

	// We consider that an error means the branch has not been found and needs to
	// be created.
	if commitBranch == baseBranch || baseBranch == "" {
		return nil, fmt.Errorf("the commit branch does not exist and base branch is equal to commit branch: %s", baseBranch)
	}

	var baseRef *github.Reference
	if baseRef, _, err = client.Git.GetRef(ctx, sourceOwner, sourceRepo, "refs/heads/"+baseBranch); err != nil {
		return nil, err
	}
	newRef := &github.Reference{Ref: github.String("refs/heads/" + commitBranch), Object: &github.GitObject{SHA: baseRef.Object.SHA}}
	ref, _, err = client.Git.CreateRef(ctx, sourceOwner, sourceRepo, newRef)
	return ref, err
}

// getTree generates the tree to commit based on the given files and the commit
// of the ref you got in getRef.
// sourceFiles format: "file1:PATH_TO_FILE1,file2:PATH_TO_FILE2..."
func getGitTree(ctx context.Context, client *github.Client, ref *github.Reference, sourceFiles, sourceOwner, sourceRepo string) (tree *github.Tree, err error) {
	// Create a tree with what to commit.
	entries := []*github.TreeEntry{}

	// Load each file into the tree.
	for _, fileArg := range strings.Split(sourceFiles, ",") {
		file, content, err := getFileContent(fileArg)
		if err != nil {
			return nil, err
		}

		entry := &github.TreeEntry{
			Path: github.String(file),
			Type: github.String("blob"),
			Mode: github.String("100644"),
		}

		// Binary files must be uploaded as blobs with base64 encoding
		// to prevent corruption during JSON serialization
		if isBinaryFile(file) {
			blob := &github.Blob{
				Content:  github.String(base64.StdEncoding.EncodeToString(content)),
				Encoding: github.String("base64"),
			}
			createdBlob, _, err := client.Git.CreateBlob(ctx, sourceOwner, sourceRepo, blob)
			if err != nil {
				return nil, fmt.Errorf("failed to create blob for %s: %v", file, err)
			}
			entry.SHA = createdBlob.SHA
		} else {
			// Text files can use inline content
			entry.Content = github.String(string(content))
		}

		entries = append(entries, entry)
	}

	tree, _, err = client.Git.CreateTree(ctx, sourceOwner, sourceRepo, *ref.Object.SHA, entries)
	return tree, err
}

// getFileContent loads the local content of a file and return the target name
// of the file in the target repository and its contents.
// fileArg format: "filename:PATH_TO_FILE"
func getFileContent(fileArg string) (targetName string, b []byte, err error) {
	var localFile string
	files := strings.Split(fileArg, ":")
	switch {
	case len(files) < 1:
		return "", nil, fmt.Errorf("files to commit not submitted")
	case len(files) == 1:
		localFile = files[0]
		targetName = files[0]
	default:
		localFile = files[0]
		targetName = files[1]
	}

	b, err = os.ReadFile(localFile)
	return targetName, b, err
}

// isBinaryFile returns true if the file should be treated as binary
// based on its extension to prevent JSON serialization corruption.
func isBinaryFile(filename string) bool {
	ext := filepath.Ext(filename)
	binaryExtensions := []string{".gz", ".zip", ".tar", ".tgz", ".bz2", ".xz", ".png", ".jpg", ".jpeg", ".gif", ".pdf", ".bin"}
	for _, binExt := range binaryExtensions {
		if ext == binExt {
			return true
		}
	}
	return false
}

// pushCommit creates the commit in the given reference using the given tree.
func pushCommit(ctx context.Context, client *github.Client, ref *github.Reference, tree *github.Tree, sourceOwner, sourceRepo, commitMessage string) (err error) {
	// Get the parent commit to attach the commit to.
	parent, _, err := client.Repositories.GetCommit(ctx, sourceOwner, sourceRepo, *ref.Object.SHA, nil)
	if err != nil {
		return err
	}
	// This is not always populated, but is needed.
	parent.Commit.SHA = parent.SHA

	// Create the commit using the tree.
	commit := &github.Commit{Message: github.String(commitMessage), Tree: tree, Parents: []*github.Commit{parent.Commit}}
	opts := github.CreateCommitOptions{}
	newCommit, _, err := client.Git.CreateCommit(ctx, sourceOwner, sourceRepo, commit, &opts)
	if err != nil {
		return err
	}

	// Attach the commit to the master branch.
	ref.Object.SHA = newCommit.SHA
	_, _, err = client.Git.UpdateRef(ctx, sourceOwner, sourceRepo, ref, false)
	return err
}

func createPR(ctx context.Context, client *github.Client, prSubject, commitBranch, prDescription, sourceOwner, sourceRepo, baseBranch string) error {
	if prSubject == "" {
		return fmt.Errorf("missing pr title flag; skipping PR creation")
	}

	newPR := &github.NewPullRequest{
		Title:               github.String(prSubject),
		Head:                github.String(commitBranch),
		Base:                github.String(baseBranch),
		Body:                github.String(prDescription),
		MaintainerCanModify: github.Bool(true),
	}

	createdPR, _, err := client.PullRequests.Create(ctx, sourceOwner, sourceRepo, newPR)
	if err != nil {
		return err
	}

	// Update the PR labels

	// Github API ¯\_(ツ)_/¯
	prWithLabels := &github.PullRequest{
		Labels: []*github.Label{
			&github.Label{
				Name: &defaultProwAutoGenLabel,
			},
		},
	}
	_, _, err = client.PullRequests.Edit(
		ctx,
		sourceOwner,
		sourceRepo,
		int(*createdPR.ID),
		prWithLabels,
	)
	if err != nil {
		return nil
	}

	return nil
}

// createGithubIssue files an issue as the account that owns GITHUB_TOKEN, for
// callers without a client of their own.
func createGithubIssue(owner, repo, title, body string, labels []string) error {
	client, err := newGithubClientFromEnv()
	if err != nil {
		return err
	}
	_, err = createGithubIssueWithClient(context.Background(), client, owner, repo, title, body, labels)
	return err
}

// apiChangeLabel is the detector's own label. It is not `kind/api-change`, which
// anyone can apply with `/kind api-change`, and it has no `prowPlugin`, so no
// slash command applies it.
const apiChangeLabel = "ack/api-change-detected"

// newGithubClientFromEnv builds an authenticated client from GITHUB_TOKEN. The
// helpers below take a client instead, so tests can use an httptest server.
func newGithubClientFromEnv() (*github.Client, error) {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("environment variable GITHUB_TOKEN is not provided")
	}
	return github.NewClient(&http.Client{Timeout: githubRequestTimeout}).WithAuthToken(token), nil
}

// fingerprintedOnly reports whether an issue carries this detector's fingerprint
// marker. It is checked on top of the label and author filters, since getting it
// wrong overwrites somebody's issue.
func fingerprintedOnly(issue *github.Issue) bool {
	return parseFingerprint(issue.GetBody()) != ""
}

// githubLogin returns the token's account login, for scoping searches with
// `author:`.
//
// This and the helpers below wrap with %w, not %s, so errors.As can still find
// *github.RateLimitError.
func githubLogin(ctx context.Context, client *github.Client) (string, error) {
	user, _, err := client.Users.Get(ctx, "")
	if err != nil {
		return "", fmt.Errorf("unable to resolve the authenticated user: %w", err)
	}
	if user.GetLogin() == "" {
		return "", fmt.Errorf("the authenticated user has no login")
	}
	return user.GetLogin(), nil
}

// githubSearchWindow is how many results GitHub's search API pages through.
// Past it the listing silently ends, but total_count stays accurate.
const githubSearchWindow = 1000

// listAPIChangeIssues lists the bot's open and closed issues in two searches
// (`is:issue` excludes pull requests).
//
// The open search gates writes, so it must be exact: it fails if total_count
// exceeds the search window. The closed search only supplies fingerprints a
// maintainer dismissed by closing; it can outgrow the window, so it warns and
// continues. One combined search would fill the window with old closed issues.
//
// openCount includes duplicates and unattributable open issues, since the cap
// bounds every open issue. Issues it declines to manage are reported in warnings.
func listAPIChangeIssues(
	ctx context.Context,
	client *github.Client,
	owner, repo, botLogin string,
	knownServices map[string]bool,
) (
	openByService map[string]*github.Issue,
	openCount int,
	closedFingerprints map[string]map[string]bool,
	warnings []string,
	err error,
) {
	openByService = map[string]*github.Issue{}
	closedFingerprints = map[string]map[string]bool{}

	// attribute returns the issue's service, whether it is in wantState, and
	// whether to manage it, warning when it declines. inWantState is separate
	// because an open issue counts against the cap whatever its labels say.
	attribute := func(issue *github.Issue, wantState string) (service string, inWantState, manage bool) {
		// GitHub documents only "open" and "closed"; anything else is warned about.
		state := issue.GetState()
		if (state == "open" || state == "closed") && state != wantState {
			// The search index lags writes by seconds, so a just-changed issue can
			// come back under its old state. The next run sees it correctly.
			log.Printf("issue #%d is now %s but the search index still lists it as %s; skipping it this run",
				issue.GetNumber(), state, wantState)
			return "", false, false
		}
		if state != wantState {
			warnings = append(warnings, fmt.Sprintf(
				"issue #%d reports state %q, not the %q the search asked for; not managing it",
				issue.GetNumber(), state, wantState))
			return "", false, false
		}
		if !fingerprintedOnly(issue) {
			// Not managed, since the body cannot be identified, but still one of
			// ours in wantState, so it counts against the cap.
			warnings = append(warnings, fmt.Sprintf(
				"issue #%d carries %s but no readable fingerprint; not managing it",
				issue.GetNumber(), apiChangeLabel))
			return "", true, false
		}
		service, err := serviceFromLabels(issue, knownServices)
		if err != nil {
			// A closed issue for a service this run does not check is irrelevant,
			// so it is skipped silently. Open or ambiguous ones still warn.
			if wantState == "closed" && errors.Is(err, errNoKnownServiceLabel) {
				return "", true, false
			}
			warnings = append(warnings, err.Error())
			return "", true, false
		}
		return service, true, true
	}

	// Oldest first, so when duplicates exist the same issue wins every run.
	err = searchIssuePages(ctx, client, owner, repo,
		fmt.Sprintf("repo:%s/%s is:issue is:open label:%s author:%s",
			owner, repo, apiChangeLabel, botLogin),
		"asc",
		func(total int) error {
			if total > githubSearchWindow {
				return fmt.Errorf(
					"%s/%s reports %d open %s issues, beyond the %d-result search window; "+
						"refusing to act on a count that cannot be read exactly",
					owner, repo, total, apiChangeLabel, githubSearchWindow)
			}
			return nil
		},
		func(issue *github.Issue) {
			service, open, manage := attribute(issue, "open")
			if !open {
				return
			}
			// Counted before the manage check; see attribute.
			openCount++
			if !manage {
				return
			}
			if existing, dup := openByService[service]; dup {
				warnings = append(warnings, fmt.Sprintf(
					"%s: #%d duplicates #%d", service, issue.GetNumber(), existing.GetNumber(),
				))
				return
			}
			openByService[service] = issue
		})
	if err != nil {
		return nil, 0, nil, nil, err
	}

	// Newest first, so if the window overflows it keeps the most relevant
	// fingerprints.
	err = searchIssuePages(ctx, client, owner, repo,
		fmt.Sprintf("repo:%s/%s is:issue is:closed label:%s author:%s",
			owner, repo, apiChangeLabel, botLogin),
		"desc",
		func(total int) error {
			if total > githubSearchWindow {
				warnings = append(warnings, fmt.Sprintf(
					"%s/%s has %d closed %s issues, beyond the %d-result search window; "+
						"suppression may be incomplete and a dismissed finding set may be re-filed",
					owner, repo, total, apiChangeLabel, githubSearchWindow))
			}
			return nil
		},
		func(issue *github.Issue) {
			service, _, manage := attribute(issue, "closed")
			if !manage {
				return
			}
			if closedFingerprints[service] == nil {
				closedFingerprints[service] = map[string]bool{}
			}
			closedFingerprints[service][parseFingerprint(issue.GetBody())] = true
		})
	if err != nil {
		return nil, 0, nil, nil, err
	}

	return openByService, openCount, closedFingerprints, warnings, nil
}

// searchIssuePages runs an issue search across all pages, passing each issue to
// visit. onTotal gets the first page's total_count before any issue is visited,
// so a refused count costs one request.
func searchIssuePages(
	ctx context.Context,
	client *github.Client,
	owner, repo, query, order string,
	onTotal func(total int) error,
	visit func(issue *github.Issue),
) error {
	opts := &github.SearchOptions{
		// Best-match order is not stable between runs.
		Sort:        "created",
		Order:       order,
		ListOptions: github.ListOptions{PerPage: 100},
	}
	firstPage := true
	for {
		result, resp, err := client.Search.Issues(ctx, query, opts)
		if err != nil {
			return fmt.Errorf("unable to search issues in %s/%s: %w", owner, repo, err)
		}
		// Set when the search times out; the count and items are then partial,
		// and an undercount would file past the cap.
		if result.GetIncompleteResults() {
			return fmt.Errorf(
				"issue search for %s/%s returned incomplete results; refusing to act on a partial count",
				owner, repo,
			)
		}
		if firstPage {
			if err := onTotal(result.GetTotal()); err != nil {
				return err
			}
			firstPage = false
		}
		for _, issue := range result.Issues {
			visit(issue)
		}
		if resp.NextPage == 0 {
			return nil
		}
		opts.ListOptions.Page = resp.NextPage
	}
}

// errNoKnownServiceLabel is wrapped by serviceFromLabels when an issue names none
// of the known services, so callers can tell it apart from an ambiguous issue.
var errNoKnownServiceLabel = errors.New("carries no known service/ label")

// serviceFromLabels returns the one known service an issue's `service/<name>`
// labels name. Zero or several matches are errors rather than guesses, since
// maintainers can add labels such as `service/s3control` beside `service/s3`.
func serviceFromLabels(issue *github.Issue, known map[string]bool) (string, error) {
	var matches []string
	for _, label := range issue.Labels {
		name := label.GetName()
		suffix, ok := strings.CutPrefix(name, "service/")
		if !ok || suffix == "" {
			continue
		}
		if known[suffix] {
			matches = append(matches, suffix)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("issue #%d %w", issue.GetNumber(), errNoKnownServiceLabel)
	default:
		sort.Strings(matches)
		return "", fmt.Errorf("issue #%d carries several service/ labels (%s); refusing to guess",
			issue.GetNumber(), strings.Join(matches, ", "))
	}
}

// updateGithubIssueBody replaces an issue's body, leaving its other fields alone
// (the PATCH omits nil fields). Pass replaceGeneratedRegion's output to keep a
// maintainer's text.
func updateGithubIssueBody(
	ctx context.Context,
	client *github.Client,
	owner, repo string,
	number int,
	body string,
) error {
	_, _, err := client.Issues.Edit(ctx, owner, repo, number, &github.IssueRequest{
		Body: github.String(body),
	})
	if err != nil {
		return fmt.Errorf("unable to edit issue %d: %w", number, err)
	}
	return nil
}

// commentOnGithubIssue adds a comment to an issue. Body edits do not notify
// subscribers; comments do.
func commentOnGithubIssue(
	ctx context.Context,
	client *github.Client,
	owner, repo string,
	number int,
	body string,
) error {
	_, _, err := client.Issues.CreateComment(ctx, owner, repo, number, &github.IssueComment{
		Body: github.String(body),
	})
	if err != nil {
		return fmt.Errorf("unable to comment on issue %d: %w", number, err)
	}
	return nil
}

// errCannotLabelIssues reports that the token filed an issue but could not label
// it, so no later run will find it. It is a property of the credential, so a
// caller looping over services must abort on it rather than continue.
var errCannotLabelIssues = errors.New("issues cannot be labelled with " + apiChangeLabel)

// createGithubIssueWithClient files an issue and returns it.
//
// The returned labels are checked because GitHub silently drops labels when the
// token's account lacks push access, and an issue without apiChangeLabel is
// invisible to later runs and to the cap.
func createGithubIssueWithClient(
	ctx context.Context,
	client *github.Client,
	owner, repo, title, body string,
	labels []string,
) (*github.Issue, error) {
	request := &github.IssueRequest{
		Title: github.String(title),
		Body:  github.String(body),
	}
	// An empty slice would send `"labels": null`; omit the field instead.
	if len(labels) > 0 {
		request.Labels = &labels
	}
	issue, _, err := client.Issues.Create(ctx, owner, repo, request)
	if err != nil {
		if !createDefinitelyRejected(err) {
			return nil, fmt.Errorf("unable to create issue in %s/%s: %w: %w",
				owner, repo, errIssueCreateIndeterminate, err)
		}
		return nil, fmt.Errorf("unable to create issue in %s/%s: %w", owner, repo, err)
	}
	if slices.Contains(labels, apiChangeLabel) && !issueHasLabel(issue, apiChangeLabel) {
		return nil, fmt.Errorf(
			"%w: issue %s/%s#%d was created without it, so no later run can find it; "+
				"the token's account probably lacks push access to %s/%s",
			errCannotLabelIssues, owner, repo, issue.GetNumber(), owner, repo,
		)
	}
	return issue, nil
}

// errIssueCreateIndeterminate reports that an issue POST failed without showing
// whether GitHub filed the issue (a transport error, timeout or 5xx). Callers must
// treat the issue as possibly existing and holding a cap slot.
var errIssueCreateIndeterminate = errors.New("the create may have been committed")

// createDefinitelyRejected reports whether a failed issue POST is known not to have
// filed anything: GitHub answered with a 4xx, which it sends before committing.
// Rate-limit refusals are 4xx too but carry their own error types.
func createDefinitelyRejected(err error) bool {
	var rateLimit *github.RateLimitError
	var abuse *github.AbuseRateLimitError
	if errors.As(err, &rateLimit) || errors.As(err, &abuse) {
		return true
	}
	var resp *github.ErrorResponse
	if errors.As(err, &resp) && resp.Response != nil {
		return resp.Response.StatusCode >= 400 && resp.Response.StatusCode < 500
	}
	return false
}

// refetchManagedIssue re-reads a listed issue just before a refresh, so the merge
// uses the current body rather than one that may be minutes old. It re-checks
// what the listing established, since the issue may have been closed, relabelled
// or had its fingerprint removed in the meantime.
func refetchManagedIssue(
	ctx context.Context,
	client *github.Client,
	owner, repo, service string,
	listed *github.Issue,
) (*github.Issue, error) {
	number := listed.GetNumber()
	fresh, _, err := client.Issues.Get(ctx, owner, repo, number)
	if err != nil {
		return nil, fmt.Errorf("unable to re-read issue %s/%s#%d before refreshing it: %w",
			owner, repo, number, err)
	}
	var problems []string
	if fresh.GetState() != "open" {
		problems = append(problems, fmt.Sprintf("its state is now %q", fresh.GetState()))
	}
	if fresh.IsPullRequest() {
		problems = append(problems, "it is a pull request")
	}
	if want := listed.GetUser().GetLogin(); want == "" || fresh.GetUser().GetLogin() != want {
		problems = append(problems, fmt.Sprintf("its author is %q, not %q",
			fresh.GetUser().GetLogin(), want))
	}
	if !issueHasLabel(fresh, apiChangeLabel) {
		problems = append(problems, "it no longer carries "+apiChangeLabel)
	}
	if !issueHasLabel(fresh, "service/"+service) {
		problems = append(problems, "it no longer carries service/"+service)
	}
	if !fingerprintedOnly(fresh) {
		problems = append(problems, "its fingerprint is no longer readable")
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("not refreshing issue %s/%s#%d: %s",
			owner, repo, number, strings.Join(problems, "; "))
	}
	return fresh, nil
}

// issueHasLabel reports whether an issue carries a label.
func issueHasLabel(issue *github.Issue, name string) bool {
	for _, label := range issue.Labels {
		if label.GetName() == name {
			return true
		}
	}
	return false
}
