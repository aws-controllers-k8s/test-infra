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

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/pkg/config"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/pkg/ecrpublic"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/pkg/ghutil"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/pkg/goversion"
)

// TODO: need to add more flags to handle making a pull request
var (
	OptBuildConfigPath string
	OptGoEcrRepository string
)

var upgradeGoVersionCMD = &cobra.Command{
	Use:   "upgrade-go-version",
	Short: "upgrade-go-version - queries for latest image version and patches prow image versions",
	RunE:  runUpgradeGoVersion,
}

func init() {
	upgradeGoVersionCMD.PersistentFlags().StringVar(
		&OptBuildConfigPath, "build-config-path", "build_config.yaml", "path to build_config.yaml, where all the build versions are stored",
	)
	upgradeGoVersionCMD.PersistentFlags().StringVar(
		&OptGoEcrRepository, "golang-ecr-repository", "docker/library/golang", "ecr gallery repository for golang",
	)

	rootCmd.AddCommand(upgradeGoVersionCMD)
}

// runUpgradeGoVersion command compares the GO version in ECR public to the one in build_config
// If the version in ECR is greater, it increases the patch version of prow images in
// images_config.yaml, updates build_config.yaml with latest version
// and pushes a PR with the updates.
func runUpgradeGoVersion(cmd *cobra.Command, args []string) error {
	log.SetPrefix("upgrade-go-version: ")

	ecrGoVersions, err := ecrpublic.ListTags(OptGoEcrRepository)
	if err != nil {
		return fmt.Errorf("unable to get go versions from ecr public: %v", err)
	}

	log.Printf("Successfully listed go versions from %s\n", OptGoEcrRepository)

	highestEcrGoVersion, err := config.FindHighestTagVersion(ecrGoVersions)
	if err != nil {
		return fmt.Errorf("%v", err)
	}
	log.Printf("Current highest Go version in %s is %s\n", OptGoEcrRepository, highestEcrGoVersion)

	goBuildVersion, err := config.ReadBuildConfigFile(OptBuildConfigPath)
	if err != nil {
		return err
	}
	log.Printf("Successfully extracted build versions from %s\n", OptBuildConfigPath)
	log.Printf("Current version in build config is %s\n", goBuildVersion.GoVersion)

	needUpgrade, err := config.IsGreaterVersion(highestEcrGoVersion, goBuildVersion.GoVersion)
	if err != nil {
		return err
	}

	if !needUpgrade {
		log.Printf("Go version in %s is up-to-date\n", OptBuildConfigPath)
		return nil
	}

	log.Printf("Changing Go build version to %s in %s\n", highestEcrGoVersion, OptBuildConfigPath)
	goBuildVersion.GoVersion = highestEcrGoVersion
	if err = config.PatchBuildVersionFile(goBuildVersion, OptBuildConfigPath); err != nil {
		return err
	}
	log.Println("Successfully updated Go version!")

	log.Printf("Patching prow image versions in %s\n", OptImagesConfigPath)
	imagesConfig, err := config.ReadCurrentImagesConfig(OptImagesConfigPath)
	if err != nil {
		return err
	}
	if err = goversion.IncreasePatchImageConfig(imagesConfig); err != nil {
		return err
	}
	if err = config.PatchImageConfigVersionFile(imagesConfig, OptImagesConfigPath); err != nil {
		return err
	}
	log.Println("Successfully patched prow image versions!")

	commitBranch := fmt.Sprintf(goversion.UpdatePRCommitBranch, highestEcrGoVersion)
	prSubject := fmt.Sprintf(goversion.UpdatePRSubject, highestEcrGoVersion)
	prDescription := fmt.Sprintf(goversion.UpdatePRDescription, goBuildVersion.GoVersion, highestEcrGoVersion)

	log.Println("Committing and creating PR with changes")
	if err = ghutil.CommitAndPushPR(OptSourceOwner, OptSourceRepo, commitBranch, goversion.UpdateSourceFiles, ghutil.BaseBranch, prSubject, prDescription); err != nil {
		return err
	}
	log.Println("Successfully created PR")

	return nil
}
