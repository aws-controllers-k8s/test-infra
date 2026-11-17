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

package dod

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod/dodtest"
)

type fixture struct {
	repo       *dodtest.Repo
	base, head string
	regen      map[string]string // what fake regeneration writes
	codegen    string            // a git repo standing in for code-generator
}

const bumpTrailers = "\n\nProducer: autopilot/sdk-bump\nWork-Item: " + wi + "\nChange-Class: SDK-bump"

// sdkBump is a minimal controller with one SDK-bump commit by the bot.
func sdkBump(t *testing.T) fixture {
	t.Helper()
	r := dodtest.NewRepo(t)
	r.Write("go.mod", "module example.com/widgets-controller\n\ngo 1.22\n")
	r.Write("generator.yaml", "ignore:\n  field_paths: []\n")
	r.Write(metadataPath, metaYAML("v0.64.0", "2026-11-01"))
	r.Write("pkg/resource/widget/sdk.go", genSDK)
	r.Write("config/crd/bases/widgets.yaml", crdFixture)
	base := r.Commit("base")
	head := strings.Replace(metaYAML("v0.64.0", "2026-11-02"), "v1.41.5", "v1.48.0", 1)
	r.Write(metadataPath, head)
	r.Write("pkg/resource/widget/sdk.go", genSDK+"\nvar NewField = 1\n")
	headSHA := r.CommitAs(dodtest.Bot, "Bump the SDK to v1.48.0"+bumpTrailers)
	cg := dodtest.NewRepo(t)
	cg.Commit("code-generator")
	return fixture{repo: r, base: base, head: headSHA, codegen: cg.Dir, regen: map[string]string{
		"pkg/resource/widget/sdk.go": genSDK + "\nvar NewField = 1\n",
		metadataPath:                 strings.Replace(head, "2026-11-02", "2026-11-16", 1),
	}}
}

// exitCode is an error with an exit code, like *exec.ExitError.
type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
func (e exitCode) ExitCode() int { return int(e) }

// fakeTools regenerates like fakeMake, passes every go command, and answers
// ack-generate with doc (exit 1 when breaking, as crd-compat-check does).
func fakeTools(t *testing.T, f fixture, doc []byte, ackGenerateCode int) Runner {
	mk := fakeMake(f.regen, false)
	return func(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, []byte, error) {
		switch {
		case name == "make":
			return mk(ctx, dir, env, name, args...)
		case name == "go":
			return nil, nil, nil
		case strings.HasSuffix(name, "ack-generate"):
			assert.NotEqual(t, f.repo.Dir, dir, "crd-compat-check runs in the scratch clone")
			assert.Equal(t, []string{"crd-compat-check", "--output", "json", "--base-ref", f.base}, args)
			if ackGenerateCode != 0 {
				return doc, nil, exitCode(ackGenerateCode)
			}
			return doc, nil, nil
		}
		return nil, nil, fmt.Errorf("unexpected command %s", name)
	}
}

func compatJSON(t *testing.T, base string, head ...string) []byte {
	t.Helper()
	d := doc(crdChange{File: "config/crd/bases/widgets.yaml", Kind: "Widget", Bucket: "added_optional_spec",
		Field: "spec.name", Path: ".properties.spec.properties.name", ChangeType: "PROPERTY_ADDED"})
	d.BaseRef = base
	if len(head) > 0 {
		d.HeadRef = head[0]
	}
	d.Files, d.SensitiveCandidates = []crdFile{}, []crdCandidate{}
	b, err := json.Marshal(d)
	require.NoError(t, err)
	return b
}

func options(t *testing.T, f fixture) Options {
	model, err := filepath.Abs("testdata/widgets.json")
	require.NoError(t, err)
	return Options{
		RepoDir: f.repo.Dir, Repo: "example/widgets-controller", Service: "widgets",
		BaseRef: f.base, HeadRef: f.head, ChangeClass: ClassSDKBump, WorkItem: wi,
		BotEmails: []string{dodtest.Bot}, CodegenDir: f.codegen,
		CRDCompatJSON: compatJSON(t, f.base, f.head), SDKModelPath: model, ToolRevision: "test",
		Run: fakeTools(t, f, nil, 0),
	}
}

func TestRunSDKBumpPasses(t *testing.T) {
	f := sdkBump(t)
	v, err := Run(context.Background(), options(t, f))
	require.NoError(t, err)
	for _, c := range v.Checks {
		if c.Binding {
			assert.Equal(t, StatusPass, c.Status, "%s %v %+v", c.ID, c.Reasons, c.Findings)
		}
	}
	assert.Equal(t, StatusPass, v.Verdict)
	assert.Equal(t, f.base, v.Subject.BaseSHA)
	assert.Equal(t, f.repo.Git("rev-parse", f.head+"^{tree}"), v.Subject.HeadTree)
	assert.Equal(t, "file", v.Inputs.CRDCompatSource)
	assert.Equal(t, "file widgets.json", v.Inputs.SDKModel)
	assert.Equal(t, f.repo.Git("-C", f.codegen, "rev-parse", "HEAD"), v.Inputs.CodegenSHA)
	assert.Zero(t, v.HumanCommits)
}

func TestRunIsReproducible(t *testing.T) {
	f := sdkBump(t)
	a, err := Run(context.Background(), options(t, f))
	require.NoError(t, err)
	b, err := Run(context.Background(), options(t, f))
	require.NoError(t, err)
	ab, err := Marshal(a)
	require.NoError(t, err)
	bb, err := Marshal(b)
	require.NoError(t, err)
	assert.Equal(t, string(ab), string(bb))
	assert.NotContains(t, string(ab), f.repo.Dir)
	assert.NotContains(t, string(ab), os.TempDir())
}

func TestRunHandWrittenFileReclassifies(t *testing.T) {
	f := sdkBump(t)
	f.repo.Write("pkg/resource/widget/hook.go", "package widget\n")
	f.head = f.repo.CommitAs(dodtest.Bot, "hook"+bumpTrailers)
	v, err := Run(context.Background(), options(t, f))
	require.NoError(t, err)
	assert.Equal(t, StatusFail, v.Verdict)
	assert.Contains(t, v.Reasons, "path-allowlist:PATH_NOT_ALLOWED")
	assert.Equal(t, ClassHandWritten, v.ReclassifyTo)
}

func TestRunSkipNeverPasses(t *testing.T) {
	f := sdkBump(t)
	o := options(t, f)
	o.Skip = []string{CheckRegenClean}
	v, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.Equal(t, StatusFail, v.Verdict)
	assert.Equal(t, []string{"regen-clean:SKIPPED_BY_FLAG"}, v.Reasons)
	assert.Equal(t, []string{CheckRegenClean}, v.Inputs.SkippedByFlag)
}

func TestRunWithACKGenerate(t *testing.T) {
	f := sdkBump(t)
	o := options(t, f)
	o.CRDCompatJSON, o.ACKGenerate = nil, "/opt/codegen/bin/ack-generate"
	o.Run = fakeTools(t, f, compatJSON(t, f.base), 1)
	v, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.Equal(t, "ack-generate", v.Inputs.CRDCompatSource)
	assert.Equal(t, StatusPass, v.Verdict, "exit 1 with a document is a breaking result, not a failure to run")

	o.Run = fakeTools(t, f, nil, 2)
	v, err = Run(context.Background(), o)
	require.NoError(t, err)
	assert.Contains(t, v.Reasons, "crd-diff:CRD_COMPAT_RUN_FAILED")
}

func TestRunRejectsBadOptions(t *testing.T) {
	f := sdkBump(t)
	for name, mut := range map[string]func(*Options){
		"class":     func(o *Options) { o.ChangeClass = "breaking-API" },
		"work item": func(o *Options) { o.WorkItem = "abc" },
		"service":   func(o *Options) { o.Service = "../x" },
		"both docs": func(o *Options) { o.ACKGenerate = "/x/ack-generate" },
		"skip":      func(o *Options) { o.Skip = []string{"nope"} },
		"base":      func(o *Options) { o.BaseRef = "no-such-ref" },
	} {
		o := options(t, f)
		mut(&o)
		_, err := Run(context.Background(), o)
		assert.Error(t, err, name)
	}
}

func TestSDKModelFromCache(t *testing.T) {
	cache := t.TempDir()
	write := func(rel string) {
		p := filepath.Join(cache, rel, "codegen", "sdk-codegen", "aws-models", "widgets.json")
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(`{"shapes":{}}`), 0o644))
	}
	write("models/v1.48.0")
	write("models/service/widgets/v1.2.3")
	o := Options{Service: "widgets", CacheDir: cache}

	b, label, err := sdkModel(o, sdkPins{Core: "v1.48.0"}, generatorConfig{})
	require.NoError(t, err)
	assert.NotNil(t, b)
	assert.Equal(t, "v1.48.0 widgets.json", label)

	b, label, err = sdkModel(o, sdkPins{Core: "v1.48.0", Service: "1.2.3"}, generatorConfig{})
	require.NoError(t, err)
	assert.NotNil(t, b)
	assert.Equal(t, "service/widgets/v1.2.3 widgets.json", label)

	b, _, err = sdkModel(o, sdkPins{Core: "v9.9.9"}, generatorConfig{})
	require.NoError(t, err)
	assert.Nil(t, b, "a missing model is not an error; the checks skip")
}

// A run that hits its deadline must give no verdict (exit 2, retry), not a
// verdict whose checks failed because their tools were killed.
func TestRunTimeoutGivesNoVerdict(t *testing.T) {
	f := sdkBump(t)
	o := options(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inner := o.Run
	o.Run = func(c context.Context, dir string, env []string, name string, args ...string) ([]byte, []byte, error) {
		if name == "go" {
			cancel() // the deadline passes while go build runs
			return nil, nil, c.Err()
		}
		return inner(c, dir, env, name, args...)
	}
	v, err := Run(ctx, o)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, v)
}

// A bot-authored head binds provenance even when no --work-item is given (the
// Prow script passes one only if the head carries the trailer), so a bot head
// without a Work-Item fails instead of passing unchecked.
func TestRunBotHeadWithoutWorkItemFails(t *testing.T) {
	f := sdkBump(t)
	f.repo.Write("pkg/resource/widget/sdk.go", genSDK+"\nvar NewField = 2\n")
	f.head = f.repo.CommitAs(dodtest.Bot, "regen\n\nProducer: autopilot/sdk-bump\nChange-Class: SDK-bump")
	f.regen["pkg/resource/widget/sdk.go"] = genSDK + "\nvar NewField = 2\n"
	o := options(t, f)
	o.WorkItem = ""
	v, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.Equal(t, StatusFail, v.Verdict)
	assert.Equal(t, []string{"provenance:BOT_COMMIT_WITHOUT_WORK_ITEM"}, v.Reasons)

	// A human head with no work item still leaves provenance informational.
	f.repo.Write("README.md", "docs\n")
	f.head = f.repo.Commit("docs")
	o = options(t, f)
	o.WorkItem = ""
	v, err = Run(context.Background(), o)
	require.NoError(t, err)
	for _, c := range v.Checks {
		if c.ID == CheckProvenance {
			assert.False(t, c.Binding)
		}
	}
}

// A CRD document given as a file must name this head (head_ref, compared by
// tree); a file for another head, or with none, cannot be trusted.
func TestRunCRDCompatFileMustMatchHead(t *testing.T) {
	f := sdkBump(t)
	for name, tc := range map[string]struct {
		doc  []byte
		want string
	}{
		"missing": {compatJSON(t, f.base), "CRD_COMPAT_HEAD_MISSING"},
		"other":   {compatJSON(t, f.base, f.base), "CRD_COMPAT_HEAD_MISMATCH"},
		"unknown": {compatJSON(t, f.base, "0000000000000000000000000000000000000000"), "CRD_COMPAT_HEAD_MISMATCH"},
	} {
		o := options(t, f)
		o.CRDCompatJSON = tc.doc
		v, err := Run(context.Background(), o)
		require.NoError(t, err, name)
		assert.Equal(t, StatusFail, v.Verdict, name)
		assert.Contains(t, v.Reasons, "crd-diff:"+tc.want, name)
		assert.Contains(t, v.Reasons, "sensitive-field:"+tc.want, name)
	}
}

// Merge commits are commits too: a secret in a merge message must not slip
// past leak-inventory because git log --no-merges hid it.
func TestRunLeaksScanMergeCommitMessages(t *testing.T) {
	f := sdkBump(t)
	r := f.repo
	r.Git("checkout", "--quiet", "-b", "side", f.base)
	r.Write("README.md", "side\n")
	r.Commit("side")
	r.Git("checkout", "--quiet", "main")
	r.Git("merge", "--quiet", "--no-ff", "-m", "Merge side\n\nkey AKIA"+"Z7QWERTYUIOPASDF", "side")
	f.head = r.Git("rev-parse", "HEAD")
	o := options(t, f)
	o.CRDCompatJSON = compatJSON(t, f.base, f.head)
	v, err := Run(context.Background(), o)
	require.NoError(t, err)
	var leaks Check
	for _, c := range v.Checks {
		if c.ID == CheckLeaks {
			leaks = c
		}
	}
	assert.Equal(t, StatusFail, leaks.Status, "%+v", leaks.Findings)
	short := f.head[:12]
	found := false
	for _, fd := range leaks.Findings {
		found = found || fd.File == "commit:"+short
	}
	assert.True(t, found, "the merge commit's message is scanned: %+v", leaks.Findings)
}

func TestRevisionFrom(t *testing.T) {
	stamped := []debug.BuildSetting{{Key: "vcs.revision", Value: "abc"}, {Key: "vcs.modified", Value: "true"}}
	assert.Equal(t, "abc-dirty", revisionFrom(stamped, ""))

	// No VCS stamp (a go build from a worktree, or go test): fall back to the
	// tools checkout's HEAD.
	r := dodtest.NewRepo(t)
	r.Write("x", "x\n")
	head := r.Commit("x")
	assert.Equal(t, head, revisionFrom(nil, r.Dir))
	r.Write("x", "y\n")
	assert.Equal(t, head+"-dirty", revisionFrom(nil, r.Dir))

	assert.Equal(t, "unknown", revisionFrom(nil, t.TempDir()))
	assert.Equal(t, "unknown", revisionFrom(nil, ""))
}

// The tool revision is recorded, never judged: an unknown revision decides
// nothing.
func TestRunToolRevisionDoesNotDecide(t *testing.T) {
	f := sdkBump(t)
	o := options(t, f)
	o.ToolRevision = ""
	a, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.Equal(t, "unknown", a.Tool.Revision)
	o.ToolRevision = "abc"
	b, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.Equal(t, StatusPass, a.Verdict)
	assert.Equal(t, b.Verdict, a.Verdict)
	assert.Equal(t, b.Reasons, a.Reasons)
	assert.Equal(t, b.Checks, a.Checks)
}
