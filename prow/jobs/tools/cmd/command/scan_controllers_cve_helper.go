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
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/generator"
)

// CHANGE when there's a new service controller
var controllersCount = 43

// This is the struct to unmarshall
// trivy image scan output
type TrivyOutput struct {
	Results []Result `json:"Results"`
}

// Results is stored in TrivyOutput
// It has the results of the scanned components
type Result struct {
	Vulnerabilities []Vulnerability `json:"Vulnerabilities"`

	// Including type here to specify the type of the component
	// being scanned
	Type string `json:"Type"`
}

// With Vulnerability, we're extracting the components
// we need to create a Github Issue
type Vulnerability struct {
	VulnerabilityId  string `json:"VulnerabilityID"`
	InstalledVersion string `json:"InstalledVersion"`
	FixedVersion     string `json:"FixedVersion"`
	Severity         string `json:"Severity"`
	Title            string `json:"Title"`
}

type CVESummary struct {
	InstalledVersion string
	FixedVersion     string
	Severity         string
	Title            string

	// Type will be assigned from the Result
	// that holds this vulnerability
	Type string
}

const (
	scanControllerImageFormat      = "public.ecr.aws/aws-controllers-k8s/%s-controller:%s"
	ecrPublicControllerImageFormat = "v2/aws-controllers-k8s/%s-controller"
)

func getACKServices(configPath string) ([]string, error) {
	fileData, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}

	var config *generator.JobsConfig
	if err = yaml.Unmarshal(fileData, &config); err != nil {
		return nil, err
	}

	return config.AWSServices, nil
}

// listRepositoryTagsWithRetries calls listRepositoryTags, which in turn queries ecrpublic for the list
// of tags from given repository
// If ecrpublic denies the request with HTTP status 429: Too Many Requests, it retries for maxRetries
// or until timeout duration expires
// For any other errors it returns a nil and an error
// A successful listRepositoryTagsWithRetries will return the list of tags per repository and err == nil
func listRepositoryTagsWithRetries(repository string, maxRetries int, timeout time.Duration) ([]string, error) {
	retries := 0
	start := time.Now()

	for {
		if time.Since(start) > timeout {
			return nil, fmt.Errorf("timeout to get repository tags after %d seconds", timeout)
		}
		if retries > maxRetries {
			return nil, fmt.Errorf("timeout to get repository tags after %d retries", retries)
		}
		tags, err := listRepositoryTags(repository)
		if err != nil {
			if strings.Contains(err.Error(), "429") {
				time.Sleep(1 * time.Second)
				retries++
				continue
			} else {
				return nil, err
			}
		}
		return tags, nil
	}

}

func getControllersLatestTags(services []string) (map[string]string, error) {
	controllerTagsMap := make(map[string]string)

	for _, service := range services {
		controllerRepoTags, err := listRepositoryTagsWithRetries(fmt.Sprintf(ecrPublicControllerImageFormat, service), 10, 1*time.Minute)
		if err != nil {
			if strings.Contains(err.Error(), "404") {
				continue
			}
			return nil, err
		}
		latestControllerTag, err := findHighestTagVersion(controllerRepoTags)
		if err != nil {
			return nil, err
		}
		controllerTagsMap[service] = latestControllerTag
	}
	controllersCount = len(controllerTagsMap)

	return controllerTagsMap, nil
}

func getCveSummaries(controller string, results []Result, cveSummaries map[string]CVESummary, detectedVulnerabilities map[string][]string) {
	for _, result := range results {
		for _, vulnerability := range result.Vulnerabilities {

			cveSummaries[vulnerability.VulnerabilityId] = CVESummary{
				InstalledVersion: vulnerability.InstalledVersion,
				FixedVersion:     vulnerability.FixedVersion,
				Severity:         vulnerability.Severity,
				Title:            vulnerability.Title,
				Type:             result.Type,
			}
			controllers, ok := detectedVulnerabilities[vulnerability.VulnerabilityId]
			if ok {
				detectedVulnerabilities[vulnerability.VulnerabilityId] = append(controllers, controller)
			} else {
				detectedVulnerabilities[vulnerability.VulnerabilityId] = []string{controller}
			}
		}
	}
}

func scanControllersForCves(controllerTagsMap map[string]string) (map[string][]string, map[string]CVESummary, error) {

	app := "trivy"

	cveSummaries := make(map[string]CVESummary)
	detectedVulnerabilities := make(map[string][]string)

	for controller, tag := range controllerTagsMap {

		args := []string{
			"image",
			"--format",
			"json",
			"-q",
			fmt.Sprintf(scanControllerImageFormat, controller, tag),
		}
		cmd := exec.Command(app, args...)
		stdout, err := cmd.CombinedOutput()
		//TODO: return error for now, revist later
		if err != nil {
			return nil, nil, fmt.Errorf("%s\n%s", stdout, err)
		}

		var trivyOutput *TrivyOutput
		if err = json.Unmarshal(stdout, &trivyOutput); err != nil {
			return nil, nil, err
		}
		getCveSummaries(controller, trivyOutput.Results, cveSummaries, detectedVulnerabilities)
	}
	return detectedVulnerabilities, cveSummaries, nil
}

func prepareGithubIssueBody(controllersByVulnerabilities map[string][]string, cveSummaries map[string]CVESummary) (string, error) {

	title := "| CVE ID | Type | Severity | Installed Version | Fixed Version | Affected Controllers | Title |"
	tableFormat := "|---|---|---|---|---|---|---|"
	const format = "|%s|%s|%s|%s|%s|%s|%s|\n"

	var builder strings.Builder

	for vulnerabilityId, cveSummary := range cveSummaries {
		controllers := fmt.Sprintf("%s", controllersByVulnerabilities[vulnerabilityId])
		if len(controllersByVulnerabilities[vulnerabilityId]) == controllersCount {
			controllers = "ALL"
		}
		_, err := builder.WriteString(fmt.Sprintf(format, vulnerabilityId, cveSummary.Type, cveSummary.Severity, cveSummary.InstalledVersion, cveSummary.FixedVersion, controllers, cveSummary.Title))
		if err != nil {
			return "", err
		}
	}

	return fmt.Sprintf("%s\n%s\n%s", title, tableFormat, builder.String()), nil
}
