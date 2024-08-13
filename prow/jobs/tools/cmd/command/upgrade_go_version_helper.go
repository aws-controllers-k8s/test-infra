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
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/aquasecurity/go-version/pkg/semver"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/ecrpublic"
)

const (
	baseBranch             = "main"
	updateGoPRSubject      = "Update to GO_VERSION %s"
	updateGoPRCommitBranch = "golang-update-%s"
	updateGoPRDescription  = "Update to GO_VERSION from %s to %s "

	// `Comma-separated list of files to commit and their location.
	// 	The local file is separated by its target location by a semi-colon.
	// 	If the file should be in the same location with the same name, you can just put the file name and omit the repetition.
	// 	Example: README.md,main.go:prow/jobs/tools/cmd/main.go`
	updateGoSourceFiles    = "build_config.yaml,images_config.yaml:./prow/jobs/images_config.yaml"
)

func listGoVersion(repository string) ([]string, error) {
	client := ecrpublic.New()
	tags, err := client.ListRepositoryTags(repository)
	if err != nil {
		return nil, fmt.Errorf("cannot list repositories in %s. %s", OptGoEcrRepository, err)
	}
	return tags, nil
}

func findHighestGoVersion(tags []string) (string, error) {
	versions := make([]semver.Version, 0, len(tags))
	regex, _ := regexp.Compile(`[a-z]`)

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

func increasePatchImageConfig(imagesConfig *ImagesConfig) error {

	for image, tag := range imagesConfig.Images {
		temp := strings.Split(tag, "-")
		version, err := semver.Parse(temp[len(temp)-1])
		if err != nil {
			return fmt.Errorf("error encountered while using semver.Parse on %s: %s", version, err)
		}
		newVersion := version.IncPatch()
		temp[len(temp)-1] = newVersion.String()
		imagesConfig.Images[image] = strings.Join(temp, "-")
	}
	return nil
}

func patchGoBuildVersion(filepath string, versionConfig *BuildConfig) error {
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
