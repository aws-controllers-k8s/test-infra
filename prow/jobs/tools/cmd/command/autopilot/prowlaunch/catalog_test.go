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

package prowlaunch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/sdkbump"
)

var testVars = Vars{ImagesRepo: "registry.example/prow", TestInfraOrg: "aws-controllers-k8s"}

func testCatalog(t *testing.T) *Catalog {
	t.Helper()
	aw, err := LoadAgentWorkflows("testdata/agent-workflows.yaml", testVars)
	require.NoError(t, err)
	image, err := LoadImage("testdata/images_config.yaml", SDKBumpImageKey, testVars)
	require.NoError(t, err)
	c, err := NewCatalog(aw, SDKBump(image, testVars.TestInfraOrg))
	require.NoError(t, err)
	return c
}

func TestVarsAreResolvedAndChecked(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wf.yaml")
	write := func(s string) {
		require.NoError(t, os.WriteFile(path, []byte(s), 0o644))
	}
	write("workflows:\n  add-field:\n    image: ${PROW_IMAGES_REPO_URI}:t\n    command: [x]\n    timeout: 1h\n    environment:\n      GITHUB_ORG: ${TEST_INFRA_ORG}\n")
	aw, err := LoadAgentWorkflows(path, testVars)
	require.NoError(t, err)
	assert.Equal(t, "registry.example/prow:t", aw["add-field"].Image)
	assert.Equal(t, "aws-controllers-k8s", aw["add-field"].Environment["GITHUB_ORG"])

	_, err = LoadAgentWorkflows(path, Vars{ImagesRepo: "evil\n  x: y", TestInfraOrg: "aws-controllers-k8s"})
	assert.ErrorContains(t, err, "not an image repository")
	_, err = LoadAgentWorkflows(path, Vars{ImagesRepo: "registry.example/prow"})
	assert.ErrorContains(t, err, "not a GitHub org")

	write("workflows:\n  add-field:\n    image: ${OTHER}:t\n    command: [x]\n    timeout: 1h\n")
	_, err = LoadAgentWorkflows(path, testVars)
	assert.ErrorContains(t, err, "unresolved ${OTHER}")

	write("workflows:\n  add-field:\n    image: r:t\n    command: [x]\n    timeout: 1h\n    privileged: true\n")
	_, err = LoadAgentWorkflows(path, testVars)
	assert.ErrorContains(t, err, "privileged", "an unknown key that could shape the pod fails")

	_, err = NewCatalog(map[string]Workflow{}, SDKBump("r:t", "o"))
	assert.ErrorContains(t, err, "no add-field")
}

// TestTheRealAgentWorkflowsLoad reads the generated file this repo deploys:
// add-field must load, and accept every arg the config-only lane passes.
func TestTheRealAgentWorkflowsLoad(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "..", "..", "..")
	c, err := FromCheckout(root, testVars)
	require.NoError(t, err)
	af := c.workflows["add-field"]
	assert.Equal(t, []string{"service", "resource", "field"}, af.RequiredArgs)
	for _, a := range []string{"work-item", "change-class", "detector-work"} {
		assert.Contains(t, af.OptionalArgs, a)
	}
	assert.True(t, strings.HasPrefix(af.Image, "registry.example/prow:add-resource-"), af.Image)
	assert.True(t, strings.HasPrefix(c.workflows["sdk-bump"].Image, "registry.example/prow:prow-auto-generate-controllers-"))
}

func TestModesMatchSDKBump(t *testing.T) {
	assert.Equal(t, sdkbump.ModeBump, ModeBump)
	assert.Equal(t, sdkbump.ModeRefresh, ModeRefresh)
}
