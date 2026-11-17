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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadResultShowsTheTriageVerdict(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, readResult("dispatcher/testdata/agent-result-triage-v1.json", "sha256:0123456789abcdef", &out))
	assert.Equal(t, "result outcome=\"completed_no_change\" reason=\"TRIAGE_VERDICT\" pr=\"\" tokens=5380 e2e_runs=0\n"+
		"triage decision=\"field-hook\" class=\"hand-written-code\" refs=3 invalid=\"\"\n", out.String())

	out.Reset()
	require.NoError(t, readResult("dispatcher/testdata/agent-result-triage-v1.json", "", &out))
	assert.Contains(t, out.String(), "triage verdict present; pass --work-item to check it\n")
}
