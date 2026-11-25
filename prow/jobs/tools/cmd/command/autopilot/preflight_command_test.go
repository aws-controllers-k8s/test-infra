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
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod/dodtest"
)

// configRepo is a controller base and an agent commit that edits generator.yaml.
func configRepo(t *testing.T) (*dodtest.Repo, string) {
	t.Helper()
	r := dodtest.NewRepo(t)
	r.Write("go.mod", "module example.com/c\n\ngo 1.22\n")
	r.Write("generator.yaml", "ignore: {}\n")
	base := r.Commit("base")
	r.Write("generator.yaml", "ignore: {}\nresources: {}\n")
	r.Commit("Add a field")
	return r, base
}

func TestRunPreflightPassWritesTheReport(t *testing.T) {
	r, base := configRepo(t)
	report := filepath.Join(t.TempDir(), "preflight.txt")
	var out bytes.Buffer
	require.NoError(t, runPreflight(context.Background(), preflightFlags{
		repoDir: r.Dir, baseRef: base, headRef: "HEAD", changeClass: dod.ClassConfigOnly,
		output: "text", reportOut: report,
	}, &out))
	assert.Contains(t, out.String(), "preflight pass class=config-only")
	b, err := os.ReadFile(report)
	require.NoError(t, err)
	assert.Equal(t, out.String(), string(b))
}

func TestRunPreflightBlockedExitsOne(t *testing.T) {
	r, base := configRepo(t)
	body := filepath.Join(t.TempDir(), "body.md")
	require.NoError(t, os.WriteFile(body, []byte("key "+"AKIA"+"Z7QWERTYUIOPASDF\n"), 0o600))
	var out bytes.Buffer
	err := runPreflight(context.Background(), preflightFlags{
		repoDir: r.Dir, baseRef: base, headRef: "HEAD", changeClass: dod.ClassConfigOnly,
		prBody: body, output: "json",
	}, &out)
	assert.Equal(t, 1, exitCodeOf(t, err))
	var res dod.PreflightResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &res))
	assert.True(t, res.Blocked)
	assert.Equal(t, []string{"leak-inventory:AWS_ACCESS_KEY_ID"}, res.Reasons)
}

func TestRunPreflightUndecidedExitsTwo(t *testing.T) {
	r, base := configRepo(t)
	for name, f := range map[string]preflightFlags{
		"bad output":   {repoDir: r.Dir, baseRef: base, changeClass: dod.ClassConfigOnly, output: "yaml"},
		"missing body": {repoDir: r.Dir, baseRef: base, changeClass: dod.ClassConfigOnly, output: "text", prBody: "/nonexistent"},
		"bad class":    {repoDir: r.Dir, baseRef: base, changeClass: "x", output: "text"},
	} {
		t.Run(name, func(t *testing.T) {
			err := runPreflight(context.Background(), f, &bytes.Buffer{})
			assert.Equal(t, 2, exitCodeOf(t, err))
		})
	}
}
