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
	"sort"
	"strings"
	"time"

	"github.com/google/go-github/v63/github"
	"github.com/spf13/cobra"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dispatcher"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/issuefinder"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/policy"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/viewstatus"
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
	// TeamOwner is the org that owns TeamSlug. Empty means Owner.
	TeamOwner string
	DryRun    bool
}

// teamOwner is the org to ask for the team's members: --team-owner, or
// --issues-owner when that is not set. A sandbox repo owned by a user needs the
// two to differ, because only an org has teams.
func (o ingestOptions) teamOwner() string {
	if strings.TrimSpace(o.TeamOwner) == "" {
		return o.Owner
	}
	return o.TeamOwner
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
		errs := append(ledger.Lint(lintDir), policy.Lint(lintDir)...)
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
	f.StringVar(&ingestOpts.Owner, "issues-owner", "aws-controllers-k8s", "owner of the issues repo (and of the team, unless --team-owner is set)")
	f.StringVar(&ingestOpts.Repo, "issues-repo", "ack-private", "repo where detect-api-changes files issues")
	f.StringVar(&ingestOpts.BotLogin, "bot-login", "", "account detect-api-changes files issues as; other authors are ignored")
	f.StringVar(&ingestOpts.TeamSlug, "team-slug", "", "team whose members' /autopilot commands count")
	f.StringVar(&ingestOpts.TeamOwner, "team-owner", "", "org that owns --team-slug (default: the value of --issues-owner)")
	f.StringVar(&ingestOpts.LedgerDir, "ledger-dir", "", "the autopilot/ directory of an ack-private checkout")
	f.BoolVar(&ingestOpts.DryRun, "dry-run", false, "report changes without writing the ledger")
	for _, name := range []string{"bot-login", "team-slug", "ledger-dir"} {
		_ = ingestCmd.MarkFlagRequired(name)
	}
	lintCmd.Flags().StringVar(&lintDir, "ledger-dir", "", "the autopilot/ directory of an ack-private checkout")
	_ = lintCmd.MarkFlagRequired("ledger-dir")
	Command.AddCommand(ingestCmd, lintCmd)

	d := dispatchCmd.Flags()
	d.StringVar(&dispatchOpts.Owner, "issues-owner", "aws-controllers-k8s", "owner of the issues repo (and of the team, unless --team-owner is set)")
	d.StringVar(&dispatchOpts.Repo, "issues-repo", "ack-private", "repo where detect-api-changes files issues")
	d.StringVar(&dispatchOpts.BotLogin, "bot-login", "", "account detect-api-changes files issues as; other authors are ignored")
	d.StringVar(&dispatchOpts.TeamSlug, "team-slug", "", "team whose members' /autopilot commands count")
	d.StringVar(&dispatchOpts.TeamOwner, "team-owner", "", "org that owns --team-slug (default: the value of --issues-owner)")
	d.StringVar(&dispatchOpts.LedgerDir, "ledger-dir", "", "the autopilot/ directory of an ack-private checkout")
	d.BoolVar(&dispatchOpts.DryRun, "dry-run", false, "report the tick without writing the ledger")
	for _, name := range []string{"bot-login", "team-slug", "ledger-dir"} {
		_ = dispatchCmd.MarkFlagRequired(name)
	}
	s := postStatusCmd.Flags()
	s.StringVar(&statusOpts.Owner, "issues-owner", "aws-controllers-k8s", "owner of the issues repo")
	s.StringVar(&statusOpts.Repo, "issues-repo", "ack-private", "repo with the api-change issues")
	s.StringVar(&statusOpts.LedgerDir, "ledger-dir", "", "the autopilot/ directory of an ack-private checkout")
	s.StringVar(&statusOpts.CommentLogin, "comment-login", "", "account the token comments as; only its marked comments are edited")
	s.BoolVar(&statusOpts.DryRun, "dry-run", false, "print the planned comment edits without making them")
	for _, name := range []string{"ledger-dir", "comment-login"} {
		_ = postStatusCmd.MarkFlagRequired(name)
	}
	Command.AddCommand(dispatchCmd, postStatusCmd)
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
	team, err := issuefinder.FetchTeam(ctx, client, o.teamOwner(), o.TeamSlug)
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

type statusOptions struct {
	Owner, Repo, LedgerDir, CommentLogin string
	DryRun                               bool
}

func (o statusOptions) validate() error {
	for _, f := range []struct{ name, value string }{
		{"issues-owner", o.Owner}, {"issues-repo", o.Repo},
		{"ledger-dir", o.LedgerDir}, {"comment-login", o.CommentLogin},
	} {
		if strings.TrimSpace(f.value) == "" {
			return fmt.Errorf("--%s must not be empty", f.name)
		}
	}
	return nil
}

var (
	dispatchOpts ingestOptions
	statusOpts   statusOptions
)

func githubClient() (*github.Client, error) {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return nil, errors.New("environment variable GITHUB_TOKEN is not provided")
	}
	return github.NewClient(&http.Client{Timeout: requestTimeout}).WithAuthToken(token), nil
}

var dispatchCmd = &cobra.Command{
	Use:   "dispatch",
	Short: "run one Dispatcher tick: ingest api-change issues, then reconcile, route and launch work items",
	Long: `Ingests detect-api-changes issues (read-only, as ingest-issues does), then
reconciles finished runs, routes items through the state machine, applies the
caps in policy/budgets.yaml and launches with the launcher policy/autopilot.yaml
names (only "shadow", which starts nothing). Saves and lints the ledger unless
--dry-run. The GitHub token is read from GITHUB_TOKEN.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := githubClient()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), ingestTimeout)
		defer cancel()
		return dispatch(ctx, client, dispatchOpts, time.Now().UTC(), cmd.OutOrStdout())
	},
}

var postStatusCmd = &cobra.Command{
	Use:   "post-status",
	Short: "create or update the Dispatcher's status comment on each open api-change issue",
	Long: `Reads the ledger and the open api-change issues and keeps one comment per issue
(by --comment-login, starting with the status marker) listing that issue's work
items. Never edits the issue body or anyone else's comment. --dry-run prints the
planned edits. The GitHub token is read from GITHUB_TOKEN.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := githubClient()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), ingestTimeout)
		defer cancel()
		return postStatus(ctx, client, statusOpts, cmd.OutOrStdout())
	},
}

// dispatch is one Dispatcher tick: read GitHub, ingest, Tick, then (unless
// o.DryRun) save and lint the ledger and its policy.
func dispatch(ctx context.Context, client *github.Client, o ingestOptions, now time.Time, out io.Writer) error {
	if err := o.validate(); err != nil {
		return err
	}
	if !o.DryRun {
		if err := requireDir(o.LedgerDir); err != nil {
			return err
		}
	}
	team, err := issuefinder.FetchTeam(ctx, client, o.teamOwner(), o.TeamSlug)
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
	cfg := dispatcher.Config{Now: now}
	if pol, perr := policy.Load(o.LedgerDir); perr != nil {
		cfg.PolicyErr = perr
	} else if l, lerr := dispatcher.NewLauncher(pol); lerr != nil {
		cfg.PolicyErr = lerr
	} else {
		cfg.Policy, cfg.Launcher = &pol, l
	}

	before := snapshot(items)
	ts := now.UTC().Format(time.RFC3339)
	ing := issuefinder.Ingest(issuefinder.Config{BotLogin: o.BotLogin, Team: team, Now: ts}, issues, items)
	tick := dispatcher.Tick(ctx, cfg, items)

	for _, s := range ing.Skipped {
		fmt.Fprintf(out, "skipped %s\n", s)
	}
	changed := changedKeys(before, items)
	for _, k := range changed {
		kind := "new"
		if _, ok := before[k]; ok {
			kind = "updated"
		}
		fmt.Fprintf(out, "changed %s %s %s (%s)\n", items[k].KeyHash, items[k].State, k, kind)
	}
	for _, l := range tick.Launched {
		mode := "started"
		if l.Shadow {
			mode = "shadow"
		}
		fmt.Fprintf(out, "launch %s %s %s %s (%s)\n", l.KeyHash, l.Workflow, l.LaunchID, l.Key, mode)
	}
	for _, w := range tick.Waiting {
		fmt.Fprintf(out, "waiting %s %s %s\n", w.KeyHash, w.Cap, w.Key)
	}
	reasons := make([]string, 0, len(tick.Held))
	held := 0
	for r, n := range tick.Held {
		reasons = append(reasons, r)
		held += n
	}
	sort.Strings(reasons)
	for _, r := range reasons {
		fmt.Fprintf(out, "held %s %d\n", r, tick.Held[r])
	}
	for _, p := range tick.Problems {
		fmt.Fprintf(out, "problem %s\n", p)
	}
	fmt.Fprintf(out, "%d issue(s) read, %d skipped, %d item(s) changed, %d launch(es), %d waiting, %d held, %d problem(s)\n",
		len(issues), len(ing.Skipped), len(changed), len(tick.Launched), len(tick.Waiting), held, len(tick.Problems))
	if o.DryRun {
		fmt.Fprintln(out, "dry run: ledger not written")
		return nil
	}
	if err := ledger.Save(o.LedgerDir, items); err != nil {
		return err
	}
	if errs := append(ledger.Lint(o.LedgerDir), policy.Lint(o.LedgerDir)...); len(errs) > 0 {
		return fmt.Errorf("ledger written but does not lint clean: %w", errors.Join(errs...))
	}
	return nil
}

// postStatus keeps the status comment on every open issue that some item
// names as its view issue.
func postStatus(ctx context.Context, client *github.Client, o statusOptions, out io.Writer) error {
	if err := o.validate(); err != nil {
		return err
	}
	if !o.DryRun {
		if err := requireDir(o.LedgerDir); err != nil {
			return err
		}
	}
	items, err := ledger.Load(o.LedgerDir)
	if err != nil {
		return err
	}
	byIssue := map[string][]*ledger.WorkItem{}
	for _, w := range items {
		if w.Links.ViewIssue != "" {
			byIssue[w.Links.ViewIssue] = append(byIssue[w.Links.ViewIssue], w)
		}
	}
	issues, err := issuefinder.FetchIssues(ctx, client, o.Owner, o.Repo)
	if err != nil {
		return err
	}
	sort.Slice(issues, func(i, j int) bool { return issues[i].Number < issues[j].Number })
	for _, is := range issues {
		if !is.Open || len(byIssue[is.URL]) == 0 {
			continue
		}
		a := viewstatus.Plan(is, o.CommentLogin, viewstatus.Render(byIssue[is.URL]))
		fmt.Fprintf(out, "status #%d %s\n", is.Number, a.Kind)
		for _, id := range a.Duplicates {
			fmt.Fprintf(out, "status #%d duplicate %d\n", is.Number, id)
		}
		if o.DryRun {
			continue
		}
		if err := viewstatus.Apply(ctx, client, o.Owner, o.Repo, a); err != nil {
			return err
		}
	}
	if o.DryRun {
		fmt.Fprintln(out, "dry run: no comments written")
	}
	return nil
}

func snapshot(items map[string]*ledger.WorkItem) map[string]string {
	out := make(map[string]string, len(items))
	for k, w := range items {
		b, _ := ledger.Marshal(w)
		out[k] = string(b)
	}
	return out
}

func changedKeys(before map[string]string, items map[string]*ledger.WorkItem) []string {
	var out []string
	for k, v := range snapshot(items) {
		if before[k] != v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
