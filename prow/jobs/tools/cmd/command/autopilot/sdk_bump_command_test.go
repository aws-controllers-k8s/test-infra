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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dispatcher"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/sdkbump"
)

func bumpFlags(t *testing.T) sdkBumpFlags {
	return sdkBumpFlags{
		service: "s3", sdkTo: "service/s3/v1.97.3", mode: sdkbump.ModeBump, org: "aws-controllers-k8s",
		controllerDir: t.TempDir(), codegenRepo: t.TempDir(), runtimeRepo: t.TempDir(),
		botName: "ack-bot", botEmail: "ack-bot@users.noreply.github.com", botLogin: "ack-bot",
		artifactsDir: t.TempDir(), timeout: time.Minute,
	}
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func readRecorded(t *testing.T, f sdkBumpFlags) dispatcher.RunResult {
	b, err := os.ReadFile(filepath.Join(f.artifactsDir, "agent-result.json"))
	require.NoError(t, err)
	rr, err := dispatcher.ParseResult(b, "")
	require.NoError(t, err)
	return rr
}

// sdkBumpExitCode is 0 for nil, -1 for an error without an exit code (dod_command_test.go has exitCodeOf).
func sdkBumpExitCode(err error) int {
	var ec interface{ ExitCode() int }
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	if err != nil {
		return -1
	}
	return 0
}

func TestSDKBumpInvalidInputRecordsAResult(t *testing.T) {
	f := bumpFlags(t)
	f.service = "S3"
	err := runSDKBump(context.Background(), f, env(nil), &bytes.Buffer{})
	assert.Equal(t, 1, sdkBumpExitCode(err))
	assert.Equal(t, "INVALID_INPUT", readRecorded(t, f).Reason)
}

func TestSDKBumpWriteNeedsAToken(t *testing.T) {
	f := bumpFlags(t)
	f.write = true
	err := runSDKBump(context.Background(), f, env(nil), &bytes.Buffer{})
	assert.Equal(t, 1, sdkBumpExitCode(err))
	rr := readRecorded(t, f)
	assert.Equal(t, [2]string{"needs_human", "INVALID_INPUT"}, [2]string{rr.Outcome, rr.Reason})
}

func TestSDKBumpDefaultIsADryRun(t *testing.T) {
	f := bumpFlags(t) // an empty controller dir: the run fails early, still a dry run
	var out bytes.Buffer
	_ = runSDKBump(context.Background(), f, env(map[string]string{"GITHUB_TOKEN": ""}), &out)
	rr := readRecorded(t, f)
	assert.Equal(t, "needs_human", rr.Outcome)
	assert.Equal(t, "DRY_RUN_NO_WRITES", rr.Reason)
	assert.Contains(t, out.String(), "dry run")
	assert.FileExists(t, filepath.Join(f.artifactsDir, "plan.txt"))
}

func TestSDKBumpFromAnEmptyLedger(t *testing.T) {
	f := bumpFlags(t)
	f.sdkTo, f.fromLedger, f.ledgerDir = "", true, t.TempDir()
	err := runSDKBump(context.Background(), f, env(nil), &bytes.Buffer{})
	assert.NoError(t, err)
	rr := readRecorded(t, f)
	assert.Equal(t, [2]string{"completed_no_change", "NO_BUMP_ITEM"}, [2]string{rr.Outcome, rr.Reason})
}

func TestSDKBumpFromLedgerRefusesExplicitArgs(t *testing.T) {
	f := bumpFlags(t)
	f.fromLedger, f.ledgerDir = true, t.TempDir()
	err := runSDKBump(context.Background(), f, env(nil), &bytes.Buffer{})
	assert.Equal(t, 1, sdkBumpExitCode(err))
	assert.Equal(t, "INVALID_INPUT", readRecorded(t, f).Reason)
}

func TestSDKBumpIsRegistered(t *testing.T) {
	c, _, err := Command.Find([]string{"sdk-bump"})
	require.NoError(t, err)
	assert.Equal(t, "sdk-bump", c.Name())
	w := c.Flags().Lookup("write")
	require.NotNil(t, w)
	assert.Equal(t, "false", w.DefValue)
}
