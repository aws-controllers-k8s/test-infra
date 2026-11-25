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

package dispatcher

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/agent-result-v1.json")
	require.NoError(t, err)
	return b
}

func TestParseResultFixture(t *testing.T) {
	r, err := ParseResult(fixture(t), "sha256:0123456789abcdef")
	require.NoError(t, err)
	assert.Equal(t, RunResult{
		Outcome: "pr_opened",
		PR:      "https://github.com/aws-controllers-k8s/s3-controller/pull/240",
		Tokens:  1280000,
		E2ERuns: 1,
	}, r)
}

func TestParseResultRejects(t *testing.T) {
	good := string(fixture(t))
	for name, tc := range map[string]struct {
		data, workItem, want string
	}{
		"other work item":  {good, "sha256:ffffffffffffffff", "work item"},
		"no work item":     {strings.Replace(good, `"sha256:0123456789abcdef"`, `""`, 1), "sha256:0123456789abcdef", "work item"},
		"schema":           {strings.Replace(good, "result/v1", "result/v2", 1), "", "schema"},
		"not json":         {"{", "", "decode"},
		"negative tokens":  {strings.Replace(good, `"total": 1280000`, `"total": -1`, 1), "", "negative"},
		"negative e2e":     {strings.Replace(good, `"attempts": 1`, `"attempts": -1`, 1), "", "negative"},
		"negative impl":    {strings.Replace(good, `"impl": 2`, `"impl": -1`, 1), "", "negative"},
		"negative replan":  {strings.Replace(good, `"replan": 0`, `"replan": -1`, 1), "", "negative"},
		"too large":        {good + strings.Repeat(" ", maxResultBytes), "", "larger"},
		"trailing garbage": {good + "{}", "", "decode"},
		"trailing brace":   {good + "}", "", "data after the object"},
		"trailing bracket": {good + "]", "", "data after the object"},
		"tokens too big":   {strings.Replace(good, `"total": 1280000`, `"total": 1099511627777`, 1), "", "out of range"},
		"input too big":    {strings.Replace(good, `"input": 1200000`, `"input": 1099511627777`, 1), "", "out of range"},
		"attempts too big": {strings.Replace(good, `"attempts": 1`, `"attempts": 1001`, 1), "", "out of range"},
		"impl too big":     {strings.Replace(good, `"impl": 2`, `"impl": 1001`, 1), "", "out of range"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseResult([]byte(tc.data), tc.workItem)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestParseResultIgnoresUnknownFields(t *testing.T) {
	data := strings.Replace(string(fixture(t)), `"workflow"`, `"later_field": 1, "workflow"`, 1)
	r, err := ParseResult([]byte(data), "")
	require.NoError(t, err)
	want, err := ParseResult(fixture(t), "")
	require.NoError(t, err)
	assert.Equal(t, want, r)
	assert.Equal(t, int64(1280000), r.Tokens)
}

func TestParseResultAcceptsLimits(t *testing.T) {
	good := string(fixture(t))
	data := strings.Replace(good, `"total": 1280000`, `"total": 1099511627776`, 1)
	data = strings.Replace(data, `"attempts": 1`, `"attempts": 1000`, 1)
	r, err := ParseResult([]byte(data), "")
	require.NoError(t, err)
	assert.Equal(t, int64(1<<40), r.Tokens)
	assert.Equal(t, 1000, r.E2ERuns)
}
