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
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Runner runs one command in dir with env added to the process environment.
// Tests replace it; ExecRunner is the real one.
type Runner func(ctx context.Context, dir string, env []string, name string, args ...string) (stdout, stderr []byte, err error)

func ExecRunner(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

const logTailBytes = 4 << 10

// logTail writes the end of a tool's output to the log, never the verdict.
func logTail(w io.Writer, what string, out []byte) {
	if len(out) > logTailBytes {
		out = out[len(out)-logTailBytes:]
	}
	fmt.Fprintf(w, "--- %s (last %d bytes of output)\n%s\n", what, len(out), out)
}

type regenOptions struct {
	Service, CodegenDir, RuntimeDir, CacheDir string
	Run                                       Runner
	Log                                       io.Writer
}

// regenIgnored is cd/scripts/verify-code-gen.sh filter_patterns_for_file,
// keyed by base name, as "^[+-].*<pattern>".
var regenIgnored = map[string]*regexp.Regexp{
	"ack-generate-metadata.yaml": regexp.MustCompile(`^[+-].*\s*(api_directory_checksum|build_date|build_hash|go_version|version):`),
	"kustomization.yaml":         regexp.MustCompile(`^[+-].*\s*newTag:\s*[0-9]+\.[0-9]+\.[0-9]+`),
	"Chart.yaml":                 regexp.MustCompile(`^[+-].*\s*(version|appVersion):\s*[0-9]+\.[0-9]+\.[0-9]+`),
	"values.yaml":                regexp.MustCompile(`^[+-].*\s*tag:\s*[0-9]+\.[0-9]+\.[0-9]+`),
	"NOTES.txt":                  regexp.MustCompile(`^[+-].*controller:[0-9]+\.[0-9]+\.[0-9]+`),
	"version.go":                 regexp.MustCompile(`^[+-].*\s*(ACKGenerateBuildDate|ACKGenerateVersion)\s*=`),
}

var semverIn = regexp.MustCompile(`v[0-9]+\.[0-9]+\.[0-9]+`)

func codegenVersion(meta []byte) string {
	var m struct {
		Info struct {
			Version string `yaml:"version"`
		} `yaml:"ack_generate_info"`
	}
	_ = yaml.Unmarshal(meta, &m)
	return m.Info.Version
}

// metadataVersion is verify-code-gen.sh validate_metadata_version.
func metadataVersion(committed, regenerated []byte, p string) []Finding {
	c, r := codegenVersion(committed), codegenVersion(regenerated)
	switch {
	case strings.Contains(c, "dirty"):
		return []Finding{{Signature: "REGEN_DIRTY_CODEGEN", File: p, Detail: "committed " + c, Fails: true}}
	case semverIn.FindString(c) == "" || semverIn.FindString(r) == "":
		return []Finding{{Signature: "REGEN_CODEGEN_VERSION_UNREADABLE", File: p, Fails: true}}
	case semverIn.FindString(c) != semverIn.FindString(r):
		return []Finding{{Signature: "REGEN_CODEGEN_VERSION_MISMATCH", File: p,
			Detail: "committed " + semverIn.FindString(c) + ", regenerated " + semverIn.FindString(r), Fails: true}}
	}
	return nil
}

func regenDiff(ctx context.Context, scratch gitRepo, head, p string) []Finding {
	out, err := scratch.run(ctx, "diff", "-U0", "--text", "--no-ext-diff", "--no-textconv", "--", p)
	if err != nil {
		return []Finding{{Signature: "REGEN_SETUP_FAILED", File: p, Fails: true}}
	}
	var findings []Finding
	filter := regenIgnored[path.Base(p)]
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "-") {
			continue
		}
		if strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "--- ") {
			continue
		}
		if filter != nil && filter.MatchString(line) {
			continue
		}
		findings = append(findings, Finding{Signature: "REGEN_DIFF", File: p, Fails: true})
		break
	}
	if path.Base(p) == "ack-generate-metadata.yaml" {
		committed, err1 := scratch.show(ctx, head, p)
		regenerated, err2 := os.ReadFile(filepath.Join(scratch.dir, filepath.FromSlash(p)))
		if err1 != nil || err2 != nil {
			return append(findings, Finding{Signature: "REGEN_CODEGEN_VERSION_UNREADABLE", File: p, Fails: true})
		}
		findings = append(findings, metadataVersion(committed, regenerated, p)...)
	}
	return findings
}

// checkRegen deletes every tracked regen output (regenOutputs) and every file
// with a regen header under apis/, cmd/ and pkg/, runs make build-controller in code-generator against the scratch
// clone, and fails on any difference verify-code-gen.sh would report, on a
// generated file that did not come back, and on new files. The scratch clone
// is reset to head afterwards.
func checkRegen(ctx context.Context, scratch gitRepo, head string, o regenOptions) Check {
	if o.CodegenDir == "" {
		return skipCheck(CheckRegenClean, "CODEGEN_DIR_NOT_SET")
	}
	defer func() {
		if err := scratch.reset(ctx, head); err != nil {
			fmt.Fprintf(o.Log, "regen-clean: reset scratch clone: %v\n", err)
		}
	}()
	fail := func(sig string) Check {
		return newCheck(CheckRegenClean, []Finding{{Signature: sig, Fails: true}})
	}
	ls, err := scratch.run(ctx, "ls-files", "-z")
	if err != nil {
		return fail("REGEN_SETUP_FAILED")
	}
	for _, p := range strings.Split(strings.TrimSuffix(string(ls), "\x00"), "\x00") {
		if p == "" {
			continue
		}
		full := filepath.Join(scratch.dir, filepath.FromSlash(p))
		// A file under a header-less output (an extra helm template, say)
		// must come back from regeneration too, or it was not generated.
		if matchAny(regenOutputs, p) {
			if err := os.Remove(full); err != nil {
				return fail("REGEN_SETUP_FAILED")
			}
			continue
		}
		if !matchAny(headerDirs, p) {
			continue
		}
		if b, err := os.ReadFile(full); err == nil && hasRegenHeader(b) {
			if err := os.Remove(full); err != nil {
				return fail("REGEN_SETUP_FAILED")
			}
		}
	}
	// make build-controller builds bin/ack-generate first; the release step
	// would otherwise look for it and the templates at a GOPATH-relative path.
	env := []string{"SERVICE=" + o.Service, "SERVICE_CONTROLLER_SOURCE_PATH=" + scratch.dir,
		"ACK_GENERATE_BIN_PATH=" + filepath.Join(o.CodegenDir, "bin", "ack-generate"),
		"TEMPLATES_DIR=" + filepath.Join(o.CodegenDir, "templates")}
	if o.RuntimeDir != "" {
		env = append(env, "RUNTIME_CRD_DIR="+filepath.Join(o.RuntimeDir, "config"))
	}
	if o.CacheDir != "" {
		env = append(env, "ACK_GENERATE_CACHE_DIR="+o.CacheDir)
	}
	if _, stderr, err := o.Run(ctx, o.CodegenDir, env, "make", "build-controller", "SERVICE="+o.Service); err != nil {
		logTail(o.Log, "make build-controller", stderr)
		return fail("REGEN_FAILED")
	}
	status, err := scratch.run(ctx, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return fail("REGEN_SETUP_FAILED")
	}
	var findings []Finding
	for _, e := range strings.Split(strings.TrimSuffix(string(status), "\x00"), "\x00") {
		if len(e) < 4 {
			continue
		}
		xy, p := e[:2], e[3:]
		switch {
		case xy == "??":
			findings = append(findings, Finding{Signature: "REGEN_NEW_FILE", File: p, Fails: true})
		case strings.Contains(xy, "D"):
			findings = append(findings, Finding{Signature: "GENERATED_FILE_NOT_REPRODUCED", File: p, Fails: true})
		default:
			findings = append(findings, regenDiff(ctx, scratch, head, p)...)
		}
	}
	return newCheck(CheckRegenClean, findings)
}
