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
	"errors"
	"testing"

	"github.com/google/go-github/v63/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dispatcher"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/prs"
)

// noPRs is a PRClient for command tests whose fake GitHub serves only
// ack-private: no item has a PR, and no repo has an open one.
type noPRs struct{}

func (noPRs) PR(context.Context, string, int) (dispatcher.PR, error) {
	return dispatcher.PR{}, errors.New("no PRs in this test")
}
func (noPRs) Commits(context.Context, string, int) ([]dispatcher.PRCommit, error) { return nil, nil }
func (noPRs) Reviews(context.Context, string, int) ([]dispatcher.PRReview, error) { return nil, nil }
func (noPRs) TreeSHA(context.Context, string, string) (string, error)             { return "", nil }
func (noPRs) IsAncestor(context.Context, string, string, string) (bool, error)    { return false, nil }
func (noPRs) OpenPRs(context.Context, string) ([]dispatcher.PR, error)            { return nil, nil }

func init() {
	prReader = func(*github.Client) dispatcher.PRClient { return noPRs{} }
}

func TestPRWriterNeedsTheGateAndItsToken(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	w, err := prWriterFor(env(nil))
	require.NoError(t, err)
	assert.Nil(t, w, "off unless AUTOPILOT_PR_WRITE=true")

	_, err = prWriterFor(env(map[string]string{prWriteEnv: "true"}))
	assert.ErrorContains(t, err, prWriteTokenEnv)

	w, err = prWriterFor(env(map[string]string{prWriteEnv: "true", prWriteTokenEnv: "t"}))
	require.NoError(t, err)
	assert.IsType(t, &prs.Writer{}, w)

	w, err = prWriterFor(env(map[string]string{prWriteEnv: "yes", prWriteTokenEnv: "t"}))
	require.NoError(t, err)
	assert.Nil(t, w, "only the exact value true")
}

func TestPROptionsValidate(t *testing.T) {
	assert.NoError(t, prOptions{SDKBumpAuthor: "ack-bot", AgentAuthor: "ack-agent"}.validate())
	assert.Error(t, prOptions{SDKBumpAuthor: "", AgentAuthor: "ack-agent"}.validate())
	assert.Error(t, prOptions{SDKBumpAuthor: "ack-bot", AgentAuthor: "a b"}.validate())
	assert.Equal(t, map[string]string{"sdk-bump": "ack-bot", "add-field": "ack-agent", "add-resource": "ack-agent"},
		prOptions{SDKBumpAuthor: "ack-bot", AgentAuthor: "ack-agent"}.prAuthors())
}
