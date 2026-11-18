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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dispatcher"
)

func withArgs(req dispatcher.LaunchRequest, edit func(map[string]string)) dispatcher.LaunchRequest {
	args := map[string]string{}
	for k, v := range req.Args {
		args[k] = v
	}
	edit(args)
	req.Args = args
	return req
}

func TestBuildRefuses(t *testing.T) {
	c := testCatalog(t)
	for name, tc := range map[string]struct {
		req  dispatcher.LaunchRequest
		want string
	}{
		"workflow off the allowlist": {func() dispatcher.LaunchRequest { r := goldenAddField; r.Workflow = "fix-bug"; return r }(), "not on the allowlist"},
		"address-feedback":           {func() dispatcher.LaunchRequest { r := goldenAddField; r.Workflow = "address-feedback"; return r }(), "not on the allowlist"},
		"not a launch id": {func() dispatcher.LaunchRequest {
			r := goldenAddField
			r.LaunchID = "periodic-agent-add-field-1"
			return r
		}(), "not a dispatcher launch id"},
		"refresh id for add-field": {func() dispatcher.LaunchRequest {
			r := goldenAddField
			r.LaunchID = "autopilot-0123456789abcdef-g1-f0123abcd"
			return r
		}(), "names a refresh"},
		"refresh id without mode": {func() dispatcher.LaunchRequest {
			r := goldenSDKBump
			r.LaunchID = "autopilot-0123456789abcdef-g1-f0123abcd"
			return r
		}(), "names a refresh"},
		"missing required":          {withArgs(goldenAddField, func(a map[string]string) { delete(a, "field") }), `missing required arg "field"`},
		"undeclared arg":            {withArgs(goldenAddField, func(a map[string]string) { a["review-id"] = "1" }), `"review-id" is not in`},
		"declared, no validator":    {withArgs(goldenAddField, func(a map[string]string) { a["model"] = "x" }), `"model" is not one the Dispatcher may pass`},
		"field injection":           {withArgs(goldenAddField, func(a map[string]string) { a["field"] = "a; rm -rf /" }), `"field" has a value of the wrong shape`},
		"long field":                {withArgs(goldenAddField, func(a map[string]string) { a["field"] = "a" + strings.Repeat(".b", 200) }), `"field" has a value of the wrong shape`},
		"service with a slash":      {withArgs(goldenAddField, func(a map[string]string) { a["service"] = "s3/../x" }), `"service" has a value`},
		"other item's work-item":    {withArgs(goldenAddField, func(a map[string]string) { a["work-item"] = "sha256:ffffffffffffffff" }), "launch id's key hash"},
		"wrong class for add-field": {withArgs(goldenAddField, func(a map[string]string) { a["change-class"] = "SDK-bump" }), `change-class must be "config-only"`},
		"wrong class for sdk-bump":  {withArgs(goldenSDKBump, func(a map[string]string) { a["change-class"] = "config-only" }), `change-class must be "SDK-bump"`},
		"bad sdk-to":                {withArgs(goldenSDKBump, func(a map[string]string) { a["sdk-to"] = "v1.2.3" }), `"sdk-to" has a value`},
		"bad mode":                  {withArgs(goldenSDKBump, func(a map[string]string) { a["mode"] = "push" }), `"mode" has a value`},
		"unknown detector work":     {withArgs(goldenAddField, func(a map[string]string) { a["detector-work"] = "from-sibling" }), `"detector-work" has a value`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := c.Build(testConfig, tc.req)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestBuildConfigAndTimeout(t *testing.T) {
	c := testCatalog(t)
	for name, cfg := range map[string]Config{
		"bucket not s3":   {Bucket: "gs://x", Org: "aws-controllers-k8s", Now: testConfig.Now},
		"bucket with dot": {Bucket: "s3://a.b", Org: "aws-controllers-k8s", Now: testConfig.Now},
		"bad org":         {Bucket: testConfig.Bucket, Org: "a/b", Now: testConfig.Now},
		"no clock":        {Bucket: testConfig.Bucket, Org: "aws-controllers-k8s"},
	} {
		_, err := c.Build(cfg, goldenAddField)
		assert.Error(t, err, name)
	}
	short := goldenAddField
	short.Timeout = 90 * time.Minute
	pj, err := c.Build(testConfig, short)
	require.NoError(t, err)
	assert.Equal(t, "1h30m0s", pj.Spec.DecorationConfig.Timeout, "the shorter of the request and the workflow")
	long := goldenSDKBump
	pj, err = c.Build(testConfig, long)
	require.NoError(t, err)
	assert.Equal(t, "3h0m0s", pj.Spec.DecorationConfig.Timeout, "never longer than the workflow's")

	refresh := withArgs(goldenSDKBump, func(a map[string]string) { a["mode"] = ModeRefresh })
	refresh.LaunchID = "autopilot-0123456789abcdef-g1-f0123abcd"
	pj, err = c.Build(testConfig, refresh)
	require.NoError(t, err)
	assert.Equal(t, "f0123abcd", pj.Metadata.Labels[LabelRun])
	assert.Contains(t, pj.Spec.PodSpec.Containers[0].Env, EnvVar{Name: "AUTOPILOT_SDK_BUMP_MODE", Value: "refresh"})
	assert.Empty(t, pj.Spec.PodSpec.Containers[0].Args, "sdk-bump takes its args as environment")
}
