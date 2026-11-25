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

// withTriage is withFiles (Plan 7's form, no edits) plus testdata/triage.yaml
// (old -> new edits).
func withTriage(t *testing.T, edits ...[2]string) string {
	t.Helper()
	root := withFiles(t)
	b, err := os.ReadFile(filepath.Join("testdata", "triage.yaml"))
	require.NoError(t, err)
	s := string(b)
	for _, e := range edits {
		require.Contains(t, s, e[0])
		s = strings.Replace(s, e[0], e[1], 1)
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, TriageFile), []byte(s), 0o644))
	return root
}

func TestTriagePolicyIsOptional(t *testing.T) {
	root := withFiles(t)
	p, err := Load(root)
	require.NoError(t, err)
	assert.Nil(t, p.Triage, "no policy/triage.yaml: no triage lane")
	assert.Empty(t, Lint(root))
}

func TestLoadTriagePolicy(t *testing.T) {
	root := withTriage(t)
	p, err := Load(root)
	require.NoError(t, err)
	require.NotNil(t, p.Triage)
	assert.Equal(t, Triage{
		Modes: []string{"api"}, MaxConcurrentTriageRuns: 1, MaxTriageLaunchesPerDay: 10,
		MaxTriageRunsPerItem: 2, JobTimeoutMinutes: 30, ReservedReviewSlotsTriaged: 2,
	}, *p.Triage)
	assert.Equal(t, 30*time.Minute, p.Triage.JobTimeout())
	assert.True(t, p.Triage.ModeEnabled("api"))
	assert.False(t, p.Triage.ModeEnabled("e2e-flake"))
	assert.Empty(t, Lint(root))
}

func TestTriagePolicyFailsClosed(t *testing.T) {
	for name, edit := range map[string][2]string{
		"unknown mode":                 {"modes: [api]", "modes: [api, stat]"},
		"negative cap":                 {"max_concurrent_triage_runs: 1", "max_concurrent_triage_runs: -1"},
		"no runs":                      {"max_triage_runs_per_item: 2", "max_triage_runs_per_item: 0"},
		"timeout too long":             {"job_timeout_minutes: 30", "job_timeout_minutes: 61"},
		"timeout too short":            {"job_timeout_minutes: 30", "job_timeout_minutes: 4"},
		"reservation above review cap": {"reserved_review_slots_triaged: 2", "reserved_review_slots_triaged: 7"},
		"missing key":                  {"max_triage_launches_per_day: 10\n", ""},
		// Phase 1 has no auto-accept, so there is no key for it.
		"unknown key": {"modes: [api]", "modes: [api]\nauto_accept: [api/field-config]"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(withTriage(t, edit))
			assert.Error(t, err)
			assert.NotEmpty(t, Lint(withTriage(t, edit)))
		})
	}
}

func TestTriagePolicyRejectsSymlink(t *testing.T) {
	root := withTriage(t)
	target := filepath.Join(root, "elsewhere.yaml")
	require.NoError(t, os.Rename(filepath.Join(root, TriageFile), target))
	require.NoError(t, os.Symlink(target, filepath.Join(root, TriageFile)))
	_, err := Load(root)
	assert.ErrorContains(t, err, "regular file")
}
