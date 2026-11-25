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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/issuefinder"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

const (
	svc = "s3"
	to  = "service/s3/v1.97.3"
)

var wi = ledger.KeyHash(issuefinder.BumpKey(svc, to))

func TestValidateAcceptsALedgerBump(t *testing.T) {
	tg, problems := Inputs{Service: svc, SDKTo: to, WorkItem: wi, ChangeClass: "SDK-bump"}.Validate()
	assert.Empty(t, problems)
	assert.Equal(t, Target{Package: "s3", Version: "v1.97.3"}, tg)

	_, problems = Inputs{Service: svc, SDKTo: to}.Validate()
	assert.Empty(t, problems, "a manual run without a work item")
}

func TestValidateRejects(t *testing.T) {
	for name, in := range map[string]Inputs{
		"service case":      {Service: "S3", SDKTo: to},
		"service traversal": {Service: "s3/../main", SDKTo: to},
		"service too long":  {Service: "a123456789012345678901234567890123456789x", SDKTo: to},
		"core pin target":   {Service: svc, SDKTo: "v1.41.5"},
		"no v":              {Service: svc, SDKTo: "service/s3/1.97.3"},
		"prerelease":        {Service: svc, SDKTo: "service/s3/v1.97.3-rc1"},
		"work item alone":   {Service: svc, SDKTo: to, WorkItem: wi},
		"class alone":       {Service: svc, SDKTo: to, ChangeClass: "SDK-bump"},
		"other class":       {Service: svc, SDKTo: to, WorkItem: wi, ChangeClass: "config-only"},
		"bad hash":          {Service: svc, SDKTo: to, WorkItem: "sha256:0123", ChangeClass: "SDK-bump"},
		"another item":      {Service: svc, SDKTo: "service/s3/v1.97.4", WorkItem: wi, ChangeClass: "SDK-bump"},
	} {
		_, problems := in.Validate()
		assert.NotEmpty(t, problems, name)
	}
}
