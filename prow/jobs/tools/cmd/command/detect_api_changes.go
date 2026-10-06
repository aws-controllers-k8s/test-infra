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
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/go-github/v63/github"
	"github.com/spf13/cobra"
)

var detectAPIChangesCMD = &cobra.Command{
	Use:   "detect-api-changes",
	Short: "detect-api-changes - compares AWS API models against ACK controllers and files issues for gaps",
	RunE:  detectAPIChanges,
}

// detectAPIChangesRunTimeout bounds one whole run. It sits below the ProwJob's
// decoration timeout (see detect-api-changes.tpl) so that a stalled run fails here,
// with this tool's own error and closing tally, rather than being interrupted by
// Prow. Timeout plus grace period fit inside the template's 30m retry interval, so a
// hung run has ended before its retry is due, let alone the next 24h run.
const detectAPIChangesRunTimeout = 20 * time.Minute

var (
	OptControllersRoot string
	OptMaxOpenIssues   int
	OptModelCacheDir   string
	OptDryRun          bool
	OptDryRunOutputDir string
)

// dryRunCaveat states what a dry run has not established. It is logged once at the end
// of a dry run rather than left for a reader to work out, because the first of the two is
// a genuine hole in the feature rather than a caution: errCannotLabelIssues is raised
// inside createGithubIssueWithClient *after* the POST, by re-reading the created issue, so
// a run that never posts cannot reach that check at all. A clean dry run is therefore no
// evidence that a real run will produce issues any later run can find — which is exactly
// the conclusion somebody onboarding a service would otherwise draw from it.
const dryRunCaveat = "DRY RUN caveats: nothing was written, so two things stay " +
	"unverified. Whether the token can label issues — that check runs after the issue " +
	"is POSTed, by re-reading it, so a dry run cannot reach it and a clean preview does " +
	"not prove a real run will file issues a later run can find. And whether any write " +
	"would succeed at all: permissions, rate limits, a locked issue."

func init() {
	detectAPIChangesCMD.PersistentFlags().StringVar(
		&OptJobsConfigPath, "jobs-config-path", "jobs_config.yaml",
		"path to jobs_config.yaml where jobs configurations are stored",
	)
	detectAPIChangesCMD.PersistentFlags().StringVar(
		&OptControllersRoot, "controllers-root", "..",
		"directory containing the <service>-controller clones",
	)
	detectAPIChangesCMD.PersistentFlags().StringVar(
		&OptGithubIssueOwner, "github-issues-owner", "aws-controllers-k8s",
		"user/org that owns the github repository where the issue will be created",
	)
	detectAPIChangesCMD.PersistentFlags().StringVar(
		&OptGithubIssueRepo, "github-issues-repo", "community",
		"repository where the github issue will be created",
	)
	// Default 1, deliberately conservative. The ProwJob template is intended to
	// supply this explicitly from api_notification_max_open_issues — no template
	// exists yet, so today the default applies to every invocation, and for a
	// hand-run one the safe value is the one that writes least. Note the cap is
	// NOT read from jobs_config.yaml at runtime: the config-time validator bounds
	// it against the notified-service list, and that validator is not on the
	// runtime path, so a config-read cap would arrive unvalidated in the unbounded
	// direction. detectAPIChanges logs a warning when the two disagree, which is
	// the diagnostic that replaces reading it.
	detectAPIChangesCMD.PersistentFlags().IntVar(
		&OptMaxOpenIssues, "max-open-issues", 1,
		"maximum number of open ack/api-change-detected issues this tool may hold open",
	)
	detectAPIChangesCMD.PersistentFlags().StringVar(
		&OptModelCacheDir, "model-cache-dir", "",
		"directory for cached AWS API model JSON; defaults to a temp dir",
	)
	// Reads still happen: the two issue searches run, because what the job would do
	// depends on what is already open. Only the three writes are suppressed.
	detectAPIChangesCMD.PersistentFlags().BoolVar(
		&OptDryRun, "dry-run", false,
		"report what would be filed without creating, updating, or commenting on any issue",
	)
	// Said in the help rather than fixed by clearing the directory. MkdirAll leaves
	// prior contents, so a re-run after a controller has caught up leaves yesterday's
	// ec2.md beside today's files, asserting findings that no longer exist and
	// indistinguishable from current output. Deleting `*.md` would be the tidy fix and
	// is the wrong one: this is a user-supplied path, so pointed at a docs directory it
	// destroys unrelated files. The per-service "wrote the would-be ... issue body to
	// ..." lines already say exactly which files a run produced.
	detectAPIChangesCMD.PersistentFlags().StringVar(
		&OptDryRunOutputDir, "dry-run-output-dir", "",
		"with --dry-run, write each service's would-be issue body to <dir>/<service>.md; "+
			"existing files are left alone, so check the run's log for which ones it wrote",
	)
	rootCmd.AddCommand(detectAPIChangesCMD)
}

func detectAPIChanges(cmd *cobra.Command, args []string) error {
	// 7 of 9 sibling commands set this. It matters more here than for most: this
	// job runs unattended under Prow alongside other output in the same stream,
	// and its log is the only thing it produces.
	log.SetPrefix("detect-api-changes: ")

	// cmd is nil when a test calls this directly. Under Prow, Execute leaves Cobra's
	// context as Background, so the run deadline and SIGTERM/SIGINT are what actually
	// end a stalled run: the entrypoint interrupts the process at the job's
	// decoration timeout, and this lets in-flight requests return instead of being
	// killed mid-write after the grace period.
	ctx := context.Background()
	if cmd != nil && cmd.Context() != nil {
		ctx = cmd.Context()
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, detectAPIChangesRunTimeout)
	defer cancel()

	services, configuredCap, err := getAPINotificationServices(OptJobsConfigPath)
	if err != nil {
		return err
	}
	if len(services) == 0 {
		log.Printf("no services in api_notification_services; nothing to do")
		return nil
	}
	// Checked here rather than left to reconcileIssue, which rejects a non-positive
	// cap per service. That meant --max-open-issues 0 ran the complete analysis for
	// every service first — a paginated ref listing plus up to two model fetches each
	// — and then reported N per-service failures as *analysis* failures, which is
	// false: the analysis succeeded and the flag was invalid.
	if OptMaxOpenIssues <= 0 {
		return fmt.Errorf(
			"--max-open-issues must be positive, got %d; 0 would mean unlimited filing",
			OptMaxOpenIssues)
	}
	// Rejected rather than ignored. The output directory is the whole reason somebody
	// passes it, so accepting it and writing nothing leaves them waiting for files that
	// never arrive and concluding the detector found nothing — on a run that may well
	// have filed issues for real.
	if OptDryRunOutputDir != "" && !OptDryRun {
		return fmt.Errorf(
			"--dry-run-output-dir is only meaningful with --dry-run; " +
				"add --dry-run, or drop the directory if you meant to file issues")
	}
	// Created once here rather than left to the loop, which would otherwise report the
	// same missing directory as a write failure per service and bury the one real cause
	// under ~74 copies of its symptom.
	if OptDryRunOutputDir != "" {
		if err := os.MkdirAll(OptDryRunOutputDir, 0o755); err != nil {
			return fmt.Errorf("unable to create --dry-run-output-dir %s: %s",
				OptDryRunOutputDir, err)
		}
	}
	// Informational only: the flag stays authoritative because the validator that bounds
	// the cap runs at generation time and is not on this path. But silently disagreeing
	// with the one value a human edits is how an operator ends up reading
	// "cap of 1 reached" against a config that says 10.
	if configuredCap != 0 && configuredCap != OptMaxOpenIssues {
		log.Printf("WARNING --max-open-issues is %d but api_notification_max_open_issues is %d; "+
			"the flag wins. Regenerate the ProwJob if the config is the one you meant to change.",
			OptMaxOpenIssues, configuredCap)
	}
	log.Printf("checking %d services for AWS API changes", len(services))
	// Stated up front as well as in the closing tally, because a reader scanning the
	// per-service lines from the top needs to know which of them describe writes that
	// happened. The caveats go at the end, where they qualify the result.
	if OptDryRun {
		log.Printf("DRY RUN: no issue will be created, updated or commented on")
	}

	client, err := newGithubClientFromEnv()
	if err != nil {
		return err
	}

	cacheDir := OptModelCacheDir
	if cacheDir == "" {
		cacheDir, err = os.MkdirTemp("", "ack-api-models-")
		if err != nil {
			return fmt.Errorf("unable to create model cache dir: %s", err)
		}
		defer os.RemoveAll(cacheDir)
	}

	botLogin, err := githubLogin(ctx, client)
	if err != nil {
		return err
	}
	// The known-service set is what makes attribution exact: an issue is only
	// claimed when exactly one `service/<name>` label matches a service this run
	// knows about. Taking the first `service/` label instead let a bot issue for s3
	// that a maintainer also tagged `service/s3control` key under either one,
	// filing a duplicate s3 issue and overwriting the s3 report with s3control's.
	knownServices := make(map[string]bool, len(services))
	for _, service := range services {
		knownServices[service] = true
	}

	// One listing for the whole run, not one search per service: GitHub allows 30
	// search requests a minute, and a per-service lookup made a run cost 1+N of
	// them, which 403s about a third of the way through a full ~74-service
	// rollout. This also resolves every service's existing issue in one pass.
	existingByService, openCount, closedFingerprints, warnings, err := listAPIChangeIssues(
		ctx, client, OptGithubIssueOwner, OptGithubIssueRepo, botLogin, knownServices)
	if err != nil {
		return err
	}
	// botLogin is logged because the search is scoped by `author:`. Rotating
	// GITHUB_TOKEN to a different machine account orphans every issue filed under
	// the old one and re-files each service once; without this line that shows up
	// as an unexplained mass re-file.
	log.Printf("%d open %s issues owned by %s; cap is %d",
		openCount, apiChangeLabel, botLogin, OptMaxOpenIssues)

	// Surfaced rather than silently ignored. A duplicate means two open issues for
	// one service, so one is being left to rot; an attribution warning means an
	// issue carries no known `service/` label or several, so the detector has
	// deliberately not claimed it. Either way the run continues, but a human needs
	// to resolve it or that issue stays unmanaged.
	for _, warning := range warnings {
		log.Printf("WARNING needs a human: %s", warning)
	}

	// One cache for the run, shared by every service, for the same reason fetchModel
	// caches models: see latestVersionCache.
	latestVersions := newLatestVersionCache()
	analyze := func(ctx context.Context, service string) ([]Finding, string, string, error) {
		return analyzeService(ctx, client, latestVersions, OptControllersRoot, cacheDir, service)
	}

	analysisFailures, writeFailures, skippedAtCap, err := reconcileServices(
		ctx, client, OptGithubIssueOwner, OptGithubIssueRepo,
		services, existingByService, closedFingerprints,
		OptMaxOpenIssues, openCount, analyze,
		OptDryRun, OptDryRunOutputDir,
	)
	return runError(err, OptMaxOpenIssues, skippedAtCap, analysisFailures, writeFailures, OptDryRun)
}

// runError folds every reason a run has to fail into a single error, because Prow
// surfaces exactly one: the returned error is the job's failure reason and the last
// line of the log.
//
// Returning the first reason and dropping the rest was actively misleading. When the
// cap binds *and* a controller fails to parse — the normal state of a rollout — only
// the cap message came back, so a reader concluded there was nothing to fix and never
// learned three controllers had failed. Prow periodics do not retry, so the exit code
// carries no information beyond red/green and this message is the only channel that
// distinguishes these conditions. During a multi-week rollout the job is red by design
// every day, which is exactly when a real failure must not be hidden.
//
// abortErr is wrapped with %w rather than flattened, so errors.Is still finds
// errCannotLabelIssues in the returned error, and the failures accumulated before the
// abort ride along instead of being discarded with the rest of the loop.
//
// The order is abort → analysis → write → cap, and it is chosen for a reader who sees
// only the beginning. Prow's deck truncates a job's failure description, and during a
// multi-week rollout the cap message is red *by design* every day — so leading with it
// spent the visible prefix on the one reason that is expected, hiding the failures
// behind it. The abort stays first because it is the only reason that means the run did
// not finish. The cap's remediation sentence moves to the very end of the joined
// message for the same reason: it is advice, not a diagnosis.
//
// dryRun drops the cap reason and its remediation sentence, and nothing else. A preview
// that exits non-zero for the expected condition is one nobody runs twice, and the cap
// binding is the expected condition during a rollout — reconcileServices has already
// named every skipped service in the log, so the information is not lost, only demoted
// out of the exit code. Analysis and write failures still fail a dry run: a controller
// that cannot be analysed means the preview is incomplete, and the oversized-merge guard
// lands in writeFailures because it reports a refresh that genuinely cannot happen — a
// real finding a dry run is there to surface, not a consequence of previewing.
func runError(
	abortErr error,
	maxOpen int,
	skippedAtCap, analysisFailures, writeFailures []string,
	dryRun bool,
) error {
	var reasons []string
	// Named apart from the write failures because the remedies point in opposite
	// directions. An analysis failure sends the reader to the model differ or the
	// controller checkout; a write failure — an oversized merged body, a failed
	// comment, a failed PATCH — sends them to the issue itself, and reporting a 422
	// on a 65,536-byte body as "failed to analyze" sent them to the wrong place.
	if len(analysisFailures) > 0 {
		reasons = append(reasons, fmt.Sprintf("failed to analyze services: %v", analysisFailures))
	}
	if len(writeFailures) > 0 {
		reasons = append(reasons, fmt.Sprintf("failed to file or refresh issues for: %v", writeFailures))
	}
	remediation := ""
	if len(skippedAtCap) > 0 && !dryRun {
		reasons = append(reasons, fmt.Sprintf(
			"open-issue cap of %d reached; no issue filed for: %v", maxOpen, skippedAtCap))
		remediation = ". Work down the backlog or raise api_notification_max_open_issues"
	}
	joined := strings.Join(reasons, "; ")

	switch {
	case abortErr != nil && joined != "":
		return fmt.Errorf("%w; also %s%s", abortErr, joined, remediation)
	case abortErr != nil:
		return abortErr
	case joined != "":
		return errors.New(joined + remediation)
	}
	return nil
}

// serviceAnalyzer produces one service's findings and the versions compared. Injected so
// the reconcile loop can be tested without a controller checkout or a model fetch.
type serviceAnalyzer func(ctx context.Context, service string) ([]Finding, string, string, error)

// reconcileServices walks the services in order and reconciles each one's issue,
// returning the services that failed analysis, the ones that failed a write, and the
// ones the cap turned away.
//
// Services are processed in api_notification_services order, not map order, so that
// when the cap allows fewer new issues than there are candidates, which services win is
// deterministic across runs.
//
// Extracted from detectAPIChanges so that it can be tested at all: that function calls
// newGithubClientFromEnv itself, leaving no seam for the httptest-backed client the rest
// of this package's tests use. Three invariants live only here and can be expressed
// nowhere else — the `openCount++` on issueCreated that reconcileIssue documents but
// cannot enforce, the errCannotLabelIssues abort, and the zero-findings fall-through
// that reaches issueStaleOpenIssue. The last of those shipped broken precisely because
// this loop had no test.
//
// openCount is taken by value and incremented locally: reconcileIssue never mutates it,
// so without the increment below every service in one run creates an issue regardless of
// the cap.
//
// dryRun is passed straight to reconcileIssue, which suppresses exactly the three GitHub
// writes and leaves every decision intact — so the counts below are the counts a real run
// over the same inputs would report, which is the only reason a preview is worth reading.
// What changes here is the wording of the tally and the two caveats appended to it: see
// dryRunCaveat for what a dry run cannot establish, and reconcileIssue for why.
//
// outputDir is where a dry run writes each service's would-be issue body, or "" for none.
// It is honoured only under dryRun; detectAPIChanges rejects the flag combination that
// would otherwise reach here.
func reconcileServices(
	ctx context.Context,
	client *github.Client,
	owner, repo string,
	services []string,
	existingByService map[string]*github.Issue,
	closedFingerprints map[string]map[string]bool,
	maxOpen, openCount int,
	analyze serviceAnalyzer,
	dryRun bool,
	outputDir string,
) (analysisFailures, writeFailures, skippedAtCap []string, err error) {
	var attempted, created, updated, unchanged, stale, suppressed int
	// Recorded rather than inferred from the counts. attempted is already incremented
	// for the aborting service, so "did we stop early?" cannot be derived from
	// attempted < len(services): when the abort landed on the last service that
	// comparison was false and the summary claimed full scope with zero write failures,
	// while an orphaned unlabelled issue had just been filed on a public repo.
	var aborted bool

	// One closing line, deferred. On success the last thing in the log was otherwise
	// whatever the final service happened to print, and for a job whose only output is a
	// log this tally is the highest-value line in the file. Deferred rather than placed
	// at the end so it also lands on the abort path, where how far the run got before
	// stopping is the first thing an operator needs — which is also why the count of
	// services attempted is named there rather than the length of the list, since an
	// abort after one service otherwise reports the full list against all-zero counts.
	defer func() {
		scope := fmt.Sprintf("%d services", len(services))
		if aborted || attempted < len(services) {
			scope = fmt.Sprintf("%d of %d services (run stopped early)", attempted, len(services))
		}
		// The aborting service is counted here and nowhere else: putting it in
		// writeFailures would make runError report it twice, once as the abort and once
		// in the list, but leaving it out of every bucket left the tally one short of
		// attempted. Every attempted service lands in exactly one bucket, so these
		// numbers sum to the scope's count — a summary whose numbers do not add up is
		// worse than one that omits a category.
		abortedCount := 0
		if aborted {
			abortedCount = 1
		}
		// A dry run must not claim it acted, so the two buckets that stand for writes
		// are worded as intentions and the line is marked. The other buckets are already
		// true of both modes — nothing is written for an unchanged, stale, suppressed or
		// capped service either way — and every number keeps its value and its position,
		// which is what lets a reader hold a preview against the real run.
		prefix, createdLabel, updatedLabel := "", "created", "updated"
		if dryRun {
			prefix, createdLabel, updatedLabel = "DRY RUN ", "would create", "would update"
		}
		log.Printf("%s%s: %d %s, %d %s, %d unchanged, %d stale, "+
			"%d suppressed, %d skipped at cap, %d analysis failures, %d write failures, "+
			"%d aborted",
			prefix, scope, created, createdLabel, updated, updatedLabel,
			unchanged, stale, suppressed,
			len(skippedAtCap), len(analysisFailures), len(writeFailures), abortedCount)
		// Once per run, straight after the tally, rather than once per service: it is a
		// property of the mode, not of any service, and repeated ~74 times it becomes
		// something a reader skips — which defeats the point of saying it at all. After
		// the tally because it qualifies the tally.
		if dryRun {
			log.Print(dryRunCaveat)
		}
	}()

	for _, service := range services {
		attempted++
		findings, baselineVersion, latestVersion, analyzeErr := analyze(ctx, service)
		if analyzeErr != nil {
			log.Printf("ERROR %s: %s", service, analyzeErr)
			analysisFailures = append(analysisFailures, service)
			continue
		}
		// Dropped operations ride along in findings only so the body can list them;
		// every decision and count below is about the reportable ones. Otherwise a
		// service whose only change is a new Start* operation would file an issue.
		reported := len(reportable(findings))
		// New operations are counted in reported but are not on their own a reason to
		// file or refresh: eligibility needs a resource or field finding. See actionable.
		eligible := len(actionable(findings)) > 0
		// Only short-circuit when there is also nothing open. A service with no findings
		// but an open issue is the issueStaleOpenIssue case: its issue asserts changes
		// that no longer exist and is holding a cap slot another service could use, and
		// skipping the call here made that outcome unreachable. Falling through
		// unconditionally instead would log "issue already up to date" for a service with
		// neither findings nor an issue, which is false.
		if !eligible && existingByService[service] == nil {
			if reported > 0 {
				log.Printf("%s: %d operation finding(s) only, no resource or field changes; "+
					"nothing to file (%s -> %s)", service, reported, baselineVersion, latestVersion)
			} else {
				log.Printf("%s: no changes (%s -> %s)", service, baselineVersion, latestVersion)
			}
			unchanged++
			continue
		}
		// Guarded, because the condition above now admits a zero-findings service that
		// has an open issue. "0 findings" would read as a detector failure rather than as
		// the stale-issue line the switch below prints for it.
		if eligible {
			log.Printf("%s: %d findings (%s -> %s)", service, reported, baselineVersion, latestVersion)
		}

		// Re-read the open issue now, after analysis and immediately before anything is
		// compared against or merged into its body. The listing ran before the first
		// service was analysed, so its copy can be minutes old, and a maintainer note
		// added since would be missing from the merged body and overwritten by the
		// PATCH. Only when there are findings: the stale-issue path writes nothing, so
		// the listed copy serves it and the GET would be a wasted request.
		//
		// A read, so it runs under dryRun too, and the preview below merges into the
		// same body a real run would.
		existing := existingByService[service]
		if existing != nil && reported > 0 {
			fresh, refetchErr := refetchManagedIssue(ctx, client, owner, repo, service, existing)
			if refetchErr != nil {
				log.Printf("ERROR %s: %s", service, refetchErr)
				writeFailures = append(writeFailures, service)
				continue
			}
			existing = fresh
		}

		// Written before reconcileIssue decides, and for every service with findings
		// rather than only the ones that would be written to. A body refused by the cap,
		// suppressed by a closed issue or too large to merge back is exactly the body
		// somebody wants to read — "what would this have said?" is the question a
		// preview exists to answer, and tying the file to a particular outcome would
		// withhold it in the cases that most need review.
		//
		// renderIssueBody is pure and reconcileIssue calls it with the same arguments, so
		// the region below is the same one a real run would render. What gets written is
		// that region resolved down the path this service would actually take; the tests
		// pin both paths.
		if dryRun && outputDir != "" && eligible {
			// The service name is validated against the AWS service list before it
			// reaches here, so it is a single path segment.
			path := filepath.Join(outputDir, service+".md")
			body, _ := renderIssueBody(service, baselineVersion, latestVersion, findings)
			// What would actually be posted, which differs by path: a create posts the
			// rendered body, a refresh posts that region spliced into the existing issue.
			// Previewing the region alone for a refresh hid the maintainer's text — 729
			// bytes in the file against 797 posted — so an operator reading it would
			// conclude the refresh was about to delete a note it in fact preserves. The
			// inverse is worse: reasoning from the file's size that a refresh fits when
			// the merged body is over the limit and the PATCH cannot land.
			//
			// The oversized case stays previewable. reconcileIssue refuses that refresh
			// and the service lands in writeFailures, but the file still shows the merged
			// body that could not be posted, which is what a reader needs in order to see
			// why — so this is written before reconcileIssue decides, not after.
			preview, previewKind := body, "new"
			if existing != nil {
				// An unmanageable body has no merged form to show, so the region alone
				// is written and labelled as such; reconcileIssue then refuses the
				// refresh and the service lands in writeFailures, as in a real run.
				if merged, mergeErr := replaceGeneratedRegion(existing.GetBody(), body); mergeErr != nil {
					previewKind = "unmergeable (region only)"
				} else {
					preview, previewKind = merged, "refreshed"
				}
			}
			if writeErr := os.WriteFile(path, []byte(preview), 0o644); writeErr != nil {
				// A write failure, and it fails the run in dry-run too: the preview
				// somebody asked for does not exist, and silently carrying on leaves
				// them reading a directory that is missing the file they came for.
				//
				// continue, for two reasons. It keeps this service in exactly one
				// bucket, which is what makes the deferred tally sum to the services
				// attempted. And skipping reconcileIssue means this service reaches no
				// decision and so never consumes a cap slot — which shifts the
				// decisions every service after it reaches away from what a live run
				// would do. That is the right trade for a filesystem error on a preview
				// whose output is already incomplete, but it is the reason not to
				// "simplify" this into a fall-through.
				log.Printf("ERROR %s: unable to write dry-run body: %s", service, writeErr)
				writeFailures = append(writeFailures, service)
				continue
			}
			// Naming which of the two it is, because the file contents differ and a
			// reader comparing a refresh preview against the current issue needs to know
			// they are looking at the whole merged body rather than the region alone.
			log.Printf("%s: wrote the would-be %s issue body to %s", service, previewKind, path)
		}

		outcome, reconcileErr := reconcileIssue(
			ctx, client, owner, repo,
			service, baselineVersion, latestVersion, findings,
			existing, closedFingerprints[service],
			maxOpen, openCount, dryRun,
		)
		if reconcileErr != nil {
			// A credential that cannot label is not a per-service problem: every
			// remaining service would file an issue that no later run can find,
			// because the listing keys on a label that never got applied. One
			// invisible orphan is a bad day; one per service per day is issue spam
			// on a public repo that the cap cannot even see. Stop the run — and hand
			// back the failures collected so far, so the caller can report them
			// alongside the abort instead of losing them.
			if errors.Is(reconcileErr, errCannotLabelIssues) {
				// Set immediately before the return, so the deferred summary states the
				// run stopped instead of deducing it from a count this service has
				// already been added to.
				aborted = true
				return analysisFailures, writeFailures, skippedAtCap,
					fmt.Errorf("aborting after %s: %w", service, reconcileErr)
			}
			// A create whose response was lost may still have filed the issue, so it
			// holds a cap slot as though it had. Not counting it let every later
			// service create past the cap, once per lost response. Counting it is
			// enough: no later service can duplicate this one within the run, and the
			// next run's listing sees the issue if it exists.
			if errors.Is(reconcileErr, errIssueCreateIndeterminate) {
				openCount++
			}
			log.Printf("ERROR %s: %s", service, reconcileErr)
			writeFailures = append(writeFailures, service)
			continue
		}

		switch outcome {
		case issueCreated:
			// reconcileIssue never mutates openCount, so this line is the only thing
			// holding the cap. Without it a run where 40 services each need a new issue
			// files all 40 under a cap of 1.
			openCount++
			created++
			// Past tense only when it happened. These two lines are the per-service
			// half of the same rule the tally follows: a log that says "issue created"
			// on a run that wrote nothing is the one artefact somebody will quote back
			// as evidence the job filed something.
			if dryRun {
				log.Printf("%s: would file a new issue", service)
			} else {
				log.Printf("%s: issue created", service)
			}
		case issueUpdated:
			updated++
			if dryRun {
				log.Printf("%s: would refresh the existing issue", service)
			} else {
				log.Printf("%s: issue updated", service)
			}
		case issueSkippedAtCap:
			log.Printf("%s: SKIPPED, open-issue cap of %d reached; %d findings not filed",
				service, maxOpen, reported)
			skippedAtCap = append(skippedAtCap, service)
		case issueUnchanged:
			unchanged++
			log.Printf("%s: issue already up to date", service)
		case issueSuppressedByClosed:
			// Distinct from "up to date": a maintainer closed an issue for this
			// exact finding set, so staying quiet is a decision somebody made
			// rather than nothing having changed.
			suppressed++
			log.Printf("%s: suppressed, a closed issue already covers these %d findings",
				service, reported)
		case issueStaleOpenIssue:
			// An open issue exists but the controller has caught up, so the issue
			// asserts changes that no longer exist and is holding a cap slot that
			// another service could use. Nothing is written — closing somebody's
			// issue is not this job's call — but it must be visible, or the slot
			// leaks silently.
			stale++
			log.Printf("%s: open issue has no current findings (%s -> %s); it is stale "+
				"and holding a cap slot", service, baselineVersion, latestVersion)
		case issueOutcomeNone:
			// Unreachable: every path returning issueOutcomeNone returns a non-nil
			// error, which is handled above. Logged rather than ignored so that if
			// reconcileIssue ever grows a silent no-decision path it shows up here
			// instead of vanishing.
			log.Printf("%s: WARNING reconcile returned no decision and no error", service)
		}
	}

	return analysisFailures, writeFailures, skippedAtCap, nil
}

// analyzeService runs the four producers for one service and returns the
// findings along with the versions compared.
//
// controllersRoot is a parameter rather than a read of OptControllersRoot so that a
// test can point this at a fixture checkout, and so the only global state on the
// reconcile path is the flag wiring in detectAPIChanges.
func analyzeService(
	ctx context.Context,
	client *github.Client,
	latestVersions *latestVersionCache,
	controllersRoot, cacheDir, service string,
) ([]Finding, string, string, error) {
	in, err := ReadControllerInputs(controllersRoot, service)
	if err != nil {
		return nil, "", "", err
	}

	candidates, err := latestVersions.resolve(ctx, client, in.PackageName)
	if err != nil {
		return nil, "", "", err
	}

	// Two baselines. The generation model — what codegen read, at the core or
	// per-service pin in ack-generate-metadata.yaml — decides what is a gap. The
	// release model — at the service module version go.mod requires — decides what
	// is new, and is what the issue says it compares against: it is the SDK the
	// controller ships with, and it shares the latest model's tag series.
	// ec2-controller is generated from core v1.41.1 and builds against
	// service/ec2 v1.290.1.
	baselineServiceVersion := in.ServiceSDKVersion
	releaseVersion := in.GoModServiceVersion
	if releaseVersion == "" {
		releaseVersion = baselineServiceVersion
	}
	series := "service/" + in.PackageName + "/"
	baselineVersion := series + releaseVersion
	if releaseVersion == "" {
		baselineVersion = in.SDKVersion
	}

	// The latest model is fetched before the baseline so that the up-to-date case
	// still costs no model request, as it did when only the highest tag was considered.
	latestServiceVersion, latest, found, err := latestModel(
		ctx, cacheDir, in.ModelName, in.PackageName, releaseVersion, candidates,
	)
	if latestServiceVersion == "" {
		// No candidate had the model; report against the newest tag that was tried.
		latestServiceVersion = candidates[0]
	}
	latestVersion := series + latestServiceVersion
	if err != nil {
		return nil, baselineVersion, latestVersion, err
	}
	if !found {
		return nil, baselineVersion, latestVersion, nil
	}

	baselineModel, err := fetchModel(
		ctx, cacheDir, in.ModelName, in.PackageName, in.SDKVersion, baselineServiceVersion,
	)
	if err != nil {
		return nil, baselineVersion, latestVersion, err
	}

	findings := collectFindings(latest, baselineModel, in)
	if releaseVersion != "" {
		releaseModel, err := fetchModel(ctx, cacheDir, in.ModelName, in.PackageName, "", releaseVersion)
		if err != nil {
			return nil, baselineVersion, latestVersion, err
		}
		findings = markPreexisting(findings, releaseModel, in)
	}
	return findings, baselineVersion, latestVersion, nil
}

// collectFindings runs every producer over one pair of models.
func collectFindings(latest, baseline *SmithyModel, in *ControllerInputs) []Finding {
	var findings []Finding
	findings = append(findings, findNewResources(latest, baseline, in)...)
	findings = append(findings, findNewOperations(latest, baseline, in)...)
	findings = append(findings, findAddedFields(latest, baseline, in)...)
	findings = annotateFields(latest, in, mergeEvidence(foldOperationsIntoResources(findings)))
	return annotateResources(latest, in, findings)
}

// mergeEvidence makes one finding of those sharing a Kind, Class and Subject,
// with their Evidence combined. Two producers can reach one field: networkfirewall's
// AvailabilityZoneChangeProtection is new on CreateFirewall's request and is what
// the new UpdateAvailabilityZoneChangeProtection sets, and two bullets for one field
// read as two fields.
func mergeEvidence(findings []Finding) []Finding {
	type key struct {
		kind, subject string
		class         FindingClass
	}
	index := map[key]int{}
	out := make([]Finding, 0, len(findings))
	for _, f := range findings {
		k := key{f.Kind, f.Subject, f.Class}
		i, seen := index[k]
		if !seen {
			index[k] = len(out)
			out = append(out, f)
			continue
		}
		out[i].Evidence = newEvidence(append(out[i].evidenceOps(), f.evidenceOps()...))
		out[i].NewSincePin = out[i].NewSincePin || f.NewSincePin
	}
	return out
}
