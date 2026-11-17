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
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
)

// Options are one run's inputs. RepoDir is read, never written.
type Options struct {
	RepoDir, Repo, Service string
	BaseRef, HeadRef       string
	ChangeClass, WorkItem  string
	BotEmails              []string
	// PRAuthor is the PR's GitHub author; when set, it alone decides whether
	// this is an autopilot PR (BotLogins), since commit emails are spoofable.
	PRAuthor  string
	BotLogins []string
	// CodegenDir and RuntimeDir are code-generator and runtime checkouts at
	// the SHAs the run is pinned to; regen-clean skips without CodegenDir.
	CodegenDir, RuntimeDir string
	// ACKGenerate is a built ack-generate with Plan 4 Part A, run in the
	// scratch clone; CRDCompatJSON is the same document from a file. At most
	// one is set.
	ACKGenerate   string
	CRDCompatJSON []byte
	// SDKModelPath overrides the model ack-generate cached under CacheDir.
	SDKModelPath, CacheDir string
	PRBody                 []byte
	Skip                   []string
	ToolRevision           string
	Run                    Runner
	Log                    io.Writer
}

var (
	workItemRE = regexp.MustCompile(`^sha256:[0-9a-f]{16}$`)
	serviceRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

func (o *Options) normalize() error {
	switch {
	case o.RepoDir == "":
		return errors.New("--repo-dir must be set")
	case !serviceRE.MatchString(o.Service):
		return fmt.Errorf("--service %q is not a service alias", o.Service)
	case strings.TrimSpace(o.BaseRef) == "":
		return errors.New("--base-ref must be set")
	case !validClass(o.ChangeClass):
		return fmt.Errorf("--change-class %q is not one of %s", o.ChangeClass, strings.Join(InputClasses, ", "))
	case o.WorkItem != "" && !workItemRE.MatchString(o.WorkItem):
		return fmt.Errorf("--work-item %q is not sha256:<16 hex>", o.WorkItem)
	case o.PRAuthor != "" && len(o.BotLogins) == 0:
		return errors.New("--pr-author needs --bot-login")
	case o.ACKGenerate != "" && o.CRDCompatJSON != nil:
		return errors.New("give --ack-generate or --crd-compat-json, not both")
	}
	for _, s := range o.Skip {
		if !slices.Contains(CheckOrder, s) {
			return fmt.Errorf("--skip-check %q is not a check (%s)", s, strings.Join(CheckOrder, ", "))
		}
	}
	o.HeadRef = cmp.Or(o.HeadRef, "HEAD")
	if o.Run == nil {
		o.Run = ExecRunner
	}
	if o.Log == nil {
		o.Log = io.Discard
	}
	for _, p := range []*string{&o.RepoDir, &o.CodegenDir, &o.RuntimeDir, &o.ACKGenerate, &o.SDKModelPath, &o.CacheDir} {
		if *p == "" {
			continue
		}
		abs, err := filepath.Abs(*p)
		if err != nil {
			return err
		}
		*p = abs
	}
	return nil
}

// sdkModel returns the head pin's model and a label with no machine path. A
// model that is not in the cache is (nil, label, nil): the checks then skip,
// and a skipped binding check fails the verdict.
func sdkModel(o Options, pins sdkPins, gen generatorConfig) ([]byte, string, error) {
	if o.SDKModelPath != "" {
		b, err := os.ReadFile(o.SDKModelPath)
		return b, "file " + filepath.Base(o.SDKModelPath), err
	}
	model := modelName(o.Service, gen)
	pkg := filepath.Base(cmp.Or(gen.SDKNames.PackageName, o.Service))
	var rel, label string
	switch {
	case pins.Service != "":
		v := "v" + strings.TrimLeft(pins.Service, "v")
		rel, label = filepath.Join("models", "service", pkg, v), "service/"+pkg+"/"+v
	case pins.Core != "":
		rel, label = filepath.Join("models", filepath.Base(pins.Core)), pins.Core
	default:
		return nil, "", nil
	}
	label += " " + model + ".json"
	if o.CacheDir == "" {
		return nil, label, nil
	}
	b, err := os.ReadFile(filepath.Join(o.CacheDir, rel, "codegen", "sdk-codegen", "aws-models", model+".json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, label, nil
	}
	return b, label, err
}

// runCRDCompat runs Plan 4's command in the scratch clone. Exit 1 with a
// document is a breaking result; anything else non-zero is a failure (nil).
func runCRDCompat(ctx context.Context, o Options, dir, base string) []byte {
	stdout, stderr, err := o.Run(ctx, dir, nil, o.ACKGenerate, "crd-compat-check", "--output", "json", "--base-ref", base)
	if err == nil {
		return stdout
	}
	var ec interface{ ExitCode() int }
	if errors.As(err, &ec) && ec.ExitCode() == 1 && len(bytes.TrimSpace(stdout)) > 0 {
		return stdout
	}
	fmt.Fprintf(o.Log, "crd-compat-check: %v\n", err)
	logTail(o.Log, "ack-generate crd-compat-check", stderr)
	return nil
}

// errCRDHead is a CRD document file that is not for this run's head.
type errCRDHead struct{ sig, detail string }

func (e errCRDHead) Error() string { return e.detail }

// crdFileHead checks a file document's head_ref: it must resolve to a commit
// whose tree is the head tree (the verdict binds the tree, decision 2).
func crdFileHead(ctx context.Context, src gitRepo, d *crdCompat, headTree string) error {
	if strings.TrimSpace(d.HeadRef) == "" {
		return errCRDHead{"CRD_COMPAT_HEAD_MISSING", "the crd-compat document has no head_ref"}
	}
	if t, err := src.treeSHA(ctx, d.HeadRef); err != nil || t != headTree {
		return errCRDHead{"CRD_COMPAT_HEAD_MISMATCH", "document head_ref " + d.HeadRef}
	}
	return nil
}

// crdDocProblem is the check to report when the CRD document is unusable.
func crdDocProblem(id string, d *crdCompat, err error, runFailed bool) (Check, bool) {
	var head errCRDHead
	switch {
	case errors.As(err, &head):
		return newCheck(id, []Finding{{Signature: head.sig, Detail: head.detail, Fails: true}}), true
	case runFailed:
		return newCheck(id, []Finding{{Signature: "CRD_COMPAT_RUN_FAILED", Fails: true}}), true
	case err != nil:
		return newCheck(id, []Finding{{Signature: "CRD_COMPAT_INVALID", Detail: err.Error(), Fails: true}}), true
	case d == nil:
		return skipCheck(id, "CRD_COMPAT_MISSING"), true
	}
	return Check{}, false
}

// sensitiveCRDs reads, from git, the head CRDs the document's added buckets
// name. It runs before regen-clean and build, which run other code.
func sensitiveCRDs(ctx context.Context, src gitRepo, head string, d *crdCompat) (map[string][]map[string]any, []Finding) {
	crds := map[string][]map[string]any{}
	var extra []Finding
	if d == nil {
		return crds, nil
	}
	for _, b := range addedBuckets {
		for _, ch := range d.Buckets[b] {
			if _, done := crds[ch.File]; done {
				continue
			}
			data, err := src.showIfExists(ctx, head, ch.File)
			if err == nil && data == nil {
				err = errors.New("not in the head tree")
			}
			var schemas []map[string]any
			if err == nil {
				schemas, err = crdVersionSchemas(data)
			}
			if err != nil {
				extra = append(extra, Finding{Signature: "CRD_UNREADABLE", File: ch.File, Fails: true})
			}
			crds[ch.File] = schemas
		}
	}
	return crds, extra
}

func sensitiveCheck(d *crdCompat, docErr error, runFailed bool, crds map[string][]map[string]any, extra []Finding, model []byte) Check {
	if c, bad := crdDocProblem(CheckSensitive, d, docErr, runFailed); bad {
		return c
	}
	if model == nil {
		return skipCheck(CheckSensitive, "SDK_MODEL_MISSING")
	}
	names, err := smithySensitiveNames(model)
	if err != nil {
		return newCheck(CheckSensitive, []Finding{{Signature: "SDK_MODEL_INVALID", Fails: true}})
	}
	return checkSensitive(d, crds, names, extra)
}

func sdkPinCheck(class string, base sdkPins, baseErr error, head sdkPins, headErr error) Check {
	if baseErr != nil || headErr != nil {
		return newCheck(CheckSDKPin, []Finding{{Signature: "SDK_PIN_UNREADABLE", Fails: true}})
	}
	return checkSDKPin(class, base, head)
}

// runOrder is the order checks run in; build is last (it runs PR code).
var runOrder = []string{
	CheckPathAllowlist, CheckProvenance, CheckCRDDiff, CheckCodeScan, CheckLeaks, CheckSDKPin,
	CheckRegenClean, CheckSensitive, CheckIgnore, CheckBuild,
}

// Run gathers one PR's inputs from git, runs every check in CheckOrder and
// decides. An error means no verdict.
func Run(ctx context.Context, o Options) (*Verdict, error) {
	if err := o.normalize(); err != nil {
		return nil, err
	}
	src := gitRepo{dir: o.RepoDir}
	head, err := src.commitSHA(ctx, o.HeadRef)
	if err != nil {
		return nil, fmt.Errorf("--head-ref: %w", err)
	}
	base, err := src.commitSHA(ctx, o.BaseRef)
	if err != nil {
		return nil, fmt.Errorf("--base-ref: %w", err)
	}
	mb, err := src.mergeBase(ctx, base, head)
	if err != nil {
		return nil, err
	}
	tree, err := src.treeSHA(ctx, head)
	if err != nil {
		return nil, err
	}
	files, err := gather(ctx, src, mb, head)
	if err != nil {
		return nil, err
	}
	commits, err := src.commits(ctx, mb, head)
	if err != nil {
		return nil, err
	}
	headCommit, err := src.commitInfo(ctx, head)
	if err != nil {
		return nil, err
	}
	provenanceBinds := o.WorkItem != "" || autopilotPR(o.PRAuthor, o.BotLogins, headCommit, o.BotEmails)

	in := Inputs{ChangeClass: o.ChangeClass, WorkItem: o.WorkItem, SkippedByFlag: slices.Clone(o.Skip)}
	if o.CodegenDir != "" {
		if in.CodegenSHA, err = (gitRepo{dir: o.CodegenDir}).commitSHA(ctx, "HEAD"); err != nil {
			return nil, fmt.Errorf("--codegen-dir: %w", err)
		}
	}
	if o.RuntimeDir != "" {
		if in.RuntimeSHA, err = (gitRepo{dir: o.RuntimeDir}).commitSHA(ctx, "HEAD"); err != nil {
			return nil, fmt.Errorf("--runtime-dir: %w", err)
		}
	}

	skip := func(id string) bool { return slices.Contains(o.Skip, id) }
	var scratch gitRepo
	if o.ACKGenerate != "" || !skip(CheckBuild) || (o.CodegenDir != "" && !skip(CheckRegenClean)) {
		s, cleanup, err := cloneScratch(ctx, src, head)
		if err != nil {
			return nil, err
		}
		defer cleanup()
		scratch = s
	}

	// The CRD document comes before regen-clean touches the scratch tree.
	raw, runFailed := o.CRDCompatJSON, false
	switch {
	case o.ACKGenerate != "":
		in.CRDCompatSource = "ack-generate"
		raw = runCRDCompat(ctx, o, scratch.dir, mb)
		runFailed = raw == nil
	case raw != nil:
		in.CRDCompatSource = "file"
	}
	var compat *crdCompat
	var compatErr error
	if raw != nil {
		in.CRDCompatSHA256 = Digest(raw)
		compat, compatErr = parseCRDCompat(raw)
		if compatErr == nil && in.CRDCompatSource == "file" {
			compatErr = crdFileHead(ctx, src, compat, tree)
		}
	}

	read := func(rev, p string) []byte {
		if err == nil {
			var b []byte
			b, err = src.showIfExists(ctx, rev, p)
			return b
		}
		return nil
	}
	baseMeta, headMeta := read(mb, metadataPath), read(head, metadataPath)
	baseMod, headMod := read(mb, "go.mod"), read(head, "go.mod")
	baseGen, headGen := read(mb, "generator.yaml"), read(head, "generator.yaml")
	if err != nil {
		return nil, err
	}
	basePins, basePinErr := readPins(baseMeta, baseMod)
	headPins, headPinErr := readPins(headMeta, headMod)
	headCfg, _ := readGeneratorConfig(headGen) // ignore-effective reports a bad file

	var docBase string
	if compat != nil {
		docBase, _ = src.commitSHA(ctx, compat.BaseRef)
	}
	crds, crdExtra := sensitiveCRDs(ctx, src, head, compat)

	// The model is read before regen-clean when it is already there
	// (--sdk-model, a warm cache); otherwise after it, since regeneration
	// fills ack-generate's cache. Either way before build.
	var model []byte
	loadModel := func() error {
		if model != nil {
			return nil
		}
		var err error
		model, in.SDKModel, err = sdkModel(o, headPins, headCfg)
		if model != nil {
			in.SDKModelSHA256 = Digest(model)
		}
		return err
	}
	if err := loadModel(); err != nil {
		return nil, err
	}

	// Checks run in runOrder: everything that only reads inputs first, then
	// regen-clean (trusted tools), then the model-based checks, and build last,
	// because go test runs PR code. The verdict lists them in CheckOrder.
	results := map[string]Check{}
	humans := 0
	for _, id := range runOrder {
		if skip(id) {
			results[id] = skipCheck(id, "SKIPPED_BY_FLAG")
			continue
		}
		var c Check
		switch id {
		case CheckPathAllowlist:
			c = checkPathAllowlist(o.ChangeClass, files)
		case CheckProvenance:
			c, humans = checkProvenance(commits, o.BotEmails, o.WorkItem, o.ChangeClass)
		case CheckRegenClean:
			c = checkRegen(ctx, scratch, head, regenOptions{
				Service: o.Service, CodegenDir: o.CodegenDir, RuntimeDir: o.RuntimeDir, CacheDir: o.CacheDir, Run: o.Run, Log: o.Log,
			})
		case CheckBuild:
			// regen-clean resets the clone; never build anything but head.
			if err := scratchAtHead(ctx, scratch, head); err != nil {
				return nil, fmt.Errorf("build: %w", err)
			}
			c = checkBuild(ctx, o.ChangeClass, scratch, o.Run, o.Log)
		case CheckCRDDiff:
			if p, bad := crdDocProblem(id, compat, compatErr, runFailed); bad {
				c = p
			} else {
				c = checkCRDDiff(o.ChangeClass, compat, docBase, mb)
			}
		case CheckSensitive:
			if err := loadModel(); err != nil {
				return nil, err
			}
			c = sensitiveCheck(compat, compatErr, runFailed, crds, crdExtra, model)
		case CheckCodeScan:
			c = checkCodeScan(o.ChangeClass, files)
		case CheckLeaks:
			c = checkLeaks(files, commits, o.PRBody)
		case CheckSDKPin:
			c = sdkPinCheck(o.ChangeClass, basePins, basePinErr, headPins, headPinErr)
		case CheckIgnore:
			if err := loadModel(); err != nil {
				return nil, err
			}
			c = ignoreCheck(baseGen, headGen, model, modelName(o.Service, headCfg))
		}
		results[id] = c
	}
	checks := make([]Check, 0, len(CheckOrder))
	for _, id := range CheckOrder {
		checks = append(checks, results[id])
	}
	// Tools killed by the deadline look like failing checks; a run that ran
	// out of time has no verdict.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("dod did not finish: %w", err)
	}

	v := decide(o.ChangeClass, provenanceBinds, checks)
	v.Tool = Tool{Name: ToolName, Revision: cmp.Or(o.ToolRevision, "unknown")}
	v.Subject = Subject{Repo: o.Repo, Service: o.Service, BaseSHA: mb, HeadTree: tree}
	v.Inputs = in
	v.HumanCommits = humans
	return &v, nil
}

// WriteText is the human-readable form of a verdict.
func WriteText(w io.Writer, v *Verdict, digest string) error {
	var b strings.Builder
	for _, c := range v.Checks {
		role := "info"
		if c.Binding {
			role = "binding"
		}
		fmt.Fprintf(&b, "check %-16s %-4s %-7s %s\n", c.ID, c.Status, role, strings.Join(c.Reasons, ","))
		for _, f := range c.Findings {
			mark := " "
			if f.Fails {
				mark = "!"
			}
			fmt.Fprintf(&b, "  %s %s %s:%d %s\n", mark, f.Signature, f.File, f.Line, f.Detail)
		}
	}
	fmt.Fprintf(&b, "verdict %s reclassify_to=%q labels=%s human_commits=%d not_covered=%s\n",
		v.Verdict, v.ReclassifyTo, strings.Join(v.Labels, ","), v.HumanCommits, strings.Join(v.NotCovered, ","))
	fmt.Fprintf(&b, "%s: %s\n", TrailerKey, digest)
	_, err := io.WriteString(w, b.String())
	return err
}

// BuildRevision is the binary's VCS revision, recorded in every verdict (and
// never used to decide it). It is the build's vcs.revision stamp; a binary
// built without one (go build from a git worktree, go test) falls back to the
// HEAD of the tools checkout it was compiled from, if that is still on disk;
// otherwise "unknown".
func BuildRevision() string {
	var settings []debug.BuildSetting
	if info, ok := debug.ReadBuildInfo(); ok {
		settings = info.Settings
	}
	src := ""
	if _, file, _, ok := runtime.Caller(0); ok && filepath.IsAbs(file) {
		src = filepath.Dir(file)
	}
	return revisionFrom(settings, src)
}

func revisionFrom(settings []debug.BuildSetting, srcDir string) string {
	rev, dirty := "", false
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" && srcDir != "" {
		g := gitRepo{dir: srcDir}
		ctx := context.Background()
		if sha, err := g.commitSHA(ctx, "HEAD"); err == nil {
			rev = sha
			out, err := g.run(ctx, "status", "--porcelain", "--untracked-files=no")
			dirty = err != nil || len(bytes.TrimSpace(out)) > 0
		}
	}
	if rev == "" {
		return "unknown"
	}
	if dirty {
		rev += "-dirty"
	}
	return rev
}
