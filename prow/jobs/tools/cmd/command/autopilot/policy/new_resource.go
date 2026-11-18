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

package policy

// NewResourceFile is optional: without it no add-resource run starts (Plan 9,
// owner decision 2026-11-17 option A). It is a separate file, like
// policy/triage.yaml, so a ledger without it still loads.
const NewResourceFile = "policy/new-resource.yaml"

// Owner ceilings for the new-resource lane (design 8): the file may lower
// them, never raise them.
const (
	maxNewResourceLaunchesPerDay = 1
	maxNewResourceOpenPRsPerRepo = 1
)

// NewResource switches and caps the new-resource lane (add-resource). Once
// the file exists every key is required.
type NewResource struct {
	// Enabled is the lane's own switch, off by default.
	Enabled bool `yaml:"enabled"`
	// MaxLaunchesPerDay is org-wide, over the last 24 hours.
	MaxLaunchesPerDay int `yaml:"max_launches_per_day"`
	// MaxOpenPRsPerRepo counts new-resource items with a run in flight or
	// an open PR, per controller repo.
	MaxOpenPRsPerRepo int `yaml:"max_open_prs_per_repo"`
}

// NewResourceEnabled reports whether the policy lets add-resource start.
func (p Policy) NewResourceEnabled() bool { return p.NewResource != nil && p.NewResource.Enabled }

func (n NewResource) validate() []error {
	var errs []error
	if n.MaxLaunchesPerDay < 0 || n.MaxLaunchesPerDay > maxNewResourceLaunchesPerDay {
		errs = append(errs, errorf(NewResourceFile, "max_launches_per_day must be 0..%d", maxNewResourceLaunchesPerDay))
	}
	if n.MaxOpenPRsPerRepo < 0 || n.MaxOpenPRsPerRepo > maxNewResourceOpenPRsPerRepo {
		errs = append(errs, errorf(NewResourceFile, "max_open_prs_per_repo must be 0..%d", maxNewResourceOpenPRsPerRepo))
	}
	return errs
}
