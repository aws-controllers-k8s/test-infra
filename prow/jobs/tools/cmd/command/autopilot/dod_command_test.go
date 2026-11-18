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
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod/dodtest"
)

// mechanicalRepo is a stdlib-only module whose head raises the go directive,
// so the real go command runs offline.
func mechanicalRepo(t *testing.T) (*dodtest.Repo, string) {
	t.Helper()
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOTOOLCHAIN", "local")
	r := dodtest.NewRepo(t)
	r.Write("go.mod", "module example.com/c\n\ngo 1.22\n")
	r.Write("main.go", "package main\n\nfunc main() {}\n")
	base := r.Commit("base")
	r.Write("go.mod", "module example.com/c\n\ngo 1.23\n")
	r.Commit("go 1.23")
	return r, base
}

func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	var ec interface{ ExitCode() int }
	require.True(t, errors.As(err, &ec), "%v", err)
	return ec.ExitCode()
}

func TestRunDodPassWritesVerdictAndTrailer(t *testing.T) {
	r, base := mechanicalRepo(t)
	dir := t.TempDir()
	f := dodFlags{repoDir: r.Dir, service: "c", baseRef: base, headRef: "HEAD", changeClass: "mechanical", output: "json",
		verdictOut: filepath.Join(dir, "verdict.json"), trailerOut: filepath.Join(dir, "gate-verdict.txt")}
	var out bytes.Buffer
	require.NoError(t, runDod(context.Background(), f, strings.NewReader(""), &out, io.Discard))

	b, err := os.ReadFile(f.verdictOut)
	require.NoError(t, err)
	assert.Equal(t, out.String(), string(b), "stdout is the exact bytes that were hashed")
	tr, err := os.ReadFile(f.trailerOut)
	require.NoError(t, err)
	assert.Equal(t, dod.Trailer(b)+"\n", string(tr))
	v, err := dod.ParseVerdict(b)
	require.NoError(t, err)
	assert.Equal(t, dod.StatusPass, v.Verdict)

	body := filepath.Join(dir, "body.md")
	require.NoError(t, os.WriteFile(body, []byte("Bump.\n\nProducer: autopilot/x\n"+dod.Trailer(b)+"\n"), 0o644))
	var vout bytes.Buffer
	require.NoError(t, runVerify(context.Background(), verifyFlags{file: f.verdictOut, repoDir: r.Dir, commit: "HEAD", prBody: body}, &vout))
	assert.Contains(t, vout.String(), "verdict ok")

	require.NoError(t, os.WriteFile(body, []byte("no trailer\n"), 0o644))
	assert.Equal(t, 2, exitCodeOf(t, runVerify(context.Background(), verifyFlags{file: f.verdictOut, repoDir: r.Dir, commit: "HEAD", prBody: body}, io.Discard)))
}

func TestRunDodExitCodes(t *testing.T) {
	r, base := mechanicalRepo(t)
	r.Write("pkg/x.go", "package pkg\n")
	r.Commit("hand-written")
	f := dodFlags{repoDir: r.Dir, service: "c", baseRef: base, headRef: "HEAD", changeClass: "mechanical", output: "text"}
	var out bytes.Buffer
	err := runDod(context.Background(), f, strings.NewReader(""), &out, io.Discard)
	assert.Equal(t, 1, exitCodeOf(t, err))
	assert.Contains(t, out.String(), "verdict fail")

	f.changeClass = "breaking-API"
	assert.Equal(t, 2, exitCodeOf(t, runDod(context.Background(), f, strings.NewReader(""), io.Discard, io.Discard)))
	f.changeClass, f.output = "mechanical", "yaml"
	assert.Equal(t, 2, exitCodeOf(t, runDod(context.Background(), f, strings.NewReader(""), io.Discard, io.Discard)))
	f.output, f.crdCompat, f.prBody = "text", "-", "-"
	assert.Equal(t, 2, exitCodeOf(t, runDod(context.Background(), f, strings.NewReader(""), io.Discard, io.Discard)))
}

// TestVerifyVerdictExitCodes: 0 only for a genuine passing verdict; 1 for a
// genuine failing one (0 with --allow-fail); 2 for anything not genuine.
func TestVerifyVerdictExitCodes(t *testing.T) {
	ctx := context.Background()
	r, base := mechanicalRepo(t)
	dir := t.TempDir()
	pass := filepath.Join(dir, "pass.json")
	f := dodFlags{repoDir: r.Dir, service: "c", baseRef: base, headRef: "HEAD", changeClass: "mechanical", output: "json", verdictOut: pass}
	require.NoError(t, runDod(ctx, f, strings.NewReader(""), io.Discard, io.Discard))
	pb, err := os.ReadFile(pass)
	require.NoError(t, err)

	r.Write("pkg/x.go", "package pkg\n")
	r.Commit("hand-written")
	failing := filepath.Join(dir, "fail.json")
	f.verdictOut = failing
	require.Equal(t, 1, exitCodeOf(t, runDod(ctx, f, strings.NewReader(""), io.Discard, io.Discard)))
	fb, err := os.ReadFile(failing)
	require.NoError(t, err)

	verify := func(file, commit, want string, allowFail bool) error {
		return runVerify(ctx, verifyFlags{file: file, repoDir: r.Dir, commit: commit, gateVerdict: want, allowFail: allowFail}, io.Discard)
	}
	assert.NoError(t, verify(pass, "HEAD~1", dod.Digest(pb), false), "genuine pass")
	assert.Equal(t, 1, exitCodeOf(t, verify(failing, "HEAD", dod.Digest(fb), false)), "genuine fail")
	assert.NoError(t, verify(failing, "HEAD", dod.Digest(fb), true), "--allow-fail: genuine is enough")

	assert.Equal(t, 2, exitCodeOf(t, verify(pass, "HEAD~1", dod.Digest(fb), false)), "digest mismatch")
	assert.Equal(t, 2, exitCodeOf(t, verify(pass, "HEAD", dod.Digest(pb), false)), "tree mismatch")
	assert.Equal(t, 2, exitCodeOf(t, verify(failing, "HEAD", dod.Digest(pb), true)), "--allow-fail never accepts a forgery")

	forged := filepath.Join(dir, "forged.json")
	fake := strings.Replace(string(fb), `"verdict": "fail"`, `"verdict": "pass"`, 1)
	require.NotEqual(t, string(fb), fake)
	require.NoError(t, os.WriteFile(forged, []byte(fake), 0o644))
	assert.Equal(t, 2, exitCodeOf(t, verify(forged, "HEAD", dod.Digest(fb), false)), "edited verdict")
}

// TestRunDodBuildSandbox (Plan 9 review F1): --build-sandbox runs every go
// step through one executable; one that cannot run fails the build closed.
func TestRunDodBuildSandbox(t *testing.T) {
	require.NotNil(t, dodCmd.Flags().Lookup("build-sandbox"))
	r, base := mechanicalRepo(t)
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	ok := filepath.Join(dir, "ok.sh")
	require.NoError(t, os.WriteFile(ok, []byte("#!/bin/sh\necho \"$*\" >> "+log+"\nexec \"$@\"\n"), 0o755))
	bad := filepath.Join(dir, "bad.sh")
	require.NoError(t, os.WriteFile(bad, []byte("#!/bin/sh\nexit 125\n"), 0o755))

	f := dodFlags{repoDir: r.Dir, service: "c", baseRef: base, headRef: "HEAD", changeClass: "mechanical", output: "json",
		buildSandbox: ok}
	var out bytes.Buffer
	require.NoError(t, runDod(context.Background(), f, strings.NewReader(""), &out, io.Discard))
	calls, err := os.ReadFile(log)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(calls), "true\ngo build ./...\n"), "%s", calls)

	f.buildSandbox = bad
	out.Reset()
	assert.Equal(t, 1, exitCodeOf(t, runDod(context.Background(), f, strings.NewReader(""), &out, io.Discard)))
	v, err := dod.ParseVerdict(out.Bytes())
	require.NoError(t, err)
	assert.Contains(t, v.Reasons, "build:BUILD_SANDBOX_FAILED")
}
