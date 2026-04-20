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

package cve

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetCveSummaries(t *testing.T) {

	assert := assert.New(t)

	type args struct {
		controller              string
		results                 []Result
		cveSummaries            map[string]CVESummary
		detectedVulnerabilities map[string][]string
	}

	tests := []struct {
		name                              string
		args                              args
		wantCveSummariesLength            int
		wantDetectedVulnerabilitiesLength int
	}{
		{
			name: "single CVE",
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
			wantCveSummariesLength:            1,
			wantDetectedVulnerabilitiesLength: 1,
		},
		{
			name: "aggregate CVEs",
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
				cveSummaries: map[string]CVESummary{
					"CVE-2024-9805": {"1.22.2", "1.23.0", "LOW", "Some title", "gobinary"},
				},
				detectedVulnerabilities: map[string][]string{
					"CVE-2024-9805": {"lambda", "s3", "sns", "sqs", "rds"},
				},
			},
			wantCveSummariesLength:            1,
			wantDetectedVulnerabilitiesLength: 1,
		},
		{
			name: "unique CVEs",
			args: args{
				controller: "ec2",
				results: []Result{{
					[]Vulnerability{{
						VulnerabilityId:  "CVE-2024-1000",
						InstalledVersion: "1.22.2",
						FixedVersion:     "1.23.0",
						Severity:         "LOW",
						Title:            "This is a title",
					}},
					"gobinary",
				},
				},
				cveSummaries: map[string]CVESummary{
					"CVE-2024-9805": {"1.22.2", "1.23.0", "LOW", "Some title", "gobinary"},
				},
				detectedVulnerabilities: map[string][]string{
					"CVE-2024-9805": {"lambda"},
				},
			},
			wantCveSummariesLength:            2,
			wantDetectedVulnerabilitiesLength: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			GetCveSummaries(tt.args.controller, tt.args.results, tt.args.cveSummaries, tt.args.detectedVulnerabilities)
			assert.Len(tt.args.cveSummaries, tt.wantCveSummariesLength)
			assert.Len(tt.args.detectedVulnerabilities, tt.wantDetectedVulnerabilitiesLength)
		})
	}
}
