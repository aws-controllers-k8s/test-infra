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

// Package sdkbump is ACK Autopilot's sdk-bump workflow (design §3 row 4): one
// deterministic SDK bump of one controller, gated by ack-dod and published as
// a draft PR from the org branch autopilot/<svc>/sdk-bump. No LLM.
package sdkbump

import (
	"fmt"
	"regexp"

	"golang.org/x/mod/semver"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/issuefinder"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

// Workflow is this workflow's name in agent-result.json and the lane.
const Workflow = "sdk-bump"

// Producer is the Producer trailer of every commit and PR this workflow makes.
const Producer = "autopilot/" + Workflow

const maxServiceLen = 40

var (
	// The same rules as prow/agent-workflows/agents/workflows/validation.py.
	serviceRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	workItemRE = regexp.MustCompile(`^sha256:[0-9a-f]{16}$`)
	// The detector's target is always a per-service tag (analyzeService).
	sdkToRE = regexp.MustCompile(`^service/([a-z0-9]+)/(v[0-9]+\.[0-9]+\.[0-9]+)$`)
)

// Inputs are the lane's args: service, sdk-to, work-item, change-class.
type Inputs struct {
	Service, SDKTo, WorkItem, ChangeClass string
}

// Target is the SDK module version the bump moves to.
type Target struct {
	Package, Version string
}

// ValidWorkItem reports whether s is a ledger key hash.
func ValidWorkItem(s string) bool { return workItemRE.MatchString(s) }

// Validate checks every input before it reaches a path, a git command or a
// PR. The work item, when given, must be the key hash of this service and
// target's bump key, so a launcher can never run one item's args under
// another's id.
func (in Inputs) Validate() (Target, []string) {
	var problems []string
	if len(in.Service) > maxServiceLen || !serviceRE.MatchString(in.Service) {
		problems = append(problems, fmt.Sprintf("service must match %s (at most %d)", serviceRE, maxServiceLen))
	}
	m := sdkToRE.FindStringSubmatch(in.SDKTo)
	if m == nil || !semver.IsValid(m[2]) {
		problems = append(problems, "sdk-to must be service/<package>/vX.Y.Z")
	}
	if (in.WorkItem == "") != (in.ChangeClass == "") {
		problems = append(problems, "work-item and change-class must be given together")
	}
	if in.ChangeClass != "" && in.ChangeClass != dod.ClassSDKBump {
		problems = append(problems, "change-class must be "+dod.ClassSDKBump)
	}
	if in.WorkItem != "" {
		switch {
		case !ValidWorkItem(in.WorkItem):
			problems = append(problems, "work-item must be sha256: and 16 hex digits")
		case len(problems) == 0 && in.WorkItem != ledger.KeyHash(issuefinder.BumpKey(in.Service, in.SDKTo)):
			problems = append(problems, "work-item is not the key hash of "+issuefinder.BumpKey(in.Service, in.SDKTo))
		}
	}
	if len(problems) > 0 {
		return Target{}, problems
	}
	return Target{Package: m[1], Version: m[2]}, nil
}
