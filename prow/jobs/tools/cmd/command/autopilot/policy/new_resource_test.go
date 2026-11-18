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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withNewResource is withFiles plus testdata/new-resource.yaml (old -> new
// edits).
func withNewResource(t *testing.T, edits ...[2]string) string {
	t.Helper()
	root := withFiles(t)
	b, err := os.ReadFile(filepath.Join("testdata", "new-resource.yaml"))
	require.NoError(t, err)
	s := string(b)
	for _, e := range edits {
		require.Contains(t, s, e[0])
		s = strings.Replace(s, e[0], e[1], 1)
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, NewResourceFile), []byte(s), 0o644))
	return root
}

func TestNewResourcePolicyIsOptional(t *testing.T) {
	p, err := Load(withFiles(t))
	require.NoError(t, err)
	assert.Nil(t, p.NewResource)
	assert.False(t, p.NewResourceEnabled(), "no policy/new-resource.yaml: the lane is off")
}

func TestLoadNewResourcePolicy(t *testing.T) {
	root := withNewResource(t)
	p, err := Load(root)
	require.NoError(t, err)
	require.NotNil(t, p.NewResource)
	assert.Equal(t, NewResource{Enabled: true, MaxLaunchesPerDay: 1, MaxOpenPRsPerRepo: 1}, *p.NewResource)
	assert.True(t, p.NewResourceEnabled())
	assert.Empty(t, Lint(root))

	off, err := Load(withNewResource(t, [2]string{"enabled: true", "enabled: false"}))
	require.NoError(t, err)
	assert.False(t, off.NewResourceEnabled())
}

func TestNewResourcePolicyFailsClosed(t *testing.T) {
	for name, edit := range map[string][2]string{
		"two launches a day": {"max_launches_per_day: 1", "max_launches_per_day: 2"},
		"negative launches":  {"max_launches_per_day: 1", "max_launches_per_day: -1"},
		"two open PRs":       {"max_open_prs_per_repo: 1", "max_open_prs_per_repo: 2"},
		"missing key":        {"max_open_prs_per_repo: 1\n", ""},
		"unknown key":        {"enabled: true", "enabled: true\nauto_merge: true"},
		"not a bool":         {"enabled: true", "enabled: yes-please"},
	} {
		t.Run(name, func(t *testing.T) {
			root := withNewResource(t, edit)
			_, err := Load(root)
			assert.Error(t, err)
			assert.NotEmpty(t, Lint(root))
		})
	}
}
