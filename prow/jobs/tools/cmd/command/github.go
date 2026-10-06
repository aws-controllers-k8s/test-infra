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

// githubRequestTimeout bounds each GitHub API request. go-github's default client
// has none, so without it one stalled response holds an unattended job until
// Prow's timeout instead of failing that one request.
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

// createGithubIssue files an issue as the account that owns GITHUB_TOKEN.
//
// It delegates rather than duplicating the create call so that the one operation
// has one error message: scan_controllers_cve.go is the only caller and it has no
// client of its own, which is the whole difference from
// createGithubIssueWithClient. The created issue is discarded here because that
// caller has no use for it, and keeping this signature error-only leaves it
// untouched.
func createGithubIssue(owner, repo, title, body string, labels []string) error {
	client, err := newGithubClientFromEnv()
	if err != nil {
		return err
	}
	_, err = createGithubIssueWithClient(context.Background(), client, owner, repo, title, body, labels)
	return err
}

// apiChangeLabel is the detector's own label. It deliberately does not reuse
// `kind/api-change`: that one is declared in labels.yaml with
// `prowPlugin: label, addedBy: anyone`, so it is the standard Prow triage label a
// maintainer applies with `/kind api-change`. Searching by it matched human-filed
// issues, and the update path then overwrote the reporter's text with a generated
// report.
//
// It also carries no `prowPlugin`, so there is no slash command that applies it.
const apiChangeLabel = "ack/api-change-detected"

// newGithubClientFromEnv builds an authenticated client from GITHUB_TOKEN.
//
// The helpers below take a *github.Client rather than reading the environment
// themselves, which is what lets them be pointed at an httptest server; this is
// the single place the unattended job turns its credential into one.
func newGithubClientFromEnv() (*github.Client, error) {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("environment variable GITHUB_TOKEN is not provided")
	}
	return github.NewClient(&http.Client{Timeout: githubRequestTimeout}).WithAuthToken(token), nil
}

// fingerprintedOnly reports whether an issue carries this detector's fingerprint
// marker, which is the discriminator no label or author filter can be tricked out
// of: renderIssueBody always writes one, and nothing else in the org does.
//
// Belt and braces on top of the label and the author filter, because being wrong
// here means silently overwriting somebody's issue, and the check costs no API
// call — the search response already carries the body.
func fingerprintedOnly(issue *github.Issue) bool {
	return parseFingerprint(issue.GetBody()) != ""
}

// githubLogin returns the login of the account the token belongs to, for scoping
// searches with `author:`. Prow supplies the credential, so this is the only way
// to know which account the issues will be filed as.
//
// This and the helpers below wrap with %w rather than the %s used elsewhere in
// this file. %s flattens the error to text, and then errors.As for
// *github.RateLimitError fails — a rate limit is the one failure an unattended
// daily job should back off on rather than abort, and it cannot be told from a
// 404 by matching strings.
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

// githubSearchWindow is how many results GitHub's search API will page through.
// Past it the Link header stops offering a next page, so a listing quietly ends
// early — while total_count keeps reporting the true figure, which is the only
// reason an overrun is detectable at all.
const githubSearchWindow = 1000

// listAPIChangeIssues runs two searches, deliberately.
//
// The open query gates writes — the cap is measured against its count, and a wrong
// count means either spamming a public repo or going silent — so it must be exact,
// and it is: the cap holds the open set to the low tens, far inside the search
// window. It refuses outright when total_count exceeds the window, because
// total_count stays accurate past it and this function's own principle is that a
// count gating a write must not be guessed at. An open count over 1,000 means
// something is badly wrong and filing more issues is the worst available response.
//
// The closed query only decides whether a finding set a maintainer already dismissed
// gets re-filed. Closed issues accumulate for the life of the job with nothing
// bounding them, so this one *can* exceed the window; when it does, some suppression
// is lost and an issue may be re-filed. That is a bad day, not a broken repo, so it
// warns and carries on.
//
// A single query without `is:open` had both properties at once, and that is how the
// cap came to be measured against a window full of ancient closed issues while every
// open issue sat outside it: with `Sort: created, Order: asc` the window holds the
// *oldest* 1,000 results, so after a year or two across ~74 services it is all closed
// history. openByService comes back empty, openCount reads 0, the cap never fires,
// and the job files a fresh issue for every configured service every single day —
// and because each new issue is younger than the window, it never self-corrects.
//
// Closed issues are fetched at all because closing is the only way a maintainer can
// say "I have seen this finding set and it does not need an issue". While the search
// was scoped to open issues that said nothing: the next run found nothing open for
// the service and re-filed the same fingerprint, then the next, for ever, on the
// public community repo. The cap did not bound it either — closing an issue
// decrements the open count, which is exactly what unblocks the re-file. Because the
// fingerprint covers the finding set only, a genuinely new finding still produces a
// new fingerprint and files a fresh issue, so closing one issue cannot silence
// future changes.
//
// Two searches for the whole run rather than 1+N: search allows 30 requests a
// minute, and a per-service search made a run cost one each. At ~74 services that
// 403s a third of the way through, daily, with nothing retrying. `PerPage: 1` did
// not help the old shape — the limit counts requests, not result rows.
//
// `is:issue` is not redundant: /search/issues matches pull requests too, and a PR
// carrying the label would otherwise be treated as an issue to edit.
//
// openCount counts every open issue the listing attributed, duplicates included,
// so it is deliberately not len(openByService) — a service with two open issues
// contributes 2. It answers "how many of my issues are open on this repo", which is
// what the cap bounds. It is not "how many services are already covered", so a
// service's absence from openByService means only "no open issue to update", never
// "skip this service".
//
// Every issue this function declines to manage is reported in warnings: no known
// `service/<name>` label, several of them, no readable fingerprint, an unrecognised
// state, or being the second open issue for a service. Picking one arbitrarily is
// what the previous `Issues[0]` did, and under the default best-match sort that
// meant updating a different issue on different days.
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

	// attribute answers "which service's issue is this, and is it ours to manage",
	// appending a warning whenever the answer is no. Both passes go through it so
	// that a declined issue is reported the same way wherever it turns up.
	//
	// It reports separately whether the issue is in the state the search asked for,
	// because an open issue holds a slot against the cap whatever its labels say:
	// it is open, the bot filed it, and it is exactly what the cap bounds. Counting
	// only the attributable ones let a run file past the cap whenever a service was
	// dropped from api_notification_services with its issue still open.
	attribute := func(issue *github.Issue, wantState string) (service string, inWantState, manage bool) {
		// Only "open" means open and only "closed" means closed. Reading anything
		// else as closed — which `GetState() != "open"` did — suppressed filing
		// *and* skipped updating, so the service went quiet with no log line.
		// GitHub documents no third value, so an unreadable state is a surprise to
		// surface rather than one to obey.
		state := issue.GetState()
		if (state == "open" || state == "closed") && state != wantState {
			// The other documented state: the issue changed state after GitHub's
			// search index last saw it, which lags writes by seconds. Observed
			// live — closing an issue and running straight away returned it from
			// the is:open search reporting "closed". Skipping it is right, since
			// the search result is stale either way, but it is not a problem for
			// a human; the next run sees the issue where it belongs.
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
			// Labelled and authored by us but unreadable: skipping is right — we
			// will not rewrite a body we cannot identify — but silence here meant
			// a duplicate got filed and the orphan never appeared in any log.
			//
			// Checked after the state, and reported as in that state, because the
			// fingerprint decides only whether the body is safe to manage. Both
			// searches are scoped to this author and this label, so a removed or
			// damaged fingerprint is still one of our issues sitting open in the
			// public backlog; returning before the count let a run file past the
			// cap for every such issue.
			warnings = append(warnings, fmt.Sprintf(
				"issue #%d carries %s but no readable fingerprint; not managing it",
				issue.GetNumber(), apiChangeLabel))
			return "", true, false
		}
		service, err := serviceFromLabels(issue, knownServices)
		if err != nil {
			// A closed issue only matters for suppressing a service this run
			// checks, so one for any other service is simply not this run's
			// concern. Warning about it did not hold up live: dropping a service
			// from api_notification_services left every closed issue of it
			// warning "needs a human" on every run, forever, with nothing to do.
			// An open one still warns — it needs closing — and so does an
			// ambiguous closed one, whose suppression might apply.
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
			// Counted before the attribution check: an open issue of ours holds a
			// slot whatever its labels say. See attribute.
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

	// Newest first here: the window may not hold every closed issue, and the ones
	// worth keeping are the recently closed ones, whose fingerprints are the most
	// likely to still describe a current finding set.
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

// searchIssuePages runs one issue search to exhaustion, handing every issue on
// every page to visit, and reports the first page's total_count to onTotal before
// any issue is visited.
//
// The two passes in listAPIChangeIssues share it so that pagination, the
// incomplete_results refusal and the page size cannot drift apart between the query
// that gates writes and the one that only improves suppression. What has to differ —
// what an overrun of the search window means — is onTotal's business, and it is
// consulted before the first page's issues so that a count worth refusing costs one
// request rather than ten.
func searchIssuePages(
	ctx context.Context,
	client *github.Client,
	owner, repo, query, order string,
	onTotal func(total int) error,
	visit func(issue *github.Issue),
) error {
	opts := &github.SearchOptions{
		// Sorted explicitly rather than by relevance: best-match order is not
		// stable between runs, so which of two duplicate issues wins would change
		// from day to day. The caller chooses the direction, because the two
		// passes want opposite ends of the search window.
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
		// GitHub sets this when the issue search times out, and total_count and the
		// items are then partial. A count that gates a write must not be guessed
		// at: an undercount files past the cap. It is refused on the closed pass
		// too, where a partial page would silently drop suppression.
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

// serviceFromLabels returns the service alias an issue is filed under, given the
// set of services this run knows about.
//
// It requires exactly one matching `service/<name>` label. Taking the first
// `service/` label instead was wrong in a way that wrote to the wrong issue:
// `service/s3` and `service/s3control` both exist and are both addedBy:anyone, and
// response label order is not guaranteed, so a bot issue for s3 that a maintainer
// also tagged `service/s3control` could key under either — filing a duplicate s3
// issue and overwriting the s3 issue's body with the s3control report. A bare
// `service/` label likewise keyed a tracked service under "".
//
// Ambiguous and unmatched issues are reported rather than guessed at, because
// guessing here means editing somebody else's issue.
// errNoKnownServiceLabel is wrapped by serviceFromLabels when an issue names none
// of the services this run knows about, so the closed pass can tell it apart from
// an ambiguous issue naming several.
var errNoKnownServiceLabel = errors.New("carries no known service/ label")

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

// updateGithubIssueBody replaces an issue's body.
//
// IssueRequest fields are pointers and the PATCH omits the nil ones, so setting
// only Body leaves the title, labels, assignees, and state of an issue humans may
// have curated untouched.
//
// The body is still replaced wholesale, so callers refreshing an existing issue
// must pass replaceGeneratedRegion's output rather than a freshly rendered body —
// that is what keeps a maintainer's own text on the issue.
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

// commentOnGithubIssue adds a comment to an issue.
//
// Editing a body is silent, so the comment is what actually notifies the issue's
// subscribers that the detector found something new.
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
// it, so no later run will find it.
//
// It is a sentinel because the condition is a property of the *credential*, not of
// one service: the token either has push access to the repo or it does not. A
// caller looping over services must errors.Is it and abort the run rather than log
// and continue — "fail loudly on the first one" only bounds the damage to one issue
// if somebody actually stops. Continuing files one unlabelled orphan per remaining
// service, each invisible to every later listing and therefore to the open-issue
// cap, which is the "one bad issue per service per day" this check exists to
// prevent.
var errCannotLabelIssues = errors.New("issues cannot be labelled with " + apiChangeLabel)

// createGithubIssueWithClient files an issue using a supplied client, so that
// callers which already hold one do not rebuild it from the environment, and
// returns the issue it filed. createGithubIssue above delegates here, so this is
// the only place an issue is created and the only error message for it.
//
// The returned labels are checked rather than assumed: GitHub silently drops the
// labels on creation when the token's account lacks push access to the repo, and an
// issue filed without apiChangeLabel is invisible to every later run — so the job
// would re-file it daily while the open-issue cap, which counts only labelled
// issues, stayed at zero and never intervened. Failing loudly on the first one is
// the difference between one bad issue and one per service per day.
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
	// Taking the address of an empty or nil slice sends `"labels": null`, which
	// asks the API to interpret an absent value; omitting the field is what "no
	// labels" means on the wire.
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
		// Wrapped in errCannotLabelIssues so a caller can tell this apart from a
		// per-service failure and stop the run. Without the sentinel the only signal
		// was prose in an error string, and the ordinary log-and-continue loop shape
		// turned one orphan into one per service per day.
		return nil, fmt.Errorf(
			"%w: issue %s/%s#%d was created without it, so no later run can find it; "+
				"the token's account probably lacks push access to %s/%s",
			errCannotLabelIssues, owner, repo, issue.GetNumber(), owner, repo,
		)
	}
	return issue, nil
}

// errIssueCreateIndeterminate reports that an issue POST failed in a way that does
// not establish whether GitHub filed the issue: a transport error, a timeout, or a
// 5xx. GitHub can commit the create and still lose the response, so the caller must
// treat the issue as possibly existing — in particular, as holding a slot against the
// open-issue cap. Counting it as not filed let every later service in the run create
// past the cap, once per lost response, and the job-level retry multiplies the chances.
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

// refetchManagedIssue re-reads an issue the listing attributed to service and
// re-checks everything the listing established, so that a refresh merges into the
// body as it stands now rather than as the search saw it.
//
// The listing runs once, before any service is analysed, and analysis takes minutes
// per service. A maintainer note added in that window was absent from the listed body,
// so merging into it and PATCHing silently deleted the note. The GET narrows that
// window to the few seconds between here and the write.
//
// Ownership is re-checked rather than assumed because the interval also lets the
// issue be closed, relabelled, or have its fingerprint edited away, and each of those
// is a reason the listing would not have handed it over in the first place. Author
// cannot change on GitHub, so comparing it guards against the wrong issue coming back
// rather than against a transfer.
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
