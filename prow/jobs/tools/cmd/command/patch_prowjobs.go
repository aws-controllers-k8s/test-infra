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
	"log"

	"github.com/spf13/cobra"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/generator"
)

var (
	OptJobsConfigPath    string
	OptJobsTemplatesPath string
	OptJobsOutputPath    string
	OptJobsImagesDir     string
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
		&OptJobsImagesDir, "images-dir", "./prow/jobs/images", "Path to directory where job Dockerfiles are stored.",
	)
	rootCmd.AddCommand(buildProwCmd)
}

func buildProwImages(cmd *cobra.Command, args []string) error {
	log.SetPrefix("build-prow-images")

	shouldPushImages, err := validateBooleanFlag(OptPushImages, "--push-images")
	if err != nil {
		return err
	}

	builtTags, err := buildAndPushImages(
		OptImagesConfigPath,
		OptJobsImagesDir,
		OptProwEcrRepository,
		OptBuildConfigPath,
		shouldPushImages,
	)
	if err != nil {
		return err
	}

	err = generator.Generate("jobs", OptJobsConfigPath, OptImagesConfigPath, OptJobsTemplatesPath, OptJobsOutputPath)
	if err != nil {
		return err
	}
	log.Println("Successfully generated \"jobs.yaml\" with up-to-date prow image tags")

	writeBuiltTags(builtTags)
	return err
}
