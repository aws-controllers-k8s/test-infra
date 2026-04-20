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

package eksdistro

import (
	"regexp"
	"strings"
)

const (
	UpdatePRSubject      = "Update to eks-distro %s"
	UpdatePRCommitBranch = "eks-distro-update-%s"
	UpdatePRDescription  = "Update eks-distro from %s to %s "

	// `Comma-separated list of files to commit and their location.
	// 	The local file is separated by its target location by a semi-colon.
	// 	If the file should be in the same location with the same name, you can just put the file name and omit the repetition.
	// 	Example: README.md,main.go:prow/jobs/tools/cmd/main.go`
	UpdateSourceFiles = "build_config.yaml"
)

// When comparing EKS-distro versions we are assuming their values
// mean the following
// YYYY-MM-DD-TTMMSS000.2
// This format of date can be compared using string comparison
// In the future if the meaning or the standard of the version
// changes, we need to change this function to reflect
// the comparison of eks-distro version
func VersionIsGreaterThan(v1 string, v2 string) bool {
	return v1 > v2
}

func FindHighestEcrVersion(tags []string) (string, error) {

	regex := regexp.MustCompile(`[a-z]`)
	max := "2000-08-13-1723575672.2"

	for _, tag := range tags {
		temp := strings.Split(tag, ".")
		if regex.MatchString(tag) || len(temp) != 2 || temp[1] != "2" {
			continue
		}
		if VersionIsGreaterThan(tag, max) {
			max = tag
		}
	}

	return max, nil
}
