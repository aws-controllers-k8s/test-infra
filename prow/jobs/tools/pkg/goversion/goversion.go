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

package goversion

import (
	"fmt"
	"strings"

	"github.com/aquasecurity/go-version/pkg/semver"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/pkg/config"
)

const (
	UpdatePRSubject      = "Update to GO_VERSION %s"
	UpdatePRCommitBranch = "golang-update-%s"
	UpdatePRDescription  = "Update to GO_VERSION from %s to %s "

	// `Comma-separated list of files to commit and their location.
	// 	The local file is separated by its target location by a semi-colon.
	// 	If the file should be in the same location with the same name, you can just put the file name and omit the repetition.
	// 	Example: README.md,main.go:prow/jobs/tools/cmd/main.go`
	UpdateSourceFiles = "build_config.yaml,./prow/jobs/images_config.yaml:prow/jobs/images_config.yaml"
)

func IncreasePatchImageConfig(imagesConfig *config.ImagesConfig) error {

	for image, tag := range imagesConfig.Images {
		// TODO: remove this skip once integration test image is stable
		if image == "integration-test" {
			continue
		}
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
