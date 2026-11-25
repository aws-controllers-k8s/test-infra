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

package sdkbump

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sha = "0123456789abcdef0123456789abcdef01234567"

func TestPushRefspecIsOnlyTheBumpBranch(t *testing.T) {
	spec, err := pushRefspec("s3", sha)
	require.NoError(t, err)
	assert.Equal(t, sha+":refs/heads/autopilot/s3/sdk-bump", spec)
	assert.Equal(t, "autopilot/s3/sdk-bump", Branch("s3"))
}

func TestPushRefspecRefuses(t *testing.T) {
	for name, c := range map[string][2]string{
		"bad service":    {"s3/../../heads/main", sha},
		"upper service":  {"S3", sha},
		"empty service":  {"", sha},
		"short sha":      {"s3", "0123456"},
		"ref as sha":     {"s3", "main"},
		"refspec in sha": {"s3", sha + ":refs/heads/main"},
	} {
		_, err := pushRefspec(c[0], c[1])
		assert.Error(t, err, name)
	}
}

func TestCheckPushRefRefusesEverythingElse(t *testing.T) {
	for _, ref := range []string{
		"main", "refs/heads/main", "refs/heads/master", "HEAD", "refs/tags/v1.0.0",
		"autopilot/s3/sdk-bump", "refs/heads/autopilot/ec2/sdk-bump",
		"refs/heads/autopilot/s3/sdk-bump/x", "+refs/heads/autopilot/s3/sdk-bump",
	} {
		err := checkPushRef("s3", ref)
		assert.True(t, errors.Is(err, ErrRefNotAllowed), ref)
	}
	assert.NoError(t, checkPushRef("s3", "refs/heads/autopilot/s3/sdk-bump"))
}

func TestRemotes(t *testing.T) {
	assert.Equal(t, "https://x-access-token@github.com/aws-controllers-k8s/s3-controller.git",
		canonicalRemote("aws-controllers-k8s", "s3"))
	assert.True(t, isLocalRemote("file:///tmp/x.git"))
	assert.False(t, isLocalRemote("https://github.com/aws-controllers-k8s/s3-controller.git"))
	assert.False(t, strings.Contains(BranchRef("s3"), "main"))
}
