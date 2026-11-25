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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod"
)

type preflightFlags struct {
	repoDir, baseRef, headRef, changeClass, prBody, output, reportOut string
}

var preflightOpts preflightFlags

var preflightCmd = &cobra.Command{
	Use:   "preflight",
	Short: "run ack-dod's git-only checks on an agent commit before it is pushed",
	Long: `Runs path-allowlist, code-scan, leak-inventory and sdk-pin on --base-ref (its
merge base with --head-ref) .. --head-ref of the controller checkout at
--repo-dir, which is never written. prow-job.sh runs it on the agent's commit
before pushing it to the fork, which is public.

The result is advisory, unsigned and never a verdict: the ack-dod job still
gates the PR. A leak or a sensitive path blocks; so does any other failing
binding check, unless it only asks for a higher change class (ack-dod then
escalates the PR for a person). Prints the report (--output text or json) and
writes the same text to --report-out. Exit 0 = may push, 1 = blocked, 2 = could
not decide (do not push).`,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runPreflight(cmd.Context(), preflightOpts, cmd.OutOrStdout())
	},
}

func init() {
	f := preflightCmd.Flags()
	f.StringVar(&preflightOpts.repoDir, "repo-dir", ".", "controller checkout (read only)")
	f.StringVar(&preflightOpts.baseRef, "base-ref", "", "what the PR will merge into; diffs from its merge base with --head-ref")
	f.StringVar(&preflightOpts.headRef, "head-ref", "HEAD", "the commit about to be pushed")
	f.StringVar(&preflightOpts.changeClass, "change-class", "", "the work item's change class (from the ledger)")
	f.StringVar(&preflightOpts.prBody, "pr-body-file", "", "PR description to scan for leaks")
	f.StringVar(&preflightOpts.output, "output", "text", "text or json")
	f.StringVar(&preflightOpts.reportOut, "report-out", "", "also write the text report here")
	for _, name := range []string{"base-ref", "change-class"} {
		_ = preflightCmd.MarkFlagRequired(name)
	}
	Command.AddCommand(preflightCmd)
}

func runPreflight(ctx context.Context, f preflightFlags, out io.Writer) error {
	undecided := func(err error) error { return exitError{code: 2, err: err} }
	if f.output != "text" && f.output != "json" {
		return undecided(fmt.Errorf(`--output must be "text" or "json", got %q`, f.output))
	}
	o := dod.PreflightOptions{RepoDir: f.repoDir, BaseRef: f.baseRef, HeadRef: f.headRef, ChangeClass: f.changeClass}
	if f.prBody != "" {
		b, err := os.ReadFile(f.prBody)
		if err != nil {
			return undecided(err)
		}
		o.PRBody = b
	}
	r, err := dod.Preflight(ctx, o)
	if err != nil {
		return undecided(err)
	}
	var text strings.Builder
	if err := dod.WritePreflightText(&text, r); err != nil {
		return undecided(err)
	}
	if f.reportOut != "" {
		if err := os.WriteFile(f.reportOut, []byte(text.String()), 0o644); err != nil {
			return undecided(err)
		}
	}
	if f.output == "json" {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		err = enc.Encode(r)
	} else {
		_, err = io.WriteString(out, text.String())
	}
	if err != nil {
		return undecided(err)
	}
	if r.Blocked {
		return exitError{code: 1, err: fmt.Errorf("preflight blocked: %s", strings.Join(r.Reasons, " "))}
	}
	return nil
}
