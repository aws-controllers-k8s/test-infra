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

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/generator"
	"github.com/spf13/cobra"
)

var (
	OptJobsConfigPath    string
	OptJobsTemplatesPath string
	OptJobsOutputPath    string
	OptProwEcrRepository string
	OptCreatePR          string
)

var buildProwCmd = &cobra.Command{
	Use:   "build-prow-images",
	Short: "build-prow-images - builds prow images in image_config.yaml and pushes them to ack-infra public ecr",
	RunE:  buildProwImages,
}

func init() {
	buildProwCmd.PersistentFlags().StringVar(
		&OptJobsConfigPath, "jobs-config-path", "", "path to jobs_config.yaml where jobs configurations are stored",
	)
	buildProwCmd.PersistentFlags().StringVar(
		&OptJobsTemplatesPath, "jobs-templates-path", "", "path to templates directory where the prow job templates are stored",
	)
	buildProwCmd.PersistentFlags().StringVar(
		&OptJobsOutputPath, "jobs-output-path", "", "path to jobs.yaml where the generated jobs will be stored",
	)
	buildProwCmd.PersistentFlags().StringVar(
		&OptProwEcrRepository, "prow-ecr-repository", "prow", "ECR public repository name for prow images",
	)
	buildProwCmd.PersistentFlags().StringVar(
		&OptCreatePR, "create-pr", "true", "option to create PR or not. accepts only true or false",
	)
	rootCmd.AddCommand(buildProwCmd)
}

func buildProwImages(cmd *cobra.Command, args []string) error {

	if OptCreatePR != "true" && OptCreatePR != "false" {
		return fmt.Errorf("--create-pr invalid: only accepts true of false")
	}

	log.SetPrefix("build-prow-images")
	imagesConfig, err := readCurrentImagesConfig(OptImagesConfigPath)
	if err != nil {
		return err
	}
	log.Printf("Successfully read versions in %s\n", OptImagesConfigPath)

	log.Printf("Attempting to list images from %s\n", OptProwEcrRepository)
	imageDetails, err := listEcrProwImageDetails(OptProwEcrRepository)
	if err != nil {
		return err
	}
	log.Printf("Successfully listed Prow Image details from %s\n", OptProwEcrRepository)

	versions := getEcrImageVersionList(imageDetails)
	log.Println("Successfully retrieved version list from image details")

	ecrImageTags := getHighestEcrImageVersionMap(versions)
	log.Println("Successfully cleaned versions")

	tagsToBuild, err := compareImageVersions(imagesConfig.Images, ecrImageTags)
	if err != nil {
		return err
	}
	log.Println("Successfully compared versions")

	if len(tagsToBuild) == 0 {
		log.Println("All prow image versions are up to date. exiting...")
		return nil
	}

	log.Printf("Tags to build:\n %v\n", tagsToBuild)
	if err = buildImagesWithKaniko(imagesConfig.ImageRepo, tagsToBuild); err != nil {
		return err
	}
	log.Println("Successfully built all images")

	// exit if we're not creating a PR
	if OptCreatePR == "false" {
		return nil
	}

	err = generator.Generate("jobs", OptJobsConfigPath, OptImagesConfigPath, OptJobsTemplatesPath, OptJobsOutputPath)
	if err != nil {
		return err
	}
	log.Println("Successfully generated \"jobs.yaml\" with up-to-date prow image tags")

	commitBranch := ""
	prSubject := ""
	prDescription := ""

	if err = commitAndSendPR(OptSourceOwner, OptSourceRepo, commitBranch, pathcJobsSourceFiles, baseBranch, prSubject, prDescription); err != nil {
		return err
	}
	log.Println("Successfully commited and raised a PR with newly generated jobs")
	return err
}
