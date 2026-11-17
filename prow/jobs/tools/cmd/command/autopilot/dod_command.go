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
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod"
)

// exitError carries a process exit code to command.Execute.
type exitError struct {
	code int
	err  error
}

func (e exitError) Error() string { return e.err.Error() }
func (e exitError) Unwrap() error { return e.err }
func (e exitError) ExitCode() int { return e.code }

type dodFlags struct {
	repoDir, repo, service, baseRef, headRef, changeClass, workItem string
	botEmails, skip                                                 []string
	codegenDir, runtimeDir, ackGenerate, crdCompat, sdkModel        string
	cacheDir, prBody, output, verdictOut, trailerOut                string
	timeout                                                         time.Duration
}

type verifyFlags struct {
	file, repoDir, commit, gateVerdict, prBody string
}

var (
	dodOpts    dodFlags
	verifyOpts verifyFlags
)

var dodCmd = &cobra.Command{
	Use:   "dod",
	Short: "run the ack-dod gates on a controller PR and write its verdict",
	Long: `Runs ACK Autopilot's Definition-of-Done checks (path-allowlist, provenance,
regen-clean, build, crd-diff, sensitive-field, code-scan, leak-inventory,
sdk-pin, ignore-effective) on --base-ref (its merge base with --head-ref) ..
--head-ref of the controller checkout at --repo-dir, which is never written.
--change-class picks the binding checks. Prints the ack.autopilot.verdict/v1
document (--output json) or a summary (text), and writes the exact bytes to
--verdict-out and "Gate-Verdict: sha256:<hex>" to --trailer-out.
Exit 0 = pass, 1 = fail, 2 = no verdict. Needs no credentials.`,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runDod(cmd.Context(), dodOpts, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

var verifyVerdictCmd = &cobra.Command{
	Use:   "verify-verdict",
	Short: "check a Gate-Verdict value against a verdict file and a commit",
	Long: `Exit 0 when the verdict file's SHA-256 is the Gate-Verdict value (--gate-verdict,
or the last Gate-Verdict trailer in --pr-body-file), --commit has the verdict's
head tree and the verdict's base is its ancestor; 1 when one of those does not
hold; 2 on any other error.`,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runVerify(cmd.Context(), verifyOpts, cmd.OutOrStdout())
	},
}

func defaultCacheDir() string {
	if d := os.Getenv("ACK_GENERATE_CACHE_DIR"); d != "" {
		return d
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".cache", "aws-controllers-k8s")
	}
	return ""
}

func init() {
	f := dodCmd.Flags()
	f.StringVar(&dodOpts.repoDir, "repo-dir", ".", "controller checkout (read only)")
	f.StringVar(&dodOpts.repo, "repo", "", "owner/name, recorded in the verdict")
	f.StringVar(&dodOpts.service, "service", "", "service alias, e.g. s3")
	f.StringVar(&dodOpts.baseRef, "base-ref", "", "the PR's base; dod diffs from its merge base with --head-ref")
	f.StringVar(&dodOpts.headRef, "head-ref", "HEAD", "the PR's head")
	f.StringVar(&dodOpts.changeClass, "change-class", "", "mechanical, docs-only, SDK-bump, config-only, hand-written-code or new-resource")
	f.StringVar(&dodOpts.workItem, "work-item", "", "ledger key hash (sha256:<16 hex>); makes provenance binding")
	f.StringArrayVar(&dodOpts.botEmails, "bot-email", nil, "author email of autopilot commits (repeatable)")
	f.StringVar(&dodOpts.codegenDir, "codegen-dir", "", "code-generator checkout at the pinned SHA (regen-clean skips without it)")
	f.StringVar(&dodOpts.runtimeDir, "runtime-dir", "", "runtime checkout at the pinned SHA (RUNTIME_CRD_DIR for regeneration)")
	f.StringVar(&dodOpts.ackGenerate, "ack-generate", "", "built ack-generate with crd-compat-check --output json; run in the scratch clone")
	f.StringVar(&dodOpts.crdCompat, "crd-compat-json", "", `ack.crd-compat/v1 document file, or "-" for stdin`)
	f.StringVar(&dodOpts.sdkModel, "sdk-model", "", "SDK model JSON (default: the head pin's model in --cache-dir)")
	f.StringVar(&dodOpts.cacheDir, "cache-dir", defaultCacheDir(), "ack-generate model cache (ACK_GENERATE_CACHE_DIR)")
	f.StringVar(&dodOpts.prBody, "pr-body-file", "", `PR description to scan for leaks, or "-" for stdin`)
	f.StringArrayVar(&dodOpts.skip, "skip-check", nil, "check to skip (repeatable); a skipped binding check fails the verdict")
	f.StringVar(&dodOpts.output, "output", "text", "text or json")
	f.StringVar(&dodOpts.verdictOut, "verdict-out", "", "write the verdict document here")
	f.StringVar(&dodOpts.trailerOut, "trailer-out", "", "write the Gate-Verdict trailer line here")
	f.DurationVar(&dodOpts.timeout, "timeout", 90*time.Minute, "give up (exit 2) after this long")
	for _, name := range []string{"service", "base-ref", "change-class"} {
		_ = dodCmd.MarkFlagRequired(name)
	}

	v := verifyVerdictCmd.Flags()
	v.StringVar(&verifyOpts.file, "file", "", "verdict document")
	v.StringVar(&verifyOpts.repoDir, "repo-dir", ".", "controller checkout")
	v.StringVar(&verifyOpts.commit, "commit", "HEAD", "commit the verdict must describe (a PR head)")
	v.StringVar(&verifyOpts.gateVerdict, "gate-verdict", "", "expected sha256:<hex>")
	v.StringVar(&verifyOpts.prBody, "pr-body-file", "", "read the Gate-Verdict trailer from this PR body")
	_ = verifyVerdictCmd.MarkFlagRequired("file")

	Command.AddCommand(dodCmd, verifyVerdictCmd)
}

func readInput(name string, stdin io.Reader) ([]byte, error) {
	if name == "-" {
		return io.ReadAll(io.LimitReader(stdin, 64<<20))
	}
	return os.ReadFile(name)
}

func runDod(ctx context.Context, f dodFlags, stdin io.Reader, out, errw io.Writer) error {
	noVerdict := func(err error) error { return exitError{code: 2, err: err} }
	if f.output != "text" && f.output != "json" {
		return noVerdict(fmt.Errorf(`--output must be "text" or "json", got %q`, f.output))
	}
	if f.crdCompat == "-" && f.prBody == "-" {
		return noVerdict(errors.New(`only one of --crd-compat-json and --pr-body-file can be "-"`))
	}
	o := dod.Options{
		RepoDir: f.repoDir, Repo: f.repo, Service: f.service, BaseRef: f.baseRef, HeadRef: f.headRef,
		ChangeClass: f.changeClass, WorkItem: f.workItem, BotEmails: f.botEmails,
		CodegenDir: f.codegenDir, RuntimeDir: f.runtimeDir, ACKGenerate: f.ackGenerate,
		SDKModelPath: f.sdkModel, CacheDir: f.cacheDir, Skip: f.skip,
		ToolRevision: dod.BuildRevision(), Log: errw,
	}
	var err error
	if f.crdCompat != "" {
		if o.CRDCompatJSON, err = readInput(f.crdCompat, stdin); err != nil {
			return noVerdict(err)
		}
	}
	if f.prBody != "" {
		if o.PRBody, err = readInput(f.prBody, stdin); err != nil {
			return noVerdict(err)
		}
	}
	if f.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, f.timeout)
		defer cancel()
	}
	v, err := dod.Run(ctx, o)
	if err != nil {
		return noVerdict(err)
	}
	b, err := dod.Marshal(v)
	if err != nil {
		return noVerdict(err)
	}
	if f.verdictOut != "" {
		if err := os.WriteFile(f.verdictOut, b, 0o644); err != nil {
			return noVerdict(err)
		}
	}
	if f.trailerOut != "" {
		if err := os.WriteFile(f.trailerOut, []byte(dod.Trailer(b)+"\n"), 0o644); err != nil {
			return noVerdict(err)
		}
	}
	if f.output == "json" {
		_, err = out.Write(b)
	} else {
		err = dod.WriteText(out, v, dod.Digest(b))
	}
	if err != nil {
		return noVerdict(err)
	}
	if v.Verdict != dod.StatusPass {
		return exitError{code: 1, err: fmt.Errorf("verdict fail: %s", strings.Join(v.Reasons, " "))}
	}
	return nil
}

func runVerify(ctx context.Context, f verifyFlags, out io.Writer) error {
	data, err := os.ReadFile(f.file)
	if err != nil {
		return exitError{code: 2, err: err}
	}
	want := f.gateVerdict
	if want == "" && f.prBody != "" {
		body, err := os.ReadFile(f.prBody)
		if err != nil {
			return exitError{code: 2, err: err}
		}
		var ok bool
		if want, ok = dod.TrailerValue(body); !ok {
			return exitError{code: 1, err: errors.New("the PR body has no Gate-Verdict trailer")}
		}
	}
	if want == "" {
		return exitError{code: 2, err: errors.New("give --gate-verdict or --pr-body-file")}
	}
	v, err := dod.Verify(ctx, f.repoDir, f.commit, data, want)
	switch {
	case errors.Is(err, dod.ErrDigestMismatch), errors.Is(err, dod.ErrTreeMismatch), errors.Is(err, dod.ErrBaseNotAncestor):
		return exitError{code: 1, err: err}
	case err != nil:
		return exitError{code: 2, err: err}
	}
	_, err = fmt.Fprintf(out, "verdict ok digest=%s verdict=%s reclassify_to=%q\n", want, v.Verdict, v.ReclassifyTo)
	return err
}
