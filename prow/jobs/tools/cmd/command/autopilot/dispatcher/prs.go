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

package dispatcher

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

// PR is what the Dispatcher reads about one pull request (Plan 9). Every
// field comes from the GitHub API, not from the PR's text.
type PR struct {
	Number int
	// State is "open" or "closed"; Merged is set for a closed, merged PR.
	State  string
	Merged bool
	Draft  bool
	// Author is the PR's user login.
	Author  string
	HeadSHA string
	HeadRef string
	BaseSHA string
	Labels  []string
	// Body is read for sdk-bump's Gate-Verdict trailer only.
	Body string
	// CreatedAt, MergedAt and ClosedAt are RFC 3339 UTC, or empty.
	CreatedAt string
	MergedAt  string
	ClosedAt  string
	MergeSHA  string
}

// PRCommit is one commit on a PR.
type PRCommit struct {
	SHA string
	// Author is the GitHub login GitHub mapped the author email to; empty
	// when it mapped none (which counts as human).
	Author string
	// Date is the committer date, RFC 3339 UTC.
	Date string
}

// PRReview is one submitted review on a PR.
type PRReview struct {
	ID     int64
	Author string
	// State is GitHub's: APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED.
	State       string
	SubmittedAt string
}

// PRClient reads pull requests. The Dispatcher reads only PRs the ledger's
// links.pr names (trailers are not proof of origin, README open decision
// 10), plus open PRs for the runtime-release back-off.
type PRClient interface {
	PR(ctx context.Context, repo string, number int) (PR, error)
	Commits(ctx context.Context, repo string, number int) ([]PRCommit, error)
	Reviews(ctx context.Context, repo string, number int) ([]PRReview, error)
	// TreeSHA and IsAncestor answer dod.Commits for one repo.
	TreeSHA(ctx context.Context, repo, commit string) (string, error)
	IsAncestor(ctx context.Context, repo, base, commit string) (bool, error)
	// OpenPRs lists the repo's open PRs.
	OpenPRs(ctx context.Context, repo string) ([]PR, error)
}

// PRWriter is the Dispatcher's only write to controller repos: marking a
// gated draft ready for review (Plan 9 decision 4), and turning a PR it
// marked ready back into a draft while a new head re-gates (Plan 9 review
// F6). It is nil unless the job sets AUTOPILOT_PR_WRITE=true.
type PRWriter interface {
	MarkReady(ctx context.Context, repo string, number int) error
	MarkDraft(ctx context.Context, repo string, number int) error
}

// ErrNoVerdict: the run left no verdict.json.
var ErrNoVerdict = errors.New("no verdict")

// VerdictReader is implemented by launchers that can read a finished run's
// verdict.json (sdk-bump's in-process verdict, or an autopilot-dod job's).
type VerdictReader interface {
	// Verdict returns the run's verdict.json, at most MaxVerdictBytes, or
	// ErrNoVerdict. For a dod job it also returns verdict.sig (at most
	// MaxSignatureBytes); a missing one is a nil signature and no error, and
	// gate decides (Plan 9 review). sdk-bump's signature is always nil.
	Verdict(ctx context.Context, launchID string) (verdict, signature []byte, err error)
}

// FeedbackJobs is implemented by launchers that can see agent-plugin's
// address-feedback jobs (design 5.5): while one runs for a PR, the item waits
// in FEEDBACK.
type FeedbackJobs interface {
	FeedbackActive(ctx context.Context, repo string, number int) (bool, error)
}

// MaxVerdictBytes bounds a verdict.json read. A real verdict is a few KiB;
// dod itself refuses more than 4 MiB.
const MaxVerdictBytes = 1 << 20

// MaxSignatureBytes bounds a verdict.sig read: a base64 Ed25519 signature is
// 89 bytes.
const MaxSignatureBytes = 1 << 10

// prNumber is the number of the PR links.pr names in w's repo.
func prNumber(w *ledger.WorkItem) (int, bool) {
	if !ledger.PRInRepo(w.Repo, w.Links.PR) {
		return 0, false
	}
	n, err := strconv.Atoi(w.Links.PR[strings.LastIndex(w.Links.PR, "/")+1:])
	return n, err == nil
}

// repoCommits answers dod.Commits for one repo through a PRClient.
type repoCommits struct {
	c    PRClient
	repo string
}

func (r repoCommits) TreeSHA(ctx context.Context, commit string) (string, error) {
	return r.c.TreeSHA(ctx, r.repo, commit)
}

func (r repoCommits) IsAncestor(ctx context.Context, base, commit string) (bool, error) {
	return r.c.IsAncestor(ctx, r.repo, base, commit)
}
