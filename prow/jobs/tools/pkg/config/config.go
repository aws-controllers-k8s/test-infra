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

package config

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/aquasecurity/go-version/pkg/semver"
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

func ReadBuildConfigFile(filepath string) (*BuildConfig, error) {
	fileData, err := os.ReadFile(filepath)
	if err != nil {
		return nil, fmt.Errorf("unable to read file %s: %s", filepath, err)
	}

	var configGoVersion *BuildConfig
	if err := yaml.Unmarshal(fileData, &configGoVersion); err != nil {
		return nil, fmt.Errorf("unable to unmarshal yaml file: %s. %s", fileData, err)
	}

	return configGoVersion, nil
}

func ReadCurrentImagesConfig(filepath string) (*ImagesConfig, error) {
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

func PatchImageConfigVersionFile(imagesConfig *ImagesConfig, filepath string) error {
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

func PatchBuildVersionFile(versionConfig *BuildConfig, filepath string) error {
	file, err := os.Create(filepath)
	if err != nil {
		return fmt.Errorf("unable to create file %s: %s", filepath, err)
	}
	defer file.Close()

	if err = yaml.NewEncoder(file).Encode(&versionConfig); err != nil {
		return fmt.Errorf("unable to encode %v to %s: %v", versionConfig, filepath, err)
	}
	return nil
}

// IsGreaterVersion returns true if v1 is greater than v2, else returns false
func IsGreaterVersion(v1, v2 string) (bool, error) {

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

func FindHighestTagVersion(tags []string) (string, error) {
	versions := make([]semver.Version, 0, len(tags))
	regex := regexp.MustCompile(`[a-z]`)

	for _, tag := range tags {
		temp := strings.Split(tag, ".")
		if regex.MatchString(tag) || len(temp) != 3 {
			continue
		}
		v, err := semver.Parse(tag)
		if err != nil {
			return "", fmt.Errorf("error while parsing version %s: %s", tag, err)
		}
		versions = append(versions, v)
	}

	sort.Sort(semver.Collection(versions))

	if len(versions) < 1 {
		return "", fmt.Errorf("unable to retrieve highest GO version from tags %s", tags)
	}

	return versions[len(versions)-1].String(), nil
}
