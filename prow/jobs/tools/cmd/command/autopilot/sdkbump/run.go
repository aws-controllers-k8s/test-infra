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

package sdkbump

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod"
)

// Run is one sdk-bump run. Every problem is an outcome in the Report (design
// 5.4: an expected outcome is a verdict token, never a job error).
func Run(ctx context.Context, o Options) Report {
	t, problems := o.Inputs.Validate()
	if err := o.check(); err != nil {
		problems = append(problems, err.Error())
	}
	if len(problems) > 0 {
		o.logf("invalid input: %s", strings.Join(problems, "; "))
		return stop(OutcomeNeedsHuman, "INVALID_INPUT", "")
	}
	if o.Mode == ModeRefresh {
		return refresh(ctx, o)
	}
	return bump(ctx, o, t)
}

func bump(ctx context.Context, o Options, t Target) Report {
	g := gitOps{dir: o.ControllerDir, run: o.Run}
	base, err := g.revParse(ctx, "HEAD")
	if err != nil {
		o.logf("%v", err)
		return stop(OutcomeFailed, "CHECKOUT_UNREADABLE", "")
	}
	if dirty, err := g.dirty(ctx); err != nil || dirty {
		o.logf("the controller checkout is not clean (%v)", err)
		return stop(OutcomeFailed, "DIRTY_CHECKOUT", "")
	}
	before, err := readPins(ctx, g, base, t.Package)
	if err != nil {
		o.logf("pins: %v", err)
		return stop(OutcomeNeedsHuman, "SDK_PIN_UNREADABLE", "")
	}
	if outcome, why := decide(before, t); outcome != "" {
		o.logf("%s %s", outcome, why)
		return stop(outcome, why, "")
	}

	prs, err := o.Forge.PRsForBranch(ctx, o.repo(), Branch(o.Service))
	if err != nil {
		o.logf("%v", err)
		return stop(OutcomeFailed, "GITHUB_READ_FAILED", "")
	}
	open, halt := pickPR(prs, o)
	if halt != nil {
		return *halt
	}
	url := ""
	if open != nil {
		url = open.URL
	}

	remote, exists, err := g.remoteBranch(ctx, o.remote(), o.Service)
	if err != nil {
		o.logf("%v", err)
		return stop(OutcomeFailed, "FETCH_FAILED", url)
	}
	if exists {
		foreign, err := g.foreignCommits(ctx, base, remote, o.BotEmail)
		if err != nil {
			o.logf("%v", err)
			return stop(OutcomeFailed, "FETCH_FAILED", url)
		}
		if len(foreign) > 0 {
			o.logf("%d commit(s) on %s are not the bot's (first %s): not force-pushing", len(foreign), Branch(o.Service), short(foreign[0]))
			if open != nil {
				if _, err := refreshGate(ctx, o, g, *open, remote); err != nil {
					o.logf("refreshing the merge block: %v", err)
				}
			}
			return stop(OutcomeNeedsHuman, "HUMAN_COMMITS_ON_BRANCH", url)
		}
		if rp, err := readPins(ctx, g, remote, t.Package); err == nil && rp.ServiceSDK != "" && semver.Compare(rp.ServiceSDK, t.Version) > 0 {
			o.logf("%s is already at %s", Branch(o.Service), rp.ServiceSDK)
			return stop(OutcomeNoChange, "NEWER_BUMP_OPEN", url)
		}
	}

	cg, doneCG, err := worktree(ctx, o.Run, o.CodegenRepo, before.CodegenHash)
	if err != nil {
		o.logf("%v", err)
		return stop(OutcomeNeedsHuman, "CODEGEN_PIN_UNUSABLE", url)
	}
	defer doneCG()
	if !codegenSupportsServicePin(cg) {
		return stop(OutcomeNeedsHuman, "CODEGEN_TOO_OLD", url)
	}
	rev, err := runtimeRev(before.RuntimeVersion)
	if err != nil {
		o.logf("%v", err)
		return stop(OutcomeNeedsHuman, "RUNTIME_PIN_UNUSABLE", url)
	}
	rt, doneRT, err := worktree(ctx, o.Run, o.RuntimeRepo, rev)
	if err != nil {
		o.logf("%v", err)
		return stop(OutcomeNeedsHuman, "RUNTIME_PIN_UNUSABLE", url)
	}
	defer doneRT()

	if err := g.checkout(ctx, base); err != nil {
		return stop(OutcomeFailed, "CHECKOUT_UNREADABLE", url)
	}
	if why := regenerate(ctx, o, t, cg, rt); why != "" {
		return stop(OutcomeFailed, why, url)
	}
	if changed, err := g.dirty(ctx); err != nil || !changed {
		o.logf("regeneration changed nothing (%v)", err)
		return stop(OutcomeNoChange, "NO_DIFF", url)
	}
	head, err := g.commitBump(ctx, base, commitMessage(o.WorkItem, t, before), o.BotName, o.BotEmail)
	if err != nil {
		o.logf("%v", err)
		return stop(OutcomeFailed, "COMMIT_FAILED", url)
	}
	push := true
	if exists {
		if same, err := g.sameBump(ctx, base, remote, head, before.MetadataPath); err == nil && same {
			o.logf("%s already holds this bump (only build_date differs): keeping %s", Branch(o.Service), short(remote))
			head, push = remote, false
			if err := g.checkout(ctx, head); err != nil {
				return stop(OutcomeFailed, "CHECKOUT_UNREADABLE", url)
			}
		}
	}
	after, err := readPins(ctx, g, head, t.Package)
	if err != nil {
		o.logf("pins at the bump: %v", err)
		return stop(OutcomeFailed, "COMMIT_FAILED", url)
	}

	crdJSON, crd := crdCompat(ctx, o, base, head)
	gen, _ := g.showIfExists(ctx, head, "generator.yaml")
	deps := loadDependents(o, gen)
	card := RenderCard(CardInput{Service: o.Service, Target: t, Before: before, After: after, CRD: crd, Deps: deps,
		JobName: o.JobName, JobURL: o.JobURL})

	do := dod.Options{
		RepoDir: o.ControllerDir, Repo: o.repo(), Service: o.Service,
		BaseRef: base, HeadRef: head, ChangeClass: dod.ClassSDKBump, WorkItem: o.WorkItem,
		BotEmails: []string{o.BotEmail}, PRAuthor: o.BotLogin, BotLogins: []string{o.BotLogin},
		CodegenDir: cg, RuntimeDir: rt,
		CRDCompatJSON: crdJSON, CacheDir: o.CacheDir, PRBody: []byte(card),
		ToolRevision: dod.BuildRevision(), Run: o.Run, Log: o.Log,
	}
	if crdJSON == nil {
		do.ACKGenerate = o.ACKGenerate // dod runs it and records why it failed
	}
	v, err := o.Gate(ctx, do)
	if err != nil {
		o.logf("ack-dod: %v", err)
		return stop(OutcomeFailed, "GATE_ERROR", url)
	}
	vb, err := dod.Marshal(v)
	if err != nil {
		o.logf("ack-dod verdict: %v", err)
		return stop(OutcomeFailed, "GATE_ERROR", url)
	}
	digest := dod.Digest(vb)
	body := ComposeBody(card, RenderGateSection(v, digest), Trailers(o.WorkItem, digest))
	rep := Report{Title: Title(t), Body: body, Head: head, Verdict: v, VerdictBytes: vb}
	if err := writeArtifacts(o.ArtifactsDir, map[string][]byte{
		"verdict.json": vb, "gate-verdict.txt": []byte(dod.Trailer(vb) + "\n"),
		"pr-body.md": []byte(body), "crd-compat.json": crdJSON,
	}); err != nil {
		o.logf("artifacts: %v", err)
	}
	if len(body) > maxBodyBytes {
		return rep.with(OutcomeNeedsHuman, "BODY_TOO_LARGE", url)
	}

	if push {
		switch {
		case !o.pushes():
			rep.Planned = append(rep.Planned, fmt.Sprintf("would push %s to %s", head, BranchRef(o.Service)))
		default:
			if err := g.push(ctx, o.remote(), o.Service, head, remote); err != nil {
				o.logf("%v", err)
				return rep.with(OutcomeFailed, "PUSH_FAILED", url)
			}
			rep.Pushed = true
		}
	}
	pr, err := upsertPR(ctx, o, open, rep.Title, body)
	if err != nil {
		o.logf("%v", err)
		if open == nil {
			return rep.with(OutcomeFailed, "PR_CREATE_FAILED", url)
		}
		return rep.with(OutcomeFailed, "PR_UPDATE_FAILED", url)
	}
	if err := setGateStatus(ctx, o, head, v, digest); err != nil {
		o.logf("%v", err)
		return rep.with(OutcomeFailed, "PR_UPDATE_FAILED", pr.URL)
	}
	if err := applyMergeBlock(ctx, o, pr, head, v.Verdict == dod.StatusPass, deps, v.Labels); err != nil {
		o.logf("%v", err)
		return rep.with(OutcomeFailed, "PR_UPDATE_FAILED", pr.URL)
	}
	rep.Result = classify(v, pr.URL)
	return rep
}

// regenerate is design 3 row 4: go get, build, go mod tidy, build. It
// returns the failure reason, or "".
func regenerate(ctx context.Context, o Options, t Target, codegen, runtime string) string {
	goEnv := []string{"GOFLAGS=-mod=mod"}
	mk := []string{
		"SERVICE=" + o.Service, "SERVICE_CONTROLLER_SOURCE_PATH=" + o.ControllerDir,
		"RUNTIME_CRD_DIR=" + filepath.Join(runtime, "config"), "AWS_SERVICE_SDK_VERSION=" + t.Version,
	}
	if o.CacheDir != "" {
		mk = append(mk, "ACK_GENERATE_CACHE_DIR="+o.CacheDir)
	}
	steps := []struct {
		dir, why string
		env      []string
		cmd      []string
	}{
		{o.ControllerDir, "GO_GET_FAILED", goEnv, []string{"go", "get", sdkServicePrefix + t.Package + "@" + t.Version}},
		{codegen, "BUILD_CONTROLLER_FAILED", mk, []string{"make", "build-controller", "SERVICE=" + o.Service}},
		{o.ControllerDir, "GO_MOD_TIDY_FAILED", goEnv, []string{"go", "mod", "tidy"}},
		{codegen, "BUILD_CONTROLLER_FAILED", mk, []string{"make", "build-controller", "SERVICE=" + o.Service}},
	}
	for _, s := range steps {
		if _, stderr, err := o.Run(ctx, s.dir, s.env, s.cmd[0], s.cmd[1:]...); err != nil {
			o.logf("%s: %v", strings.Join(s.cmd, " "), err)
			logTail(o.Log, s.cmd[0], stderr)
			return s.why
		}
	}
	return ""
}

// crdCompat runs crd-compat-check in the controller checkout (at the bump
// head). Exit 1 with a document means "breaking", which is still a document.
// The returned bytes carry head_ref = head: dod accepts a document given as a
// file only when it names this run's head (CRD_COMPAT_HEAD_MISSING), and the
// card, dod and the crd-compat.json artifact all read these same bytes.
func crdCompat(ctx context.Context, o Options, base, head string) ([]byte, *CRDDoc) {
	if o.ACKGenerate == "" {
		return nil, nil
	}
	out, stderr, err := o.Run(ctx, o.ControllerDir, nil, o.ACKGenerate, "crd-compat-check", "--output", "json", "--base-ref", base)
	var ec interface{ ExitCode() int }
	if err != nil && !(errors.As(err, &ec) && ec.ExitCode() == 1) {
		o.logf("crd-compat-check: %v", err)
		logTail(o.Log, "crd-compat-check", stderr)
		return nil, nil
	}
	if _, err := ParseCRDDoc(out); err != nil {
		o.logf("crd-compat-check output: %v", err)
		return nil, nil
	}
	out, err = withHeadRef(out, head)
	if err != nil {
		o.logf("crd-compat-check output: %v", err)
		return nil, nil
	}
	doc, err := ParseCRDDoc(out)
	if err != nil {
		o.logf("crd-compat-check output: %v", err)
		return nil, nil
	}
	return out, doc
}

// withHeadRef sets the document's head_ref, keeping every other key as
// written (re-encoded with sorted keys).
func withHeadRef(doc []byte, head string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(doc, &m); err != nil {
		return nil, err
	}
	h, err := json.Marshal(head)
	if err != nil {
		return nil, err
	}
	m["head_ref"] = h
	return json.Marshal(m)
}

var envReasons = map[string]bool{"REGEN_FAILED": true, "REGEN_SETUP_FAILED": true, "CRD_COMPAT_RUN_FAILED": true}

// classify maps the verdict to the run's outcome (decision 10).
func classify(v *dod.Verdict, pr string) Result {
	switch {
	case v.Verdict == dod.StatusPass:
		return Result{Outcome: OutcomePROpened, PR: pr}
	case v.ReclassifyTo != "":
		return Result{Outcome: OutcomeNeedsHuman, Reason: "RECLASSIFY_" + strings.ToUpper(strings.ReplaceAll(v.ReclassifyTo, "-", "_")), PR: pr}
	case len(v.Reasons) > 0 && allEnv(v.Reasons):
		return Result{Outcome: OutcomeFailed, Reason: "GATE_ENV", PR: pr}
	}
	return Result{Outcome: OutcomeNeedsHuman, Reason: "GATE_FAILED", PR: pr}
}

// allEnv: every reason ("<check>:<signature>") is an environment failure.
func allEnv(reasons []string) bool {
	for _, r := range reasons {
		_, sig, _ := strings.Cut(r, ":")
		if !envReasons[sig] {
			return false
		}
	}
	return true
}

func writeArtifacts(dir string, files map[string][]byte) error {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, b := range files {
		if b == nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func logTail(w io.Writer, what string, out []byte) {
	if len(out) > stderrTail {
		out = out[len(out)-stderrTail:]
	}
	fmt.Fprintf(w, "--- %s (last %d bytes of output)\n%s\n", what, len(out), out)
}
