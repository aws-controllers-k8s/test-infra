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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pins(t *testing.T, meta, gomod string) sdkPins {
	t.Helper()
	var m, g []byte
	if meta != "" {
		m = []byte(meta)
	}
	if gomod != "" {
		g = []byte(gomod)
	}
	p, err := readPins(m, g)
	require.NoError(t, err)
	return p
}

const goModBase = `module github.com/aws-controllers-k8s/s3-controller

go 1.24

require (
	github.com/aws/aws-sdk-go-v2 v1.41.5
	github.com/aws/aws-sdk-go-v2/service/s3 v1.97.3
	github.com/aws/smithy-go v1.24.0
	github.com/spf13/pflag v1.0.5
)
`

func TestSDKPin(t *testing.T) {
	base := pins(t, "aws_sdk_go_version: v1.41.5\n", goModBase)
	assert.Equal(t, map[string]string{
		"github.com/aws/aws-sdk-go-v2": "v1.41.5", "github.com/aws/aws-sdk-go-v2/service/s3": "v1.97.3",
		"github.com/aws/smithy-go": "v1.24.0",
	}, base.Modules)

	c := checkSDKPin(base, pins(t, "aws_sdk_go_version: v1.48.0\n", goModBase))
	assert.Equal(t, StatusPass, c.Status)
	assert.Equal(t, "SDK_PIN_RAISED", c.Findings[0].Signature)

	assert.Equal(t, StatusPass, checkSDKPin(base, base).Status)
	assert.Empty(t, checkSDKPin(base, base).Findings)

	for name, tc := range map[string]struct {
		meta, gomod, want string
	}{
		"core down":      {"aws_sdk_go_version: v1.40.0\n", goModBase, "SDK_PIN_DECREASED"},
		"module down":    {"aws_sdk_go_version: v1.41.5\n", replaceAll(goModBase, "s3 v1.97.3", "s3 v1.90.0"), "SDK_PIN_DECREASED"},
		"smithy down":    {"aws_sdk_go_version: v1.41.5\n", replaceAll(goModBase, "smithy-go v1.24.0", "smithy-go v1.20.0"), "SDK_PIN_DECREASED"},
		"pin removed":    {"api_version: v1alpha1\n", goModBase, "SDK_PIN_REMOVED"},
		"module removed": {"aws_sdk_go_version: v1.41.5\n", replaceAll(goModBase, "\tgithub.com/aws/aws-sdk-go-v2/service/s3 v1.97.3\n", ""), "SDK_PIN_REMOVED"},
		"not semver":     {"aws_sdk_go_version: latest\n", goModBase, "SDK_PIN_UNPARSEABLE"},
		"regime":         {"aws_service_sdk_version: v1.97.3\n", goModBase, "SDK_PIN_REGIME_CHANGED"},
	} {
		c := checkSDKPin(base, pins(t, tc.meta, tc.gomod))
		assert.Equal(t, StatusFail, c.Status, name)
		assert.Contains(t, c.Reasons, tc.want, name)
	}
}

func replaceAll(s, old, new string) string { return strings.ReplaceAll(s, old, new) }
