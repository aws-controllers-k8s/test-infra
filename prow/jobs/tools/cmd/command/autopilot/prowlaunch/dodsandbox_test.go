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
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scriptsDir holds the job scripts the dod job runs.
var scriptsDir = filepath.Join("..", "..", "..", "..", "..", "..", "..", "scripts")

// TestDodBuildSandboxScript (Plan 9 review F1): the dod job's build runs as
// an unprivileged uid through scripts/autopilot-dod-sandbox.sh, which fails
// closed (125) unless it runs as root.
func TestDodBuildSandboxScript(t *testing.T) {
	sb := filepath.Join(scriptsDir, "autopilot-dod-sandbox.sh")
	fi, err := os.Stat(sb)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), fi.Mode().Perm())
	for _, s := range []string{sb, filepath.Join(scriptsDir, "autopilot-dod.sh")} {
		out, err := exec.Command("bash", "-n", s).CombinedOutput()
		assert.NoError(t, err, "%s: %s", s, out)
	}
	b, err := os.ReadFile(sb)
	require.NoError(t, err)
	for _, want := range []string{
		"setpriv --reuid=65532 --regid=65532 --clear-groups --no-new-privs",
		"env -i", "pkill -9 -U 65532", "pgrep -U 65532", `chmod -R go+rX "$PWD"`,
		"AUTOPILOT_DOD_SANDBOX_DIR:-/tmp/dod-sandbox", "Plan 9 review F1",
	} {
		assert.True(t, strings.Contains(string(b), want), "the sandbox script has %q", want)
	}
	if os.Geteuid() != 0 {
		err := exec.Command(sb, "true").Run()
		var ee *exec.ExitError
		require.True(t, errors.As(err, &ee), "%v", err)
		assert.Equal(t, 125, ee.ExitCode(), "not root: fails closed")
	}

	d, err := os.ReadFile(filepath.Join(scriptsDir, "autopilot-dod.sh"))
	require.NoError(t, err)
	for _, want := range []string{`--build-sandbox "$DIR/autopilot-dod-sandbox.sh"`, `chmod 0755 "$ARTIFACTS"`, "mktemp -d"} {
		assert.True(t, strings.Contains(string(d), want), "autopilot-dod.sh has %q", want)
	}
}
