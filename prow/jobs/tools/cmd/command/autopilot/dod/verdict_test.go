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
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sampleVerdict() *Verdict {
	return &Verdict{
		Schema: Schema,
		Tool:   Tool{Name: ToolName, Revision: "test"},
		Subject: Subject{
			Repo: "aws-controllers-k8s/s3-controller", Service: "s3",
			BaseSHA:  "1111111111111111111111111111111111111111",
			HeadTree: "2222222222222222222222222222222222222222",
		},
		Inputs:       Inputs{ChangeClass: "SDK-bump", WorkItem: "sha256:0123456789abcdef"},
		Verdict:      StatusFail,
		Reasons:      []string{"path-allowlist:PATH_NOT_ALLOWED"},
		ReclassifyTo: "hand-written-code",
		Checks: []Check{{
			ID: "path-allowlist", Binding: true, Status: StatusFail,
			Reasons: []string{"PATH_NOT_ALLOWED"}, Reclassify: "hand-written-code",
			Findings: []Finding{{Signature: "PATH_NOT_ALLOWED", File: "pkg/resource/bucket/hook.go", Fails: true}},
		}},
	}
}

const sampleGolden = `{
  "schema": "ack.autopilot.verdict/v1",
  "tool": {
    "name": "ack-build-tools autopilot dod",
    "revision": "test"
  },
  "subject": {
    "repo": "aws-controllers-k8s/s3-controller",
    "service": "s3",
    "base_sha": "1111111111111111111111111111111111111111",
    "head_tree": "2222222222222222222222222222222222222222"
  },
  "inputs": {
    "change_class": "SDK-bump",
    "work_item": "sha256:0123456789abcdef",
    "codegen_sha": "",
    "runtime_sha": "",
    "crd_compat_source": "",
    "crd_compat_sha256": "",
    "sdk_model": "",
    "sdk_model_sha256": "",
    "skipped_by_flag": []
  },
  "verdict": "fail",
  "reasons": [
    "path-allowlist:PATH_NOT_ALLOWED"
  ],
  "reclassify_to": "hand-written-code",
  "labels": [],
  "human_commits": 0,
  "not_covered": [],
  "checks": [
    {
      "id": "path-allowlist",
      "binding": true,
      "status": "fail",
      "reasons": [
        "PATH_NOT_ALLOWED"
      ],
      "reclassify": "hand-written-code",
      "labels": [],
      "findings": [
        {
          "signature": "PATH_NOT_ALLOWED",
          "file": "pkg/resource/bucket/hook.go",
          "fails": true
        }
      ]
    }
  ]
}
`

func TestMarshalGoldenVerdict(t *testing.T) {
	b, err := Marshal(sampleVerdict())
	require.NoError(t, err)
	assert.Equal(t, sampleGolden, string(b))
}

func TestMarshalIsOrderIndependent(t *testing.T) {
	a, b := sampleVerdict(), sampleVerdict()
	f1 := Finding{Signature: "B", File: "z.go", Line: 2}
	f2 := Finding{Signature: "A", File: "a.go", Line: 9}
	a.Checks[0].Findings = []Finding{f1, f2, f1}
	b.Checks[0].Findings = []Finding{f2, f1}
	a.Labels, b.Labels = []string{"y", "x", "x"}, []string{"x", "y"}
	ab, err := Marshal(a)
	require.NoError(t, err)
	bb, err := Marshal(b)
	require.NoError(t, err)
	assert.Equal(t, string(ab), string(bb))
}

func TestDigestAndTrailer(t *testing.T) {
	b := []byte("x\n")
	sum := sha256.Sum256(b)
	assert.Equal(t, "sha256:"+hex.EncodeToString(sum[:]), Digest(b))
	assert.Equal(t, "Gate-Verdict: "+Digest(b), Trailer(b))
}

func TestParseVerdictRoundTrip(t *testing.T) {
	b, err := Marshal(sampleVerdict())
	require.NoError(t, err)
	v, err := ParseVerdict(b)
	require.NoError(t, err)
	again, err := Marshal(v)
	require.NoError(t, err)
	assert.Equal(t, string(b), string(again))
}

func TestParseVerdictRejects(t *testing.T) {
	good, err := Marshal(sampleVerdict())
	require.NoError(t, err)
	for name, data := range map[string]string{
		"unknown field": `{"schema":"ack.autopilot.verdict/v1","verdict":"pass","extra":1}`,
		"trailing data": string(good) + "{}",
		"wrong schema":  `{"schema":"ack.autopilot.verdict/v0","verdict":"pass"}`,
		"skip verdict":  `{"schema":"ack.autopilot.verdict/v1","verdict":"skip"}`,
	} {
		_, err := ParseVerdict([]byte(data))
		assert.Error(t, err, name)
	}
}

func TestTrailerValueTakesTheLastTrailerBlock(t *testing.T) {
	body := "Summary.\n\nGate-Verdict: sha256:old\n\nMore text.\n\nProducer: autopilot/sdk-bump\nGate-Verdict: sha256:new\n"
	v, ok := TrailerValue([]byte(body))
	assert.True(t, ok)
	assert.Equal(t, "sha256:new", v)
	_, ok = TrailerValue([]byte("no trailers here"))
	assert.False(t, ok)
}
