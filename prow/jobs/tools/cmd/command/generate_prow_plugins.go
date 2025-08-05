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
	"strings"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/generator"
	"github.com/spf13/cobra"
)

var (
	OptPluginsTemplatePath string
	OptPluginsOutputPath   string
	OptBuiltPluginImages   string
)

var generateProwPluginsCmd = &cobra.Command{
	Use:   "generate-prow-plugins",
	Short: "generate-prow-plugins - generates prow plugin deployment manifests from template and config files.",
	RunE:  generateProwPlugins,
}

func init() {
	generateProwPluginsCmd.PersistentFlags().StringVar(
		&OptPluginsTemplatePath, "plugins-templates-path", "", "Path to directory where plugin templates are stored.",
	)
	generateProwPluginsCmd.PersistentFlags().StringVar(
		&OptPluginsOutputPath, "plugins-output-path", "", "Path to directory where plugin deployment manifests will be stored.",
	)
	rootCmd.AddCommand(generateProwPluginsCmd)
}

func generateProwPlugins(cmd *cobra.Command, args []string) error {
	log.SetPrefix("generate-prow-plugins")

	log.Printf("Generating Prow Plugin deployment manifests...")
	generatedFiles, err := generator.GeneratePlugins(OptImagesConfigPath, OptPluginsTemplatePath, OptPluginsOutputPath)
	if err != nil {
		return err
	}
	writeGeneratedFiles(generatedFiles)

	return err
}

func writeGeneratedFiles(generatedFiles []string) {
	fmt.Printf("%s", strings.Join(generatedFiles, ","))
}
