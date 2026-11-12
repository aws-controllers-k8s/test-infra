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

package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withFiles copies testdata into a temp ledger and applies edits (file -> old
// -> new) to the copies.
func withFiles(t *testing.T, edits ...[3]string) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "policy"), 0o755))
	for _, f := range []string{SwitchesFile, BudgetsFile} {
		b, err := os.ReadFile(filepath.Join("testdata", f))
		require.NoError(t, err)
		s := string(b)
		for _, e := range edits {
			if e[0] == f {
				require.Contains(t, s, e[1])
				s = strings.Replace(s, e[1], e[2], 1)
			}
		}
		require.NoError(t, os.WriteFile(filepath.Join(root, f), []byte(s), 0o644))
	}
	return root
}

func TestLoadPhase1Policy(t *testing.T) {
	p, err := Load("testdata")
	require.NoError(t, err)
	assert.False(t, p.Switches.Paused)
	assert.Equal(t, LauncherShadow, p.Switches.Launcher)
	assert.True(t, p.Switches.ServiceEnabled("s3"))
	assert.False(t, p.Switches.ServiceEnabled("sqs"))
	assert.False(t, p.Switches.RepoPaused("aws-controllers-k8s/s3-controller"))
	assert.True(t, p.Budgets.Expensive("ec2"))
	assert.False(t, p.Budgets.Expensive("s3"))
	assert.Equal(t, 5*time.Hour, p.Budgets.JobTimeout())
	assert.Equal(t, 30*time.Minute, p.Budgets.ReaperGrace())
	assert.Equal(t, 3, p.Budgets.MaxRunsPerItem)
	assert.Empty(t, Lint("testdata"))
}

func TestLoadFailsClosed(t *testing.T) {
	for name, edit := range map[string][3]string{
		"unknown launcher": {SwitchesFile, "launcher: shadow", "launcher: prowjob"},
		"unknown field":    {SwitchesFile, "paused: false", "paused: false\npasued: true"},
		"negative cap":     {BudgetsFile, "max_concurrent_llm_runs: 2", "max_concurrent_llm_runs: -1"},
		"no runs":          {BudgetsFile, "max_runs_per_item: 3", "max_runs_per_item: 0"},
		"timeout too long": {BudgetsFile, "job_timeout_minutes: 300", "job_timeout_minutes: 400"},
		"grace too short":  {BudgetsFile, "reaper_grace_minutes: 30", "reaper_grace_minutes: 5"},
		"bad service":      {SwitchesFile, "[s3, iam", `["s3|x", iam`},
		"bad repo":         {SwitchesFile, "paused_repos: []", "paused_repos: [s3-controller]"},
		"missing paused":   {SwitchesFile, "paused: false\n", ""},
		"missing max runs": {BudgetsFile, "max_runs_per_item: 3\n", ""},
		"bad expensive":    {BudgetsFile, "[ec2, rds", `["a b", rds`},
		"two documents":    {BudgetsFile, "reaper_grace_minutes: 30", "reaper_grace_minutes: 30\n---\npaused: true"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(withFiles(t, edit))
			assert.Error(t, err)
			assert.NotEmpty(t, Lint(withFiles(t, edit)))
		})
	}
}

func TestLoadMissingAndEmpty(t *testing.T) {
	_, err := Load(t.TempDir())
	assert.ErrorContains(t, err, "autopilot.yaml")

	root := withFiles(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, BudgetsFile), nil, 0o644))
	_, err = Load(root)
	assert.ErrorContains(t, err, "empty")
	assert.ErrorContains(t, err, "budgets.yaml")

	root = withFiles(t)
	require.NoError(t, os.Remove(filepath.Join(root, BudgetsFile)))
	_, err = Load(root)
	assert.ErrorContains(t, err, "budgets.yaml")
	assert.NotEmpty(t, Lint(root))
}

func TestLoadRejectsSymlinkedPolicyDir(t *testing.T) {
	root := withFiles(t)
	real := filepath.Join(t.TempDir(), "real")
	require.NoError(t, os.Rename(filepath.Join(root, "policy"), real))
	require.NoError(t, os.Symlink(real, filepath.Join(root, "policy")))
	_, err := Load(root)
	assert.ErrorContains(t, err, "regular directory")
	assert.NotEmpty(t, Lint(root))
}

func TestLoadRejectsSymlink(t *testing.T) {
	root := withFiles(t)
	target := filepath.Join(root, "elsewhere.yaml")
	require.NoError(t, os.Rename(filepath.Join(root, BudgetsFile), target))
	require.NoError(t, os.Symlink(target, filepath.Join(root, BudgetsFile)))
	_, err := Load(root)
	assert.ErrorContains(t, err, "regular file")
}
