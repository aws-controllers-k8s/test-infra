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
	assert.Equal(t, 1, exitCodeOf(t, runVerify(context.Background(), verifyFlags{file: f.verdictOut, repoDir: r.Dir, commit: "HEAD", prBody: body}, io.Discard)))
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
