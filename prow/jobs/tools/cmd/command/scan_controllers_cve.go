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
	"log"

	"github.com/spf13/cobra"
)

var scanCveCMD = &cobra.Command{
	Use:   "scan-controllers-cve",
	Short: "scan-controllers-cve - retrieves all ack controllers and scans them for CVE",
	RunE:  scanControllersCve,
}

var (
	OptGithubIssueOwner string
	OptGithubIssueRepo  string
)

func init() {
	scanCveCMD.PersistentFlags().StringVar(
		&OptJobsConfigPath, "jobs-config-path", "jobs_config.yaml", "path to jobs_config.yaml where jobs configurations are stored",
	)
	scanCveCMD.PersistentFlags().StringVar(
		&OptGithubIssueOwner, "github-issues-owner", "aws-controllers-k8s", "user/org that owns the github repository where CVE issue will be created",
	)
	scanCveCMD.PersistentFlags().StringVar(
		&OptGithubIssueRepo, "github-issues-repo", "community", "repository where the github issue will be created",
	)
	rootCmd.AddCommand(scanCveCMD)
}

func scanControllersCve(cmd *cobra.Command, args []string) error {

	log.Printf("Retreiving AWS services list from %s\n", OptJobsConfigPath)
	services, err := getACKServices(OptJobsConfigPath)
	if err != nil {
		return err
	}

	log.Println("Retreiving service controller to latest tag map")
	controllerTagsMap, err := getControllersLatestTags(services)
	if err != nil {
		return err
	}

	log.Println("Scanning All controllers for CVEs")
	detectedVulnerabilities, cveSummaries, err := scanControllersForCves(controllerTagsMap)
	if err != nil {
		return err
	}

	log.Printf("We found %d unique CVEs across all the controllers\n", len(detectedVulnerabilities))
	if len(detectedVulnerabilities) == 0 {
		log.Println("exiting...")
		return nil
	}

	log.Println("Preparing github Issue listing all CVEs")
	githubIssueBody, err := prepareGithubIssueBody(detectedVulnerabilities, cveSummaries)
	if err != nil {
		return fmt.Errorf("error while preparing github issue body: %s", err)
	}

	log.Printf("Creating github issue to %s/%s repo\n", OptGithubIssueOwner, OptGithubIssueRepo)
	title := "ACK Detected Controllers CVEs"
	labels := []string{
		"kind/cve",
		defaultProwAutoGenLabel,
	}
	if err = createGithubIssue(OptGithubIssueOwner, OptGithubIssueRepo, title, githubIssueBody, labels); err != nil {
		return err
	}

	return nil
}
