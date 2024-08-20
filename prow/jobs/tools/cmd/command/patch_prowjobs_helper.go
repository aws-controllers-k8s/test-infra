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
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecrpublic"
	"github.com/aws/aws-sdk-go-v2/service/ecrpublic/types"
)

const (
	ECR_TAGS_MAX_CAPACITY = 200
	patchJobsSourceFiles  = "./prow/jobs/jobs.yaml:test-infra/prow/jobs/jobs.yaml"
	patchJobCommitBranchPrefix  = "ack-bot/built-and-pushed-images-%d"
	patchJobPRSubject     = "Patch Prow Jobs Image Version"
	patchJobPRDescriptionPrefix = "Regenerated jobs.yaml with new prow job versions for %v\n"
)

func listEcrProwImageDetails(repositoryName string) ([]types.ImageDetail, error) {
	ctx := context.Background()

	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("us-east-1"))
	if err != nil {
		return nil, fmt.Errorf("unable to load config, %v", err)
	}

	svc := ecrpublic.NewFromConfig(cfg)
	describeImagesInput := &ecrpublic.DescribeImagesInput{
		RepositoryName: aws.String(repositoryName),
		MaxResults:     aws.Int32(ECR_TAGS_MAX_CAPACITY),
	}

	describeImagesOutput := &ecrpublic.DescribeImagesOutput{
		NextToken: aws.String(""),
	}

	imageDetails := make([]types.ImageDetail, 0, ECR_TAGS_MAX_CAPACITY)
	for describeImagesOutput.NextToken != nil {
		describeImagesOutput, err = svc.DescribeImages(ctx, describeImagesInput)
		if err != nil {
			return nil, fmt.Errorf("failure encountered while describing images for %v: %s", *describeImagesInput, err)
		}
		imageDetails = append(imageDetails, describeImagesOutput.ImageDetails...)
	}

	return imageDetails, nil
}

func getEcrImageVersionList(imageDetails []types.ImageDetail) []string {

	pattern := `-[0-9]`
	regex := regexp.MustCompile(pattern)
	versions := make([]string, 0, ECR_TAGS_MAX_CAPACITY)

	for _, imageDetail := range imageDetails {
		for _, tag := range imageDetail.ImageTags {
			if regex.MatchString(tag) {
				versions = append(versions, tag)
				break
			}
		}
	}
	return versions
}

func getHighestEcrImageVersionMap(versions []string) map[string]string {

	imageTagsMap := make(map[string]string)

	for _, version := range versions {
		// skip tags with no prefix
		temp := strings.Split(version, "-")
		if len(temp) < 2 {
			continue
		}

		tagInList := temp[len(temp)-1]
		imageTagKey := strings.Join(temp[:len(temp)-1], "-")
		currentTag, ok := imageTagsMap[imageTagKey]

		// put tagInList in imageTagsMap it's not there, or if tagInList is greater than currentTag in map
		if replace, err := isGreaterVersion(tagInList, currentTag); !ok || err == nil && replace {
			imageTagsMap[imageTagKey] = tagInList
		}
	}

	return imageTagsMap
}

func compareImageVersions(configTagsMap, ecrTagsMap map[string]string) (map[string]string, error) {

	tagsToBuild := make(map[string]string)

	for configTagKey, configTag := range configTagsMap {
		// sample configTag: prow-controller-release-temp-0.0.3
		temp := strings.Split(configTag, "-")

		// joins the tag so it can be retrieved for ecrTagMap
		// key would be prow-controller-release-tag
		key := strings.Join(temp[:len(temp)-1], "-")
		// tag would be 0.0.3
		latestTag := temp[len(temp)-1]
		ecrTag, ok := ecrTagsMap[key]
		// if tag is not in ECR, add to tags to build
		if !ok {
			tagsToBuild[configTagKey] = configTag
			continue
		}

		needToUpdate, err := isGreaterVersion(latestTag, ecrTag)
		if err != nil {
			return nil, fmt.Errorf("unable to compare %s and %s: %v", latestTag, ecrTag, err)
		}

		if needToUpdate {
			tagsToBuild[configTagKey] = strings.Join(temp, "-")
		}
	}

	return tagsToBuild, nil
}

func buildImagesWithKaniko(imageRepository string, tagsToBuild map[string]string) error {
	// BuildImage("my-app", "my-app-0.0.9")
	app := "/kaniko/executor"
	imagesDir := "./prow/jobs/images"

	sortedTagKeys := make([]string, 0, len(tagsToBuild))
	for key := range tagsToBuild {
		sortedTagKeys = append(sortedTagKeys, key)
	}
	sort.Strings(sortedTagKeys)

	for _, postfix := range sortedTagKeys {
		context := "dir://./prow/jobs/images"
		if postfix == "build-prow-images" || postfix == "upgrade-go-version" {
			context = "dir://."
		}
		args := []string{
			"--dockerfile",
			fmt.Sprintf("%s/Dockerfile.%s", imagesDir, postfix),
			"--destination",
			fmt.Sprintf("%s:%s", imageRepository, tagsToBuild[postfix]),
			"--context",
			context,
		}
		cmd := exec.Command(app, args...)
		stdout, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("unable to build with kaniko for context %s. stdout: %s\n: %v", context, stdout, err)
		}
	}
	return nil
}
