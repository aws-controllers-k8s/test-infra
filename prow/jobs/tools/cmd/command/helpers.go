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
	"os"
	"strings"

	"github.com/aquasecurity/go-version/pkg/semver"
	"github.com/google/go-github/v63/github"

	"gopkg.in/yaml.v3"
)

// ImagesConfig specifies the structure of images_config.yaml
// where the prow image versions are specified
type ImagesConfig struct {
	ImageRepo string            `yaml:"image_repo"`
	Images    map[string]string `yaml:"images"`
}

// BuildConfig specifies the structure of build_config.yaml
// where the build versions are stored
type BuildConfig struct {
	// so far we only have the go-version
	GoVersion        string `yaml:"go_version"`
	EksDistroVersion string `yaml:"eks_distro_version"`
}

func readBuildConfigFile(filepath string) (*BuildConfig, error) {
	fileData, err := os.ReadFile(filepath)
	if err != nil {
		return nil, fmt.Errorf("unable to read file %s: %s", OptBuildConfigPath, err)
	}

	var configGoVersion *BuildConfig
	if err := yaml.Unmarshal(fileData, &configGoVersion); err != nil {
		return nil, fmt.Errorf("unable to unmarshal yaml file: %s. %s", fileData, err)
	}

	return configGoVersion, nil
}

func readCurrentImagesConfig(filepath string) (*ImagesConfig, error) {
	imagesConfigData, err := os.ReadFile(filepath)
	if err != nil {
		return nil, fmt.Errorf("unable to read file %s: %s", filepath, err)
	}

	var imagesConfig *ImagesConfig
	if err = yaml.Unmarshal(imagesConfigData, &imagesConfig); err != nil {
		return nil, fmt.Errorf("unable to unmarshal imagesConfigData, %s, %s", imagesConfigData, err)
	}

	return imagesConfig, nil
}

func patchImageConfigVersionFile(imagesConfig *ImagesConfig, filepath string) error {
	file, err := os.Create(filepath)
	if err != nil {
		return fmt.Errorf("unable to create file %s: %s", filepath, err)
	}
	defer file.Close()

	if err = yaml.NewEncoder(file).Encode(&imagesConfig); err != nil {
		return fmt.Errorf("unable to encode %v to %s: %v", imagesConfig, filepath, err)
	}
	return nil
}

// isGreaterVersion returns true if v1 is greater than v2, else returns false
func isGreaterVersion(v1, v2 string) (bool, error) {

	semver1, err := semver.Parse(v1)
	if err != nil {
		return false, fmt.Errorf("error encountered while parsing %s: %s", v1, err)
	}
	semver2, err := semver.Parse(v2)
	if err != nil {
		return false, fmt.Errorf("error encountered while parsing %s: %s", v2, err)
	}
	return semver1.GreaterThan(semver2), nil
}

func commitAndSendPR(sourceOwner, sourceRepo, commitBranch, sourceFiles, baseBranch, prSubject, prDescription string) error {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return fmt.Errorf("enviroment variable GITHUB_TOKEN is not provided")
	}

	if sourceOwner == "" || sourceRepo == "" || commitBranch == "" || sourceFiles == "" {
		return fmt.Errorf("you need to specify a non-empty value for the flags `-source-owner`, and `-source-repo`")
	}

	// Cleanup any trailing comma in source files
	sourceFiles = strings.TrimSuffix(sourceFiles, ",")

	client := github.NewClient(nil).WithAuthToken(token)
	ctx := context.Background()

	ref, err := getGitRef(ctx, client, sourceOwner, sourceRepo, commitBranch, baseBranch)

	if err != nil {
		return fmt.Errorf("unable to get/create the commit reference: %v", err)
	}
	if ref == nil {
		return fmt.Errorf("no error when returned but the reference is nil when getting/creating commit reference")
	}

	tree, err := getGitTree(ctx, client, ref, sourceFiles, sourceOwner, sourceRepo)
	if err != nil {
		return fmt.Errorf("unable to create the tree based on provided files: %v", err)
	}

	if err = pushCommit(ctx, client, ref, tree, sourceOwner, sourceRepo, prSubject); err != nil {
		return fmt.Errorf("unable to crate the commit: %v", err)
	}

	if err := createPR(ctx, client, prSubject, commitBranch, prDescription, sourceOwner, sourceRepo, baseBranch); err != nil {
		return fmt.Errorf("error while creating the pull request: %v", err)
	}

	return nil
}
