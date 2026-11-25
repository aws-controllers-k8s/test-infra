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
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/google/go-github/v63/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dispatcher"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
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

func TestDodPublicKeyFromEnv(t *testing.T) {
	k, err := dodPublicKeyFor(envMap(nil))
	require.NoError(t, err)
	assert.Nil(t, k, "empty: no key; dod gates wait")

	_, pub, err := dod.GenerateKey()
	require.NoError(t, err)
	k, err = dodPublicKeyFor(envMap(map[string]string{dodPublicKeyEnv: pub}))
	require.NoError(t, err)
	want, err := dod.ParsePublicKey(pub)
	require.NoError(t, err)
	assert.Equal(t, want, k)

	for _, bad := range []string{"not base64!", base64.StdEncoding.EncodeToString(make([]byte, 31))} {
		_, err = dodPublicKeyFor(envMap(map[string]string{dodPublicKeyEnv: bad}))
		assert.ErrorContains(t, err, dodPublicKeyEnv, bad)
	}
	assert.Equal(t, "AUTOPILOT_DOD_PUBLIC_KEY", dodPublicKeyEnv)
}

// TestDispatchRefusesAnInvalidDodPublicKey: an invalid key stops dispatch
// before the tick; nothing is read, ticked or written.
func TestDispatchRefusesAnInvalidDodPublicKey(t *testing.T) {
	saved := getenv
	t.Cleanup(func() { getenv = saved })
	getenv = envMap(map[string]string{dodPublicKeyEnv: "AAAA"})
	dir := t.TempDir()
	copyPolicy(t, dir)
	var out strings.Builder
	err := dispatch(context.Background(), fakeGitHub(t), opts(dir), tickTime, &out)
	assert.ErrorContains(t, err, dodPublicKeyEnv)
	assert.Empty(t, out.String())
	items, lerr := ledger.Load(dir)
	require.NoError(t, lerr)
	assert.Empty(t, items)
}

// TestDispatchPeriodicNamesTheDodPublicKey: the periodic's template and
// rendered job set AUTOPILOT_DOD_PUBLIC_KEY (empty until the key exists)
// right after AUTOPILOT_PR_WRITE, identically.
func TestDispatchPeriodicNamesTheDodPublicKey(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "..", "..")
	block := regexp.MustCompile(`(?s)          - name: AUTOPILOT_PR_WRITE\n            value: "false"\n((?:          #[^\n]*\n)+          - name: AUTOPILOT_DOD_PUBLIC_KEY\n            value: ""\n)`)
	var got []string
	for _, f := range []string{filepath.Join("prow", "jobs", "templates", "periodics", "autopilot-dispatch.tpl"), filepath.Join("prow", "jobs", "jobs.yaml")} {
		b, err := os.ReadFile(filepath.Join(root, f))
		require.NoError(t, err)
		m := block.FindStringSubmatch(string(b))
		require.NotNil(t, m, "%s sets AUTOPILOT_DOD_PUBLIC_KEY under AUTOPILOT_PR_WRITE", f)
		assert.Contains(t, m[1], "autopilot-dod-signing-key", f)
		assert.Contains(t, m[1], "dod-keygen", f)
		assert.Contains(t, m[1], "not a secret", f)
		got = append(got, m[1])
	}
	assert.Equal(t, got[0], got[1], "edited identically in both files")
}
