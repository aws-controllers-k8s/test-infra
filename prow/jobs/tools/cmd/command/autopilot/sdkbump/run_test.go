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
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod/dodtest"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

type bumpFixture struct {
	*repos
	t        *testing.T
	ledger   string
	forge    *FakeForge
	runs     []string
	builds   int
	svcPin   string // the aws_service_sdk_version fake regeneration writes
	failMake bool
	verdict  dod.Verdict
	gateErr  error
	gate     []dod.Options
}

func newBumpFixture(t *testing.T) *bumpFixture {
	f := &bumpFixture{repos: newRepos(t), t: t, forge: NewFakeForge("ack-bot"), svcPin: "v1.97.3"}
	f.saveLedger(bumpItem(ledger.Queued), dependent("Bucket", "logging.prefix", ServerDefaultWork, ledger.Blocked))
	f.verdict = dod.Verdict{Schema: dod.Schema, Verdict: dod.StatusPass, Reasons: []string{}, Labels: []string{},
		NotCovered: []string{"real-aws-e2e"}, Checks: []dod.Check{}}
	return f
}

// saveLedger replaces the ledger with exactly ws.
func (f *bumpFixture) saveLedger(ws ...*ledger.WorkItem) {
	f.ledger = f.t.TempDir()
	require.NoError(f.t, ledger.Save(f.ledger, itemsOf(ws...)))
}

func (f *bumpFixture) runner(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, []byte, error) {
	f.runs = append(f.runs, name+" "+strings.Join(args, " "))
	switch {
	case name == "git":
		return dod.ExecRunner(ctx, dir, env, name, args...)
	case name == "go" && len(args) == 2 && args[0] == "get":
		_, v, _ := strings.Cut(args[1], "@")
		f.ctrl.Write("go.mod", goMod(v))
		return nil, nil, nil
	case name == "go" && len(args) == 2 && args[0] == "mod" && args[1] == "tidy":
		return nil, nil, nil
	case name == "make":
		if f.failMake {
			return nil, []byte("compile error"), errors.New("exit status 2")
		}
		assert.Contains(f.t, env, "AWS_SERVICE_SDK_VERSION=v1.97.3")
		assert.Contains(f.t, env, "SERVICE_CONTROLLER_SOURCE_PATH="+f.ctrl.Dir)
		f.builds++
		f.ctrl.Write(metaPath, genMeta(f.cgSHA, f.svcPin, fmt.Sprintf("2026-11-16T00:00:%02dZ", f.builds)))
		f.ctrl.Write("pkg/resource/bucket/sdk.go", dodtest.GoHeader+"package bucket\n\nvar LoggingPrefix string\n")
		return nil, nil, nil
	case strings.HasSuffix(name, "ack-generate"):
		return []byte(crdDocJSON), nil, nil
	}
	return nil, nil, fmt.Errorf("unexpected command %s %v", name, args)
}

func (f *bumpFixture) options() Options {
	return Options{
		Inputs: Inputs{Service: svc, SDKTo: to, WorkItem: wi, ChangeClass: "SDK-bump"},
		Mode:   ModeBump, Org: "aws-controllers-k8s",
		ControllerDir: f.ctrl.Dir, CodegenRepo: f.codegen.Dir, RuntimeRepo: f.runtime.Dir,
		ACKGenerate: "/opt/codegen/bin/ack-generate", LedgerDir: f.ledger,
		BotName: "ack-bot", BotEmail: botEmail, BotLogin: "ack-bot",
		Remote: f.remote, ArtifactsDir: f.t.TempDir(), JobName: "autopilot-sdk-bump-s3 #1",
		Forge: f.forge, Run: f.runner,
		Gate: func(_ context.Context, o dod.Options) (*dod.Verdict, error) {
			f.gate = append(f.gate, o)
			v := f.verdict
			return &v, f.gateErr
		},
		Log: io.Discard,
	}
}

// fresh puts the controller checkout back on main, as a new job clone is.
func (f *bumpFixture) fresh() { f.ctrl.Git("checkout", "--quiet", "--force", "main") }

func (f *bumpFixture) remoteHead() (string, bool) {
	h, ok, err := gitOps{dir: f.ctrl.Dir, run: dod.ExecRunner}.remoteBranch(context.Background(), f.remote, svc)
	require.NoError(f.t, err)
	return h, ok
}

func (f *bumpFixture) status(sha, context string) string {
	return f.forge.Statuses[testRepo+"@"+sha+"@"+context].State
}

func TestBumpOpensABlockedDraftPR(t *testing.T) {
	f := newBumpFixture(t)
	o := f.options()
	o.BotLogin = "" // defaults to DefaultBotLogin
	rep := Run(context.Background(), o)
	require.Equal(t, Result{Outcome: OutcomePROpened, PR: "https://github.com/" + testRepo + "/pull/1"}, rep.Result)
	assert.True(t, rep.Pushed)
	head, ok := f.remoteHead()
	require.True(t, ok)
	assert.Equal(t, rep.Head, head)
	assert.Equal(t, botEmail+" "+botEmail, f.ctrl.Git("show", "-s", "--format=%ae %ce", head))
	v, ok := trailerValue(f.ctrl.Git("show", "-s", "--format=%B", head), "Work-Item")
	require.True(t, ok)
	assert.Equal(t, wi, v)
	assert.Equal(t, 2, f.builds, "build, tidy, build")

	pr := f.forge.PRs[testRepo][0]
	assert.True(t, pr.Draft)
	assert.Equal(t, [3]string{"autopilot/s3/sdk-bump", "main", "Update aws-sdk-go-v2/service/s3 to v1.97.3"},
		[3]string{pr.HeadRef, pr.BaseRef, pr.Title})
	assert.Contains(t, pr.Body, "- [ ] `Bucket` `logging.prefix`")
	digest, ok := dod.TrailerValue([]byte(pr.Body))
	require.True(t, ok)
	assert.Equal(t, dod.Digest(rep.VerdictBytes), digest)
	assert.ElementsMatch(t, []string{LabelAutoGen, LabelClass, LabelLevel, LabelBlockedServerDefault}, pr.Labels)
	assert.Equal(t, "success", f.status(head, ContextGate))
	assert.Equal(t, "failure", f.status(head, ContextServerDefault))
	assert.NotContains(t, strings.Join(f.forge.Calls, "\n"), "SetDraft")

	require.Len(t, f.gate, 1)
	g := f.gate[0]
	assert.Equal(t, dod.ClassSDKBump, g.ChangeClass)
	assert.Equal(t, wi, g.WorkItem)
	assert.Equal(t, []string{botEmail}, g.BotEmails)
	assert.Equal(t, head, g.HeadRef)
	assert.Equal(t, "ack-bot", g.PRAuthor, "the bump PR is always ack-bot's")
	assert.Equal(t, []string{"ack-bot"}, g.BotLogins)
	var want map[string]any
	require.NoError(t, json.Unmarshal([]byte(crdDocJSON), &want))
	want["head_ref"] = head // dod accepts a file document only for this run's head
	wantJSON, err := json.Marshal(want)
	require.NoError(t, err)
	assert.JSONEq(t, string(wantJSON), string(g.CRDCompatJSON))
	artifact, err := os.ReadFile(filepath.Join(o.ArtifactsDir, "crd-compat.json"))
	require.NoError(t, err)
	assert.Equal(t, string(g.CRDCompatJSON), string(artifact), "the artifact is the document dod read")
	assert.Empty(t, g.ACKGenerate, "exactly one CRD source")
	assert.NotContains(t, string(g.PRBody), sectionBegin(sectionGate), "dod scans the card, not its own summary")
	for _, name := range []string{"verdict.json", "gate-verdict.txt", "pr-body.md", "crd-compat.json"} {
		assert.FileExists(t, filepath.Join(o.ArtifactsDir, name))
	}
}

func TestBumpIsIdempotent(t *testing.T) {
	f := newBumpFixture(t)
	first := Run(context.Background(), f.options())
	require.Equal(t, OutcomePROpened, first.Outcome)
	f.fresh()
	calls := len(f.forge.Calls)
	second := Run(context.Background(), f.options())
	assert.Equal(t, first.Result, second.Result)
	assert.False(t, second.Pushed, "only build_date differs: the remote head is kept")
	assert.Equal(t, first.Head, second.Head)
	assert.Equal(t, first.Body, second.Body)
	assert.Len(t, f.forge.PRs[testRepo], 1)
	for _, c := range f.forge.Calls[calls:] {
		assert.NotRegexp(t, `^(CreatePR|EditPR)`, c)
	}
}

func TestBumpReplacesABranchOnAnOldBase(t *testing.T) {
	f := newBumpFixture(t)
	first := Run(context.Background(), f.options())
	f.fresh()
	f.ctrl.Write("README.md", "news\n")
	f.ctrl.Commit("main moved")
	second := Run(context.Background(), f.options())
	require.Equal(t, OutcomePROpened, second.Outcome)
	assert.True(t, second.Pushed)
	head, _ := f.remoteHead()
	assert.Equal(t, second.Head, head)
	assert.NotEqual(t, first.Head, head)
	assert.Len(t, f.forge.PRs[testRepo], 1, "the same PR, force-updated")
}

func TestBumpNeverForcePushesOverHumans(t *testing.T) {
	f := newBumpFixture(t)
	first := Run(context.Background(), f.options())
	f.ctrl.Git("checkout", "--quiet", "--force", first.Head)
	f.ctrl.Write("generator.yaml", "resources:\n  Bucket:\n    fields:\n      Logging.Prefix:\n        late_initialize: {}\n")
	human := f.ctrl.Commit("Decide late_initialize for logging.prefix")
	f.ctrl.Git("push", "--quiet", f.remote, human+":refs/heads/autopilot/s3/sdk-bump")
	f.fresh()
	f.forge.PRs[testRepo][0].HeadSHA = human
	builds := f.builds

	rep := Run(context.Background(), f.options())
	assert.Equal(t, Result{Outcome: OutcomeNeedsHuman, Reason: "HUMAN_COMMITS_ON_BRANCH", PR: first.PR}, rep.Result)
	assert.Equal(t, builds, f.builds, "no regeneration")
	head, _ := f.remoteHead()
	assert.Equal(t, human, head)
	pr := f.forge.PRs[testRepo][0]
	assert.Equal(t, "success", f.status(human, ContextServerDefault), "decided in generator.yaml")
	assert.True(t, pr.Draft, "dod never saw the human's head")
	assert.Contains(t, pr.Body, "- [x] `Bucket` `logging.prefix`")
	assert.NotContains(t, pr.Labels, LabelBlockedServerDefault)
}

func TestBumpReadyWhenNothingBlocks(t *testing.T) {
	f := newBumpFixture(t)
	f.saveLedger(bumpItem(ledger.Queued), dependent("Bucket", "logging.prefix", ServerDefaultWork, ledger.WontFix))
	rep := Run(context.Background(), f.options())
	require.Equal(t, OutcomePROpened, rep.Outcome)
	pr := f.forge.PRs[testRepo][0]
	assert.False(t, pr.Draft)
	assert.Equal(t, "success", f.status(rep.Head, ContextServerDefault))
	assert.NotContains(t, pr.Labels, LabelBlockedServerDefault)
}

func TestBumpOutcomeFollowsTheVerdict(t *testing.T) {
	for name, c := range map[string]struct {
		v       dod.Verdict
		outcome string
		reason  string
	}{
		"reclassify": {dod.Verdict{Verdict: dod.StatusFail, ReclassifyTo: "hand-written-code", Reasons: []string{"path-allowlist:PATH_NOT_ALLOWED"}},
			OutcomeNeedsHuman, "RECLASSIFY_HAND_WRITTEN_CODE"},
		"breaking": {dod.Verdict{Verdict: dod.StatusFail, ReclassifyTo: "breaking-API", Reasons: []string{"crd-diff:CRD_NOT_ADDITIVE"}},
			OutcomeNeedsHuman, "RECLASSIFY_BREAKING_API"},
		"env": {dod.Verdict{Verdict: dod.StatusFail, Reasons: []string{"regen-clean:REGEN_FAILED", "crd-diff:CRD_COMPAT_RUN_FAILED"}},
			OutcomeFailed, "GATE_ENV"},
		"other": {dod.Verdict{Verdict: dod.StatusFail, Reasons: []string{"regen-clean:REGEN_FAILED", "build:BUILD_FAILED"}},
			OutcomeNeedsHuman, "GATE_FAILED"},
	} {
		f := newBumpFixture(t)
		f.verdict = c.v
		rep := Run(context.Background(), f.options())
		assert.Equal(t, Result{Outcome: c.outcome, Reason: c.reason, PR: "https://github.com/" + testRepo + "/pull/1"}, rep.Result, name)
		assert.True(t, f.forge.PRs[testRepo][0].Draft, name)
		assert.Equal(t, "failure", f.status(rep.Head, ContextGate), name)
	}
}

func TestBumpStops(t *testing.T) {
	for name, c := range map[string]struct {
		setup  func(f *bumpFixture)
		result Result
	}{
		"at target": {func(f *bumpFixture) {
			f.ctrl.Write(metaPath, genMeta(f.cgSHA, "v1.97.3", "x"))
			f.ctrl.Commit("already bumped")
		}, Result{OutcomeNoChange, "ALREADY_AT_TARGET", ""}},
		"newer branch": {func(f *bumpFixture) {
			g := gitOps{dir: f.ctrl.Dir, run: dod.ExecRunner}
			base, _ := g.revParse(context.Background(), "HEAD")
			f.ctrl.Git("checkout", "--quiet", "--detach") // commitBump commits on HEAD; keep main at base
			f.ctrl.Write(metaPath, genMeta(f.cgSHA, "v1.98.0", "x"))
			c, err := g.commitBump(context.Background(), base, "newer", "ack-bot", botEmail)
			require.NoError(f.t, err)
			require.NoError(f.t, g.push(context.Background(), f.remote, svc, c, ""))
			f.fresh()
		}, Result{OutcomeNoChange, "NEWER_BUMP_OPEN", ""}},
		"closed by a human": {func(f *bumpFixture) {
			f.forge.Seed(testRepo, PR{State: "closed", HeadRef: Branch(svc), BaseRef: "main", Author: "ack-bot", Body: "x\n\nWork-Item: " + wi + "\n"})
		}, Result{OutcomeNeedsHuman, "PR_CLOSED_BY_HUMAN", "https://github.com/" + testRepo + "/pull/1"}},
		"codegen too old": {func(f *bumpFixture) {
			f.codegen.Write("cmd/ack-generate/command/root.go", "package command\n")
			f.cgSHA = f.codegen.Commit("old code-generator")
			f.ctrl.Write(metaPath, genMeta(f.cgSHA, "", "x"))
			f.ctrl.Commit("generated with an old code-generator")
		}, Result{OutcomeNeedsHuman, "CODEGEN_TOO_OLD", ""}},
		"build fails":    {func(f *bumpFixture) { f.failMake = true }, Result{OutcomeFailed, "BUILD_CONTROLLER_FAILED", ""}},
		"gate errors":    {func(f *bumpFixture) { f.gateErr = errors.New("no verdict") }, Result{OutcomeFailed, "GATE_ERROR", ""}},
		"dirty checkout": {func(f *bumpFixture) { f.ctrl.Write("stray.txt", "x") }, Result{OutcomeFailed, "DIRTY_CHECKOUT", ""}},
	} {
		f := newBumpFixture(t)
		c.setup(f)
		rep := Run(context.Background(), f.options())
		assert.Equal(t, c.result, rep.Result, name)
		if c.result.Reason != "NEWER_BUMP_OPEN" {
			_, pushed := f.remoteHead()
			assert.False(t, pushed, name+": nothing pushed")
		}
		for _, call := range f.forge.Calls {
			assert.NotRegexp(t, `^(CreatePR|EditPR|SetStatus)`, call, name)
		}
	}
}

func TestBumpPushesOnlyTheBumpBranch(t *testing.T) {
	f := newBumpFixture(t)
	Run(context.Background(), f.options())
	f.fresh()
	f.ctrl.Write("README.md", "news\n")
	f.ctrl.Commit("main moved")
	Run(context.Background(), f.options())
	pushRE := regexp.MustCompile(`^git push --quiet --no-verify --porcelain --force-with-lease=refs/heads/autopilot/s3/sdk-bump:(?:[0-9a-f]{40})? file://\S+ [0-9a-f]{40}:refs/heads/autopilot/s3/sdk-bump$`)
	pushes := 0
	for _, r := range f.runs {
		if strings.HasPrefix(r, "git push") {
			pushes++
			assert.Regexp(t, pushRE, r)
		}
	}
	assert.Equal(t, 2, pushes)
}

func TestRunRefusesUnsafeOptions(t *testing.T) {
	f := newBumpFixture(t)
	for name, mutate := range map[string]func(*Options){
		"write to a file remote":    func(o *Options) { o.Write = true },
		"write without a work item": func(o *Options) { o.Write, o.Remote, o.WorkItem, o.ChangeClass = true, "", "", "" },
		"GitHub forge, no --write":  func(o *Options) { o.Forge = writingForge{NewFakeForge("x")} },
		"another remote":            func(o *Options) { o.Remote = "https://github.com/someone/s3-controller.git" },
		"bad mode":                  func(o *Options) { o.Mode = "merge" },
		"bad service":               func(o *Options) { o.Service = "S3" },
		"no gate":                   func(o *Options) { o.Gate = nil },
	} {
		o := f.options()
		mutate(&o)
		assert.Equal(t, Result{Outcome: OutcomeNeedsHuman, Reason: "INVALID_INPUT"}, Run(context.Background(), o).Result, name)
	}
	assert.Empty(t, f.forge.Calls)
	assert.Empty(t, f.runs)
}

// writingForge is a FakeForge that reports it writes to GitHub.
type writingForge struct{ *FakeForge }

func (writingForge) Writes() bool { return true }

// The codegen and runtime checkouts are fresh worktrees in unrelated temporary
// directories: build-controller-release.sh looks for ack-generate and the
// templates at a GOPATH-relative path, and for the runtime next to the
// code-generator, unless told where they are (as dod's regen-clean is).
func TestRegeneratePointsMakeAtTheCodegenWorktree(t *testing.T) {
	type call struct {
		dir string
		env []string
	}
	var makes []call
	run := func(_ context.Context, dir string, env []string, name string, _ ...string) ([]byte, []byte, error) {
		if name == "make" {
			makes = append(makes, call{dir, env})
		}
		return nil, nil, nil
	}
	o := Options{Inputs: Inputs{Service: svc}, ControllerDir: "/work/controller", CacheDir: "/work/cache", Run: run, Log: io.Discard}
	cg, rt := "/work/cg", "/work/rt"
	require.Equal(t, "", regenerate(context.Background(), o, Target{Package: "s3", Version: "v1.97.3"}, cg, rt))
	require.Len(t, makes, 2)
	for _, m := range makes {
		assert.Equal(t, cg, m.dir)
		assert.Contains(t, m.env, "ACK_GENERATE_BIN_PATH="+filepath.Join(cg, "bin", "ack-generate"))
		assert.Contains(t, m.env, "TEMPLATES_DIR="+filepath.Join(cg, "templates"))
		assert.Contains(t, m.env, "RUNTIME_CRD_DIR="+filepath.Join(rt, "config"))
		assert.Contains(t, m.env, "RUNTIME_DIR="+rt)
		assert.Contains(t, m.env, "ACK_GENERATE_CACHE_DIR=/work/cache")
	}
}
