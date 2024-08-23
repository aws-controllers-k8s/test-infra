package command

import (
	"regexp"
	"strings"
)

const (
	updateEksDistroPRSubject      = "Update to eks-distro %s"
	updateEksDistroPRCommitBranch = "eks-distro-update-%s"
	updateEksDistroPRDescription  = "Update eks-distro from %s to %s "

	// `Comma-separated list of files to commit and their location.
	// 	The local file is separated by its target location by a semi-colon.
	// 	If the file should be in the same location with the same name, you can just put the file name and omit the repetition.
	// 	Example: README.md,main.go:prow/jobs/tools/cmd/main.go`
	updateEksDistroSourceFiles = "build_config.yaml,./prow/jobs/images_config.yaml:prow/jobs/images_config.yaml"
)

// When comparing EKS-distro versions we are assuming their values
// mean the following
// YYYY-MM-DD-TTMMSS000.2
// This format of date can be compared using string comparison
// In the future if the meaning or the standard of the version
// changes, we need to change this function to reflect 
// the comparison of eks-distro version
func eksDistroVersionIsGreaterThan(v1 string, v2 string) bool {
	return v1 > v2
}

func findHighestEcrEksDistroVersion(tags []string) (string, error) {

	regex := regexp.MustCompile(`[a-z]`)
	max := "2000-08-13-1723575672.2"

	for _, tag := range tags {
		temp := strings.Split(tag, ".")
		if regex.MatchString(tag) || len(temp) != 2 || temp[1] != "2" {
			continue
		}
		if eksDistroVersionIsGreaterThan(tag, max) {
			max = tag
		}
	}

	return max, nil
}
