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

package command

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFindHighestTagVersion(t *testing.T) {
	assert := assert.New(t)

	type args struct {
		tags []string
	}

	tests := []struct {
		name        string
		args        args
		wantVersion string
		wantErr     bool
	}{
		{
			name:        "empty tags",
			args:        args{[]string{}},
			wantVersion: "",
			wantErr:     true,
		},
		{
			name:        "one tag",
			args:        args{[]string{"1.23.0"}},
			wantVersion: "1.23.0",
			wantErr:     false,
		},
		{
			name:        "malformed tag",
			args:        args{[]string{"1.23.0", "1./3.3"}},
			wantVersion: "",
			wantErr:     true,
		},
		{
			name:        "correct versions",
			args:        args{[]string{"1.0.0", "1.1.1", "2.2.2", "0.10.1", "12.34.91", "12.34.92"}},
			wantVersion: "12.34.92",
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			version, err := findHighestTagVersion(tt.args.tags)
			if (err != nil) != tt.wantErr {
				assert.Fail(fmt.Sprintf("findHighestTags() error = %v, wantErr %v", err, tt.wantErr))
			}
			assert.Equal(tt.wantVersion, version)
		})
	}
}

func TestGetCveSummaries(t *testing.T) {

	assert := assert.New(t)

	type args struct {
		controller              string
		results             []Result
		cveSummaries            map[string]CVESummary
		detectedVulnerabilities map[string][]string
	}

	tests := []struct {
		name                        string
		args                        args
		wantCveSummariesLength            int
		wantDetectedVulnerabilitiesLength int
	}{
		{
			name: "correct input",
			args: args{
				controller: "ec2",
				results: []Result{{
						[]Vulnerability{{
							VulnerabilityId:  "CVE-2024-9805",
							InstalledVersion: "1.22.2",
							FixedVersion:     "1.23.0",
							Severity:         "LOW",
							Title:            "This is a title",
						}},
						"gobinary",
					},
				},
				cveSummaries:            map[string]CVESummary{},
				detectedVulnerabilities: map[string][]string{},
			},
			wantCveSummariesLength: 1,
			wantDetectedVulnerabilitiesLength: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getCveSummaries(tt.args.controller, tt.args.results, tt.args.cveSummaries, tt.args.detectedVulnerabilities)
			assert.Len(tt.args.cveSummaries, tt.wantCveSummariesLength)
			assert.Len(tt.args.detectedVulnerabilities, tt.wantDetectedVulnerabilitiesLength)
		})
	}
}
