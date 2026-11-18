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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod"
)

func TestDodKeygenWritesSeedAndPrintsPublicKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seed")
	var out bytes.Buffer
	require.NoError(t, runDodKeygen(path, &out))

	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	priv, err := dod.LoadSigningKey(path)
	require.NoError(t, err)
	pub, err := dod.ParsePublicKey(out.String())
	require.NoError(t, err)
	assert.Equal(t, priv.Public(), pub)
	assert.True(t, strings.HasSuffix(out.String(), "\n"))
	msg := []byte("v")
	require.NoError(t, dod.VerifySignature(pub, msg, dod.Sign(priv, msg)))
}

func TestDodKeygenRefusesToOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seed")
	require.NoError(t, os.WriteFile(path, []byte("keep"), 0o600))
	var out bytes.Buffer
	assert.Error(t, runDodKeygen(path, &out))
	assert.Empty(t, out.String())
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "keep", string(b))

	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "absent"), link))
	assert.Error(t, runDodKeygen(link, &out), "a dangling symlink is not followed")
	assert.Error(t, runDodKeygen("", &out))
}

func TestDodKeygenIsRegistered(t *testing.T) {
	c, _, err := Command.Find([]string{"dod-keygen"})
	require.NoError(t, err)
	assert.Equal(t, "dod-keygen", c.Name())
	assert.NotNil(t, c.Flags().Lookup("private-out"))
}
