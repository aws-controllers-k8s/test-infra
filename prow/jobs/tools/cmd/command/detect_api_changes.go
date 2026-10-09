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

// detectAPIChangesRunTimeout bounds one run. It is below the ProwJob's decoration
// timeout (detect-api-changes.tpl) so a stalled run fails with this tool's own
// error and tally, and with the grace period it fits inside the 30m retry interval.
const detectAPIChangesRunTimeout = 20 * time.Minute

var (
	OptControllersRoot string
	OptMaxOpenIssues   int
	OptModelCacheDir   string
	OptDryRun          bool
	OptDryRunOutputDir string
	OptMetricsNS       string
	OptMetricsRegion   string
)

// dryRunCaveat states what a dry run cannot verify. errCannotLabelIssues is only
// detected after the POST, so a clean dry run does not prove a real run will file
// issues that later runs can find.
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
	// Defaults to 1, the value that writes least. The ProwJob passes
	// api_notification_max_open_issues; the cap is not read from jobs_config.yaml
	// at runtime because only the config-time validator bounds it.
	detectAPIChangesCMD.PersistentFlags().IntVar(
		&OptMaxOpenIssues, "max-open-issues", 1,
		"maximum number of open ack/api-change-detected issues this tool may hold open",
	)
	detectAPIChangesCMD.PersistentFlags().StringVar(
		&OptModelCacheDir, "model-cache-dir", "",
		"directory for cached AWS API model JSON; defaults to a temp dir",
	)
	// Reads still run; only the create, edit and comment writes are suppressed.
	detectAPIChangesCMD.PersistentFlags().BoolVar(
		&OptDryRun, "dry-run", false,
		"report what would be filed without creating, updating, or commenting on any issue",
	)
	// Stale files from earlier runs are left in place rather than deleted, since
	// the path is user-supplied; the log names the files each run wrote.
	detectAPIChangesCMD.PersistentFlags().StringVar(
		&OptDryRunOutputDir, "dry-run-output-dir", "",
		"with --dry-run, write each service's would-be issue body to <dir>/<service>.md; "+
			"existing files are left alone, so check the run's log for which ones it wrote",
	)
	// Off by default so local and dry runs publish nothing; the ProwJob sets both.
	detectAPIChangesCMD.PersistentFlags().StringVar(
		&OptMetricsNS, "metrics-namespace", "",
		"CloudWatch namespace for the RunSucceeded metric; empty publishes nothing",
	)
	detectAPIChangesCMD.PersistentFlags().StringVar(
		&OptMetricsRegion, "metrics-region", "",
		"AWS region for the RunSucceeded metric; required with --metrics-namespace",
	)
	rootCmd.AddCommand(detectAPIChangesCMD)
}

func detectAPIChanges(cmd *cobra.Command, args []string) error {
	log.SetPrefix("detect-api-changes: ")

	// cmd is nil when a test calls this directly. Prow's entrypoint sends SIGTERM
	// at the decoration timeout; cancelling on it lets in-flight requests return
	// before the grace period ends.
	ctx := context.Background()
	if cmd != nil && cmd.Context() != nil {
		ctx = cmd.Context()
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, detectAPIChangesRunTimeout)
	defer cancel()

	if OptMetricsNS != "" && OptMetricsRegion == "" {
		return fmt.Errorf("--metrics-region is required with --metrics-namespace")
	}
	// A dry run writes nothing, and that includes the metric.
	if OptMetricsNS == "" || OptDryRun {
		return runDetectAPIChanges(ctx)
	}
	runErr := runDetectAPIChanges(ctx)
	// Best effort: a publish failure is logged, not returned, so it cannot turn a
	// good run red. The missing-data alarm catches a publisher that keeps failing.
	pubCtx, pubCancel := context.WithTimeout(context.Background(), runMetricTimeout)
	defer pubCancel()
	value := runSucceededValue(runErr)
	putter, err := newMetricPutter(pubCtx, OptMetricsRegion)
	if err == nil {
		err = publishRunSucceeded(pubCtx, putter, OptMetricsNS, value)
	}
	if err != nil {
		log.Printf("WARNING unable to publish %s/%s=%v: %s", OptMetricsNS, runSucceededMetric, value, err)
	} else {
		log.Printf("published %s/%s=%v", OptMetricsNS, runSucceededMetric, value)
	}
	return runErr
}

// runDetectAPIChanges is one run; detectAPIChanges wraps it to publish the result.
func runDetectAPIChanges(ctx context.Context) error {
	services, configuredCap, err := getAPINotificationServices(OptJobsConfigPath)
	if err != nil {
		return err
	}
	if len(services) == 0 {
		log.Printf("no services in api_notification_services; nothing to do")
		return nil
	}
	// Validated up front so a bad flag fails once instead of after analysing
	// every service.
	if OptMaxOpenIssues <= 0 {
		return fmt.Errorf(
			"--max-open-issues must be positive, got %d; 0 would mean unlimited filing",
			OptMaxOpenIssues)
	}
	// Rejected rather than ignored: without --dry-run the run would file real
	// issues and write no files.
	if OptDryRunOutputDir != "" && !OptDryRun {
		return fmt.Errorf(
			"--dry-run-output-dir is only meaningful with --dry-run; " +
				"add --dry-run, or drop the directory if you meant to file issues")
	}
	// Created once here so a bad path is one error, not one per service.
	if OptDryRunOutputDir != "" {
		if err := os.MkdirAll(OptDryRunOutputDir, 0o755); err != nil {
			return fmt.Errorf("unable to create --dry-run-output-dir %s: %s",
				OptDryRunOutputDir, err)
		}
	}
	// The flag stays authoritative; the warning explains a cap that differs from
	// the config an operator edited.
	if configuredCap != 0 && configuredCap != OptMaxOpenIssues {
		log.Printf("WARNING --max-open-issues is %d but api_notification_max_open_issues is %d; "+
			"the flag wins. Regenerate the ProwJob if the config is the one you meant to change.",
			OptMaxOpenIssues, configuredCap)
	}
	log.Printf("checking %d services for AWS API changes", len(services))
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
	// An issue is claimed only when exactly one of its `service/<name>` labels is
	// a known service; see serviceFromLabels.
	knownServices := make(map[string]bool, len(services))
	for _, service := range services {
		knownServices[service] = true
	}

	// One listing for the whole run: GitHub allows 30 search requests a minute,
	// too few for a search per service.
	existingByService, openCount, closedFingerprints, warnings, err := listAPIChangeIssues(
		ctx, client, OptGithubIssueOwner, OptGithubIssueRepo, botLogin, knownServices)
	if err != nil {
		return err
	}
	// botLogin is logged because the search is scoped by `author:`; a token for a
	// different account orphans existing issues and re-files every service.
	log.Printf("%d open %s issues owned by %s; cap is %d",
		openCount, apiChangeLabel, botLogin, OptMaxOpenIssues)

	// Duplicates and unattributable issues are left unmanaged until a human
	// resolves them.
	for _, warning := range warnings {
		log.Printf("WARNING needs a human: %s", warning)
	}

	// Logged at both ends of the run so the closing tally carries it too.
	codegenWarning := codeGeneratorReleaseWarning(ctx, client, codeGeneratorVersion)
	if codegenWarning != "" {
		log.Printf("WARNING %s", codegenWarning)
	}
	log.Printf("deciding what regeneration adds with code-generator %s", codeGeneratorVersion)

	// Shared by every service in the run; see latestVersionCache.
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
	if codegenWarning != "" {
		log.Printf("WARNING %s", codegenWarning)
	}
	return runError(err, OptMaxOpenIssues, skippedAtCap, analysisFailures, writeFailures, OptDryRun)
}

// runError joins every reason a run failed into one error, since Prow shows only
// the returned error. abortErr is wrapped with %w so errors.Is still matches it.
//
// Order is abort, analysis, write, cap: Deck truncates the failure description,
// and the cap binds by design during a rollout, so it and its remediation go last.
// A dry run omits the cap reason, which reconcileServices has already logged.
func runError(
	abortErr error,
	maxOpen int,
	skippedAtCap, analysisFailures, writeFailures []string,
	dryRun bool,
) error {
	var reasons []string
	// Kept apart because the fixes differ: analysis failures point at the
	// controller or model, write failures at the issue.
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
	case len(reasons) == 1 && remediation != "":
		return capReachedError(joined + remediation)
	case joined != "":
		return errors.New(joined + remediation)
	}
	return nil
}

// serviceAnalyzer returns one service's findings and the versions compared. It is
// injected so the reconcile loop can be tested without a checkout or model fetch.
type serviceAnalyzer func(ctx context.Context, service string) ([]Finding, string, string, error)

// reconcileServices reconciles each service's issue in api_notification_services
// order, so which services the cap admits is deterministic. It returns the services
// that failed analysis, failed a write, or were skipped at the cap.
//
// reconcileIssue never mutates openCount; this loop increments it, which is what
// enforces the cap within a run. dryRun suppresses only the writes, so the tally
// matches what a real run would report. outputDir, used only under dryRun, receives
// each service's would-be issue body.
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
	// Tracked explicitly: an abort on the last service leaves attempted equal to
	// len(services).
	var aborted bool
	// The context error that stopped the run before every service was attempted.
	var cutShort error

	// Deferred so the closing tally is also logged on the abort path.
	defer func() {
		scope := fmt.Sprintf("%d services", len(services))
		switch {
		case cutShort != nil:
			scope = fmt.Sprintf("%d of %d services (run cut short: %v)", attempted, len(services), cutShort)
		case aborted || attempted < len(services):
			scope = fmt.Sprintf("%d of %d services (run stopped early)", attempted, len(services))
		}
		// Every attempted service lands in exactly one bucket. The aborting service
		// gets its own, since writeFailures would make runError report it twice.
		abortedCount := 0
		if aborted {
			abortedCount = 1
		}
		// A dry run words the write buckets as intentions; numbers and their order
		// stay the same so a preview can be compared with a real run.
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
		if dryRun {
			log.Print(dryRunCaveat)
		}
	}()

	// The same set listAPIChangeIssues attributed against, for the revalidation
	// before each PATCH.
	knownServices := make(map[string]bool, len(services))
	for _, service := range services {
		knownServices[service] = true
	}

	for _, service := range services {
		// Checked here, not only in requests: analysis scans the controller checkout
		// before its first request, so a cancelled run would otherwise keep going.
		if ctxErr := ctx.Err(); ctxErr != nil {
			cutShort = ctxErr
			return analysisFailures, writeFailures, skippedAtCap,
				fmt.Errorf("run cut short before %s, %d of %d services not attempted: %w",
					service, len(services)-attempted, len(services), ctxErr)
		}
		attempted++
		findings, baselineVersion, latestVersion, analyzeErr := analyze(ctx, service)
		// Checked again here: a cancel during the last service's analysis would
		// otherwise reach the final return as success. Nothing is written for it.
		if ctxErr := ctx.Err(); ctxErr != nil {
			cutShort = ctxErr
			if analyzeErr != nil {
				log.Printf("ERROR %s: %s", service, analyzeErr)
			}
			return analysisFailures, writeFailures, skippedAtCap,
				fmt.Errorf("run cut short while analysing %s, %d of %d services not attempted: %w",
					service, len(services)-attempted, len(services), ctxErr)
		}
		if analyzeErr != nil {
			log.Printf("ERROR %s: %s", service, analyzeErr)
			analysisFailures = append(analysisFailures, service)
			continue
		}
		// Dropped operations stay in findings only so the body can list them.
		reported := len(reportable(findings))
		// New operations alone do not justify an issue; see actionable.
		eligible := len(actionable(findings)) > 0
		// A service with an open issue still falls through, to reach the
		// issueStaleOpenIssue case.
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
		// A stale issue gets its own log line from the switch below.
		if eligible {
			log.Printf("%s: %d findings (%s -> %s)", service, reported, baselineVersion, latestVersion)
		}

		// Re-read the issue just before merging into it: the listed copy can be
		// minutes old, and a maintainer edit since then would be overwritten. The
		// stale-issue path (not eligible, even with operation findings) writes
		// nothing, so it skips the GET.
		existing := existingByService[service]
		if existing != nil && eligible {
			fresh, refetchErr := refetchManagedIssue(ctx, client, owner, repo, service, knownServices, existing)
			if refetchErr != nil {
				log.Printf("ERROR %s: %s", service, refetchErr)
				writeFailures = append(writeFailures, service)
				continue
			}
			existing = fresh
		}

		// Written before reconcileIssue decides, so bodies the cap, a closed issue
		// or the size limit would block can still be reviewed.
		if dryRun && outputDir != "" && eligible {
			// The service name is validated against the AWS service list, so it is a
			// single path segment.
			path := filepath.Join(outputDir, service+".md")
			body, _ := renderIssueBody(service, baselineVersion, latestVersion, findings)
			// Preview what would be posted: the rendered body for a create, or the
			// region spliced into the existing body for a refresh.
			preview, previewKind := body, "new"
			if existing != nil {
				// Rendered into what the existing outside text leaves, as reconcileIssue
				// does. A body with no mergeable region gets the region alone.
				region, _ := renderIssueRegion(service, baselineVersion, latestVersion, findings,
					githubMaxIssueBody-bytesOutsideRegion(existing.GetBody()))
				if merged, mergeErr := replaceGeneratedRegion(existing.GetBody(), region); mergeErr != nil {
					previewKind = "unmergeable (region only)"
				} else {
					preview, previewKind = merged, "refreshed"
				}
			}
			if writeErr := os.WriteFile(path, []byte(preview), 0o644); writeErr != nil {
				// Counted as a write failure, then skipped so the service stays in one
				// tally bucket. Skipping means it takes no cap slot, so later services
				// can diverge from a live run.
				log.Printf("ERROR %s: unable to write dry-run body: %s", service, writeErr)
				writeFailures = append(writeFailures, service)
				continue
			}
			log.Printf("%s: wrote the would-be %s issue body to %s", service, previewKind, path)
		}

		outcome, reconcileErr := reconcileIssue(
			ctx, client, owner, repo,
			service, baselineVersion, latestVersion, findings,
			existing, closedFingerprints[service], knownServices,
			maxOpen, openCount, dryRun,
		)
		if reconcileErr != nil {
			// A token that cannot label would orphan an unlabelled issue for every
			// remaining service, so stop and return the failures so far.
			if errors.Is(reconcileErr, errCannotLabelIssues) {
				aborted = true
				return analysisFailures, writeFailures, skippedAtCap,
					fmt.Errorf("aborting after %s: %w", service, reconcileErr)
			}
			// A create whose response was lost may still have filed the issue, so
			// it holds a cap slot.
			if errors.Is(reconcileErr, errIssueCreateIndeterminate) {
				openCount++
			}
			log.Printf("ERROR %s: %s", service, reconcileErr)
			writeFailures = append(writeFailures, service)
			continue
		}

		switch outcome {
		case issueCreated:
			// The only increment that enforces the cap within a run.
			openCount++
			created++
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
		case issueRelabelled:
			// The orphan was not in the listing, so it takes a cap slot now.
			openCount++
			updated++
			if dryRun {
				log.Printf("%s: would label the unlabelled issue an earlier run filed", service)
			} else {
				log.Printf("%s: labelled the unlabelled issue an earlier run filed", service)
			}
		case issueSkippedAtCap:
			log.Printf("%s: SKIPPED, open-issue cap of %d reached; %d findings not filed",
				service, maxOpen, reported)
			skippedAtCap = append(skippedAtCap, service)
		case issueUnchanged:
			unchanged++
			log.Printf("%s: issue already up to date", service)
		case issueSuppressedByClosed:
			// A maintainer closed an issue for this exact finding set.
			suppressed++
			log.Printf("%s: suppressed, a closed issue already covers these %d findings",
				service, reported)
		case issueStaleOpenIssue:
			// The controller has caught up but the issue is still open and holds a
			// cap slot. It is logged, not closed; closing is left to a human.
			stale++
			log.Printf("%s: open issue has no current findings (%s -> %s); it is stale "+
				"and holding a cap slot", service, baselineVersion, latestVersion)
		case issueOutcomeNone:
			// Unreachable: issueOutcomeNone always comes with an error.
			log.Printf("%s: WARNING reconcile returned no decision and no error", service)
		}
	}

	// A cancel during the last service's writes must not read as a clean run.
	if ctxErr := ctx.Err(); ctxErr != nil {
		cutShort = ctxErr
		return analysisFailures, writeFailures, skippedAtCap,
			fmt.Errorf("run cut short during the last service: %w", ctxErr)
	}
	return analysisFailures, writeFailures, skippedAtCap, nil
}

// analyzeService runs the producers for one service and returns the findings and
// the versions compared.
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

	// The baseline is the generation pin in ack-generate-metadata.yaml, which
	// build-controller.sh regenerates from; go.mod plays no part.
	series := "service/" + in.PackageName + "/"
	baselineVersion := series + in.ServiceSDKVersion
	if in.ServiceSDKVersion == "" {
		baselineVersion = in.SDKVersion
	}

	// Fetched before the baseline so an up-to-date service costs no model request.
	latestServiceVersion, latest, found, err := latestModel(
		ctx, cacheDir, in.ModelName, in.PackageName, in.ServiceSDKVersion, candidates,
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
		ctx, cacheDir, in.ModelName, in.PackageName, in.SDKVersion, in.ServiceSDKVersion,
	)
	if err != nil {
		return nil, baselineVersion, latestVersion, err
	}

	// Run only when the model moved, so an up-to-date service costs no codegen.
	view, err := runCodegenOracle(in, baselineModel, latest)
	if err != nil {
		return nil, baselineVersion, latestVersion, err
	}
	findings := reconcileWithCodegen(latest, collectFindings(latest, baselineModel, in), in, view)
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

// mergeEvidence merges findings that share a Kind, Class and Subject, combining
// their Evidence, because two producers can report the same field.
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
		out[i].SDKPaths = joinSDKPaths(out[i].SDKPaths, f.SDKPaths)
		out[i].NewSincePin = out[i].NewSincePin || f.NewSincePin
	}
	return out
}
