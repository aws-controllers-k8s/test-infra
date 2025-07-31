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
)

var (
	OptPluginImagesDir string
)

var buildProwPluginsCmd = &cobra.Command{
	Use:   "build-prow-plugin-images",
	Short: "build-prow-plugin-images - builds prow plugin images in image_config.yaml and pushes them to ack-infra public ecr",
	RunE:  buildProwPluginImages,
}

func init() {
	buildProwPluginsCmd.PersistentFlags().StringVar(
		&OptBuildConfigPath, "build-config-path", "build_config.yaml", "path to build_config.yaml, where all the build versions are stored",
	)
	buildProwPluginsCmd.PersistentFlags().StringVar(
		&OptPluginImagesDir, "images-dir", "./prow/plugins/images", "Path to directory where plugin Dockerfiles are stored.",
	)
	rootCmd.AddCommand(buildProwPluginsCmd)
}

func buildProwPluginImages(cmd *cobra.Command, args []string) error {
	log.SetPrefix("build-prow-plugin-images")
	shouldPushImages, err := validateBooleanFlag(OptPushImages, "--push-images")
	if err != nil {
		return err
	}

	builtTags, err := buildAndPushImages(
		OptImagesConfigPath,
		OptPluginImagesDir,
		OptProwEcrRepository,
		OptBuildConfigPath,
		shouldPushImages,
	)
	if err != nil {
		return err
	}

	writeBuiltTags(builtTags)

	// TODO Generate plugin deployment manifests from template files

	return nil
}
