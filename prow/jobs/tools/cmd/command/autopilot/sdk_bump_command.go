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

package autopilot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/go-github/v63/github"
	"github.com/spf13/cobra"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/sdkbump"
)

type sdkBumpFlags struct {
	service, sdkTo, workItem, changeClass, mode                               string
	fromLedger, write                                                         bool
	controllerDir, codegenRepo, runtimeRepo, ackGenerate, cacheDir, ledgerDir string
	org, remote, botName, botEmail, botLogin                                  string
	artifactsDir, resultFile                                                  string
	timeout                                                                   time.Duration
}

var sdkBumpOpts sdkBumpFlags

var sdkBumpCmd = &cobra.Command{
	Use:   "sdk-bump",
	Short: "bump one controller's SDK model, gate it with ack-dod, and publish a draft PR (no LLM)",
	Long: `sdk-bump is ACK Autopilot's sdk-bump workflow (Plan 6). It regenerates the
controller at the target SDK model with the code-generator and runtime it was last
generated with, commits as the bot, runs ack-dod, and force-pushes (with a lease)
only to autopilot/<service>/sdk-bump. Without --write it writes nothing to GitHub
and records needs_human/DRY_RUN_NO_WRITES; the plan goes to plan.txt.

Exit codes: 0 pr_opened or completed_no_change; 1 needs_human or failed; 2 when
agent-result.json cannot be written.`,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runSDKBump(cmd.Context(), sdkBumpOpts, os.Getenv, cmd.OutOrStdout())
	},
}

func init() {
	f := sdkBumpCmd.Flags()
	f.StringVar(&sdkBumpOpts.service, "service", "", "service alias, e.g. s3")
	f.StringVar(&sdkBumpOpts.sdkTo, "sdk-to", "", "target model, service/<package>/vX.Y.Z (the item's sdk_to)")
	f.StringVar(&sdkBumpOpts.workItem, "work-item", "", "the SDK-bump item's key hash (sha256:<16 hex>)")
	f.StringVar(&sdkBumpOpts.changeClass, "change-class", "", "the item's change class; only SDK-bump")
	f.BoolVar(&sdkBumpOpts.fromLedger, "from-ledger", false, "take --sdk-to and --work-item from the service's one open SDK-bump item in --ledger-dir")
	f.StringVar(&sdkBumpOpts.mode, "mode", sdkbump.ModeBump, "bump, or refresh (re-evaluate the open PR's merge block only)")
	f.StringVar(&sdkBumpOpts.controllerDir, "controller-dir", "", "clean clone of <org>/<service>-controller with main checked out")
	f.StringVar(&sdkBumpOpts.codegenRepo, "codegen-repo", "", "full clone of code-generator (the run checks out the controller's pin)")
	f.StringVar(&sdkBumpOpts.runtimeRepo, "runtime-repo", "", "full clone of runtime (the run checks out go.mod's version)")
	f.StringVar(&sdkBumpOpts.ackGenerate, "ack-generate", "", "ack-generate with crd-compat-check --output json (Plan 4)")
	f.StringVar(&sdkBumpOpts.cacheDir, "cache-dir", "", "ack-generate model cache (ACK_GENERATE_CACHE_DIR)")
	f.StringVar(&sdkBumpOpts.ledgerDir, "ledger-dir", "", "ack-private autopilot/ checkout, read only; without it the merge stays blocked")
	f.StringVar(&sdkBumpOpts.org, "org", "aws-controllers-k8s", "GitHub organization of the controller repo")
	f.StringVar(&sdkBumpOpts.remote, "remote", "", "push remote override: only a file:// path, for local tests")
	f.StringVar(&sdkBumpOpts.botName, "bot-name", "ack-bot", "commit author name")
	f.StringVar(&sdkBumpOpts.botEmail, "bot-email", "ack-bot@users.noreply.github.com", "commit author email (= ack-dod --bot-email)")
	f.StringVar(&sdkBumpOpts.botLogin, "bot-login", "ack-bot", "GitHub login that owns the bump PRs")
	f.StringVar(&sdkBumpOpts.artifactsDir, "artifacts-dir", "", "verdict.json, gate-verdict.txt, pr-body.md, plan.txt (default $ARTIFACTS)")
	f.StringVar(&sdkBumpOpts.resultFile, "result-file", "", "agent-result.json path (default <artifacts-dir>/agent-result.json)")
	f.BoolVar(&sdkBumpOpts.write, "write", false, "push to the org repo and write to GitHub (the script sets it only when AUTOPILOT_SDK_BUMP_WRITE=true)")
	f.DurationVar(&sdkBumpOpts.timeout, "timeout", 150*time.Minute, "give up after this long")
	_ = sdkBumpCmd.MarkFlagRequired("service")
	Command.AddCommand(sdkBumpCmd)
}

// defaultArtifacts is result.py's DEFAULT_ARTIFACTS.
const defaultArtifacts = "/tmp/ack-test-logs"

func runSDKBump(ctx context.Context, f sdkBumpFlags, getenv func(string) string, out io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()
	if f.artifactsDir == "" {
		f.artifactsDir = getenv("ARTIFACTS")
	}
	if f.artifactsDir == "" {
		f.artifactsDir = defaultArtifacts
	}
	resultPath := f.resultFile
	if resultPath == "" {
		resultPath = filepath.Join(f.artifactsDir, "agent-result.json")
	}
	jobID := getenv("PROW_JOB_ID")
	if !sdkbump.ValidProwJobID(jobID) {
		jobID = ""
	}
	workItem := ""
	record := func(r sdkbump.Result) error {
		if err := sdkbump.WriteResult(resultPath, workItem, jobID, r); err != nil {
			return exitError{code: 2, err: err}
		}
		return nil
	}
	finish := func(r sdkbump.Result) error {
		if err := record(r); err != nil {
			return err
		}
		fmt.Fprintf(out, "sdk-bump: recorded %s %s %s\n", r.Outcome, r.Reason, r.PR)
		if r.Outcome == sdkbump.OutcomePROpened || r.Outcome == sdkbump.OutcomeNoChange {
			return nil
		}
		return exitError{code: 1, err: fmt.Errorf("sdk-bump: %s %s", r.Outcome, r.Reason)}
	}
	// The file exists before anything can fail (Plan 3).
	if err := record(sdkbump.Result{Outcome: sdkbump.OutcomeFailed, Reason: "JOB_INCOMPLETE"}); err != nil {
		return err
	}
	invalid := func(why string) error {
		fmt.Fprintf(out, "sdk-bump: invalid input: %s\n", why)
		return finish(sdkbump.Result{Outcome: sdkbump.OutcomeNeedsHuman, Reason: "INVALID_INPUT"})
	}

	in := sdkbump.Inputs{Service: f.service, SDKTo: f.sdkTo, WorkItem: f.workItem, ChangeClass: f.changeClass}
	if f.fromLedger {
		if f.sdkTo != "" || f.workItem != "" || f.changeClass != "" || f.ledgerDir == "" {
			return invalid("--from-ledger needs --ledger-dir and excludes --sdk-to, --work-item and --change-class")
		}
		items, err := ledger.Load(f.ledgerDir)
		if err != nil {
			fmt.Fprintf(out, "sdk-bump: ledger: %v\n", err)
			return finish(sdkbump.Result{Outcome: sdkbump.OutcomeNeedsHuman, Reason: "LEDGER_UNAVAILABLE"})
		}
		to, wi, err := sdkbump.FromLedger(items, f.service)
		switch {
		case errors.Is(err, sdkbump.ErrNoBumpItem):
			fmt.Fprintf(out, "sdk-bump: %v\n", err)
			return finish(sdkbump.Result{Outcome: sdkbump.OutcomeNoChange, Reason: "NO_BUMP_ITEM"})
		case err != nil:
			fmt.Fprintf(out, "sdk-bump: %v\n", err)
			return finish(sdkbump.Result{Outcome: sdkbump.OutcomeNeedsHuman, Reason: "MULTIPLE_BUMP_ITEMS"})
		}
		in.SDKTo, in.WorkItem, in.ChangeClass = to, wi, dod.ClassSDKBump
	}
	if sdkbump.ValidWorkItem(in.WorkItem) {
		workItem = in.WorkItem
	}

	forge, err := newSDKBumpForge(f.write, getenv("GITHUB_TOKEN"))
	if err != nil {
		return invalid(err.Error())
	}
	rep := sdkbump.Run(ctx, sdkbump.Options{
		Inputs: in, Mode: f.mode, Org: f.org,
		ControllerDir: f.controllerDir, CodegenRepo: f.codegenRepo, RuntimeRepo: f.runtimeRepo,
		ACKGenerate: f.ackGenerate, CacheDir: f.cacheDir, LedgerDir: f.ledgerDir,
		BotName: f.botName, BotEmail: f.botEmail, BotLogin: f.botLogin,
		Remote: f.remote, Write: f.write, ArtifactsDir: f.artifactsDir,
		JobName: strings.TrimSpace(getenv("JOB_NAME") + " " + getenv("BUILD_ID")), JobURL: getenv("AUTOPILOT_JOB_URL"),
		Forge: forge, Run: dod.ExecRunner, Gate: dod.Run, Log: out,
	})
	planned := rep.Planned
	if d, ok := forge.(*sdkbump.DryRunForge); ok {
		planned = append(planned, d.Planned...)
	}
	if err := os.MkdirAll(f.artifactsDir, 0o755); err == nil {
		_ = os.WriteFile(filepath.Join(f.artifactsDir, "plan.txt"), []byte(strings.Join(planned, "\n")+"\n"), 0o644)
	}
	fmt.Fprintf(out, "sdk-bump: outcome %s %s %s\n", rep.Outcome, rep.Reason, rep.PR)

	final := rep.Result
	if !f.write && final.Outcome != sdkbump.OutcomeNeedsHuman {
		fmt.Fprintf(out, "sdk-bump: dry run: a run with --write would record %s %s\n", final.Outcome, final.Reason)
		final = sdkbump.Result{Outcome: sdkbump.OutcomeNeedsHuman, Reason: "DRY_RUN_NO_WRITES"}
	} else if !f.write {
		fmt.Fprintf(out, "sdk-bump: dry run\n")
	}
	if err := record(final); err != nil {
		return err
	}
	// The exit code follows what the run found, so a dry run is green when a
	// real one would be. Dry-run results are never reconciled (shadow only).
	if rep.Outcome == sdkbump.OutcomePROpened || rep.Outcome == sdkbump.OutcomeNoChange {
		return nil
	}
	return exitError{code: 1, err: fmt.Errorf("sdk-bump: %s %s", rep.Outcome, rep.Reason)}
}

// newSDKBumpForge: without --write a DryRunForge (with a read-only reader
// when a token is set); with --write the real forge, which needs a token.
func newSDKBumpForge(write bool, token string) (sdkbump.Forge, error) {
	var real *sdkbump.GitHubForge
	if token != "" {
		real = &sdkbump.GitHubForge{Client: github.NewClient(&http.Client{Timeout: requestTimeout}).WithAuthToken(token)}
	}
	if !write {
		if real == nil {
			return &sdkbump.DryRunForge{}, nil
		}
		return &sdkbump.DryRunForge{Reader: real}, nil
	}
	if real == nil {
		return nil, errors.New("--write needs GITHUB_TOKEN")
	}
	return real, nil
}
