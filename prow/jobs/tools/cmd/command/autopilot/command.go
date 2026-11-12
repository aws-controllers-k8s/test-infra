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

// Package autopilot holds the ACK Autopilot subcommands of ack-build-tools.
package autopilot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/go-github/v63/github"
	"github.com/spf13/cobra"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/issuefinder"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

const (
	ingestTimeout  = 10 * time.Minute
	requestTimeout = 60 * time.Second
)

// Command groups the autopilot subcommands; the command package registers it.
var Command = &cobra.Command{
	Use:   "autopilot",
	Short: "autopilot - ACK Autopilot ledger tools",
}

type ingestOptions struct {
	Owner, Repo, BotLogin, TeamSlug, LedgerDir string
	DryRun                                     bool
}

// validate rejects empty values. MarkFlagRequired only checks that a flag was
// given, so --bot-login "" would otherwise get through, and an empty bot login
// must never be what decides which issues are trusted.
func (o ingestOptions) validate() error {
	for _, f := range []struct{ name, value string }{
		{"issues-owner", o.Owner},
		{"issues-repo", o.Repo},
		{"bot-login", o.BotLogin},
		{"team-slug", o.TeamSlug},
		{"ledger-dir", o.LedgerDir},
	} {
		if strings.TrimSpace(f.value) == "" {
			return fmt.Errorf("--%s must not be empty", f.name)
		}
	}
	return nil
}

// requireDir fails unless dir exists and is a directory. It does not look
// inside: an empty ledger holding only a README is valid.
func requireDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("--ledger-dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("--ledger-dir %q is not a directory", dir)
	}
	return nil
}

var (
	ingestOpts ingestOptions
	lintDir    string
)

var ingestCmd = &cobra.Command{
	Use:   "ingest-issues",
	Short: "record the findings in detect-api-changes issues as ledger work items",
	Long: `Reads every detect-api-changes issue (read-only), turns the findings block in
each trusted issue's generated region into work items, and writes them to the
ledger, then lints it. With --dry-run it prints what would change and writes
nothing. The GitHub token is read from GITHUB_TOKEN.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := ingestOpts.validate(); err != nil {
			return err
		}
		token := os.Getenv("GITHUB_TOKEN")
		if token == "" {
			return errors.New("environment variable GITHUB_TOKEN is not provided")
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), ingestTimeout)
		defer cancel()
		client := github.NewClient(&http.Client{Timeout: requestTimeout}).WithAuthToken(token)
		return ingest(ctx, client, ingestOpts, time.Now().UTC().Format(time.RFC3339), cmd.OutOrStdout())
	},
}

var lintCmd = &cobra.Command{
	Use:   "lint-ledger",
	Short: "check every work item file in a ledger",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requireDir(lintDir); err != nil {
			return err
		}
		errs := ledger.Lint(lintDir)
		for _, err := range errs {
			fmt.Fprintln(cmd.ErrOrStderr(), err)
		}
		if len(errs) > 0 {
			return fmt.Errorf("%d ledger problem(s)", len(errs))
		}
		return nil
	},
}

func init() {
	f := ingestCmd.Flags()
	f.StringVar(&ingestOpts.Owner, "issues-owner", "aws-controllers-k8s", "org that owns the issues repo and the team")
	f.StringVar(&ingestOpts.Repo, "issues-repo", "ack-private", "repo where detect-api-changes files issues")
	f.StringVar(&ingestOpts.BotLogin, "bot-login", "", "account detect-api-changes files issues as; other authors are ignored")
	f.StringVar(&ingestOpts.TeamSlug, "team-slug", "", "team whose members' /autopilot commands count")
	f.StringVar(&ingestOpts.LedgerDir, "ledger-dir", "", "the autopilot/ directory of an ack-private checkout")
	f.BoolVar(&ingestOpts.DryRun, "dry-run", false, "report changes without writing the ledger")
	for _, name := range []string{"bot-login", "team-slug", "ledger-dir"} {
		_ = ingestCmd.MarkFlagRequired(name)
	}
	lintCmd.Flags().StringVar(&lintDir, "ledger-dir", "", "the autopilot/ directory of an ack-private checkout")
	_ = lintCmd.MarkFlagRequired("ledger-dir")
	Command.AddCommand(ingestCmd, lintCmd)
}

// ingest reads issues and the team from GitHub, reconciles the ledger at
// o.LedgerDir and reports to out. Without o.DryRun it saves the ledger and
// fails if the result does not lint clean.
func ingest(ctx context.Context, client *github.Client, o ingestOptions, now string, out io.Writer) error {
	if err := o.validate(); err != nil {
		return err
	}
	if !o.DryRun {
		// A mistyped path must not read as an empty ledger. A dry run is exempt:
		// the periodic runs before the directory is bootstrapped.
		if err := requireDir(o.LedgerDir); err != nil {
			return err
		}
	}
	team, err := issuefinder.FetchTeam(ctx, client, o.Owner, o.TeamSlug)
	if err != nil {
		return err
	}
	issues, err := issuefinder.FetchIssues(ctx, client, o.Owner, o.Repo)
	if err != nil {
		return err
	}
	items, err := ledger.Load(o.LedgerDir)
	if err != nil {
		return err
	}
	existed := make(map[string]bool, len(items))
	for k := range items {
		existed[k] = true
	}
	res := issuefinder.Ingest(issuefinder.Config{BotLogin: o.BotLogin, Team: team, Now: now}, issues, items)
	for _, s := range res.Skipped {
		fmt.Fprintf(out, "skipped %s\n", s)
	}
	for _, k := range res.Changed {
		// The line format "changed <key_hash> <state> <key> (new|updated)" is a
		// stable contract: scripts read field 4 as the key. new-or-updated is
		// a fifth field so it cannot shift the first four.
		kind := "new"
		if existed[k] {
			kind = "updated"
		}
		fmt.Fprintf(out, "changed %s %s %s (%s)\n", items[k].KeyHash, items[k].State, k, kind)
	}
	fmt.Fprintf(out, "%d issue(s) read, %d skipped, %d item(s) changed\n", len(issues), len(res.Skipped), len(res.Changed))
	if o.DryRun {
		fmt.Fprintln(out, "dry run: ledger not written")
		return nil
	}
	if err := ledger.Save(o.LedgerDir, items); err != nil {
		return err
	}
	if errs := ledger.Lint(o.LedgerDir); len(errs) > 0 {
		return fmt.Errorf("ledger written but does not lint clean: %w", errors.Join(errs...))
	}
	return nil
}
