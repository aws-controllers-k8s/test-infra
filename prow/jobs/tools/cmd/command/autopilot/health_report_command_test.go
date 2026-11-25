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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/policy"
)

// reportLedger is a ledger written by one dispatch tick, with policy.
func reportLedger(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	copyPolicy(t, dir)
	require.NoError(t, dispatch(context.Background(), fakeGitHub(t), opts(dir), tickTime, &strings.Builder{}))
	return dir
}

func TestHealthReportWritesTheDatedFile(t *testing.T) {
	dir := reportLedger(t)
	var out strings.Builder
	// The default date is the previous UTC day.
	require.NoError(t, healthReport(healthReportOptions{LedgerDir: dir}, tickTime.Add(24*time.Hour), &out))
	path := filepath.Join(dir, "reports", "2026-11-12.md")
	assert.Equal(t, "health-report "+path+" date=2026-11-12 items=2 runs-completed=0 runs-failed=0 escalated=0 gate-first-pass=0 gate-first-fail=0\n", out.String())
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(b), "# ACK Autopilot health report 2026-11-12\n"))
	assert.Contains(t, string(b), "| Detected | 2 | 2 |")

	// The report lives beside the ledger without upsetting its lint.
	assert.Empty(t, ledger.Lint(dir))
	assert.Empty(t, policy.Lint(dir))
	_, err = ledger.Load(dir)
	require.NoError(t, err)

	// Same ledger, same date, same bytes, whenever it runs.
	require.NoError(t, healthReport(healthReportOptions{LedgerDir: dir, Date: "2026-11-12"}, tickTime.Add(72*time.Hour), &strings.Builder{}))
	again, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(b), string(again))
}

func TestHealthReportOnce(t *testing.T) {
	dir := reportLedger(t)
	path := filepath.Join(dir, "reports", "2026-11-12.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("kept\n"), 0o644))
	var out strings.Builder
	require.NoError(t, healthReport(healthReportOptions{LedgerDir: dir, Date: "2026-11-12", Once: true}, tickTime, &out))
	assert.Equal(t, "health-report "+path+" exists; nothing written\n", out.String())
	b, _ := os.ReadFile(path)
	assert.Equal(t, "kept\n", string(b))

	// With --out elsewhere (a dry run), --once still looks in the ledger.
	other := filepath.Join(t.TempDir(), "r.md")
	require.NoError(t, healthReport(healthReportOptions{LedgerDir: dir, Date: "2026-11-12", Once: true, Out: other}, tickTime, &out))
	assert.NoFileExists(t, other)
	require.NoError(t, healthReport(healthReportOptions{LedgerDir: dir, Date: "2026-11-13", Once: true, Out: other}, tickTime, &out))
	assert.FileExists(t, other)
	assert.NoFileExists(t, filepath.Join(dir, "reports", "2026-11-13.md"), "--out writes nothing into the ledger")
}

func TestHealthReportRefusesSymlinks(t *testing.T) {
	dir := reportLedger(t)
	elsewhere := t.TempDir()
	require.NoError(t, os.Symlink(elsewhere, filepath.Join(dir, "reports")))
	err := healthReport(healthReportOptions{LedgerDir: dir, Date: "2026-11-12"}, tickTime, &strings.Builder{})
	assert.ErrorContains(t, err, "must be a real directory")
	entries, _ := os.ReadDir(elsewhere)
	assert.Empty(t, entries)

	dir = reportLedger(t)
	require.NoError(t, os.Mkdir(filepath.Join(dir, "reports"), 0o755))
	target := filepath.Join(elsewhere, "target")
	require.NoError(t, os.Symlink(target, filepath.Join(dir, "reports", "2026-11-12.md")))
	err = healthReport(healthReportOptions{LedgerDir: dir, Date: "2026-11-12"}, tickTime, &strings.Builder{})
	assert.ErrorContains(t, err, "not a regular file")
	assert.NoFileExists(t, target)
}

func TestHealthReportRejectsBadInput(t *testing.T) {
	dir := reportLedger(t)
	assert.ErrorContains(t, healthReport(healthReportOptions{LedgerDir: dir, Date: "18/11/2026"}, tickTime, &strings.Builder{}), "want YYYY-MM-DD")
	assert.ErrorContains(t, healthReport(healthReportOptions{LedgerDir: filepath.Join(dir, "missing")}, tickTime, &strings.Builder{}), "--ledger-dir")
}

func TestHealthReportCommandIsRegistered(t *testing.T) {
	c, _, err := Command.Find([]string{"health-report"})
	require.NoError(t, err)
	assert.Equal(t, "health-report", c.Name())
	for _, f := range []string{"ledger-dir", "date", "out", "once"} {
		assert.NotNil(t, c.Flags().Lookup(f), f)
	}
}
