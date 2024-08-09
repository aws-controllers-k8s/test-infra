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
	"os"

	"github.com/spf13/cobra"
)

const (
	appName      = "ack-build-tools"
	appShortDesc = "ack-build-tools - patch prow images, build, and release"
	appLongDesc  = "ack-build-tools - A tool to patch prow jobs when there is a change to test infra, or when there's a new go version pushed to ECR"
)

var (
	OptImagesConfigPath        string
	OptSourceOwner             string
	OptSourceRepo              string
	OptCommitMessage           string
	OptCommitBranch            string
)

var rootCmd = &cobra.Command{
	Use:          appName,
	Short:        appShortDesc,
	Long:         appLongDesc,
	SilenceUsage: true,
}

func init() {
	rootCmd.PersistentFlags().StringVar(
		&OptImagesConfigPath, "images-config-path", "images_config.yaml", "path to images_config.yaml, where prow job image versions are stored",
	)
	rootCmd.PersistentFlags().StringVar(
		&OptSourceOwner, "source-owner", "aws-controllers-k8s", "Name of the owner of the repo to create the commit in.",
	)
	rootCmd.PersistentFlags().StringVar(
		&OptSourceRepo, "source-repo", "test-infra", "Name of repo to create the commit in.",
	)
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
