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

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/generator"
	"github.com/spf13/cobra"
)

var (
	OptAgentWorkflowsConfigPath    string
	OptAgentWorkflowsTemplatesPath string
	OptAgentWorkflowsOutputPath    string
	OptAgentWorkflowImagesDir      string
)

var buildProwAgentWorkflowsCmd = &cobra.Command{
	Use:   "build-prow-agent-workflow-images",
	Short: "build-prow-agent-workflow-images - builds prow agent workflow images in image_config.yaml and pushes them to ack-infra public ecr",
	RunE:  buildProwAgentWorkflowImages,
}

func init() {
	buildProwAgentWorkflowsCmd.PersistentFlags().StringVar(
		&OptBuildConfigPath, "build-config-path", "build_config.yaml", "path to build_config.yaml, where all the build versions are stored",
	)
	buildProwAgentWorkflowsCmd.PersistentFlags().StringVar(
		&OptAgentWorkflowImagesDir, "images-dir", "./prow/agent-workflows/images", "Path to directory where agent-workflow Dockerfiles are stored.",
	)
	buildProwAgentWorkflowsCmd.PersistentFlags().StringVar(
		&OptAgentWorkflowsTemplatesPath, "agent-workflows-templates-path", "", "Path to directory where agent-workflow templates are stored.",
	)
	buildProwAgentWorkflowsCmd.PersistentFlags().StringVar(
		&OptAgentWorkflowsOutputPath, "agent-workflows-output-path", "", "Path to directory where agent-workflow.yaml will be stored.",
	)
	rootCmd.AddCommand(buildProwAgentWorkflowsCmd)
}

func buildProwAgentWorkflowImages(cmd *cobra.Command, args []string) error {
	log.SetPrefix("build-prow-agent-workflow-images")
	shouldPushImages, err := validateBooleanFlag(OptPushImages, "--push-images")
	if err != nil {
		return err
	}

	builtTags, err := buildAndPushImages(OptImagesConfigPath,
		OptAgentWorkflowImagesDir,
		OptProwEcrRepository,
		OptBuildConfigPath,
		shouldPushImages,
	)
	if err != nil {
		return err
	}

	writeBuiltTags(builtTags)

	err = generator.GenerateAgentWorkflows(
		OptImagesConfigPath,
		OptAgentWorkflowsTemplatesPath,
		OptAgentWorkflowsOutputPath,
	)
	return err
}
