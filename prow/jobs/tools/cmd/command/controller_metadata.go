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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ackGenerateMetadata is the subset of a controller's
// apis/v1alpha1/ack-generate-metadata.yaml that identifies which AWS SDK
// version the controller was last generated against.
type ackGenerateMetadata struct {
	AWSSDKGoVersion      string `yaml:"aws_sdk_go_version"`
	AWSServiceSDKVersion string `yaml:"aws_service_sdk_version"`
}

// readGenerateMetadata returns the core SDK version and the per-service SDK
// version a controller was generated against. Exactly one of the two is
// normally present, and they are effectively mutually exclusive: of the 76
// controllers, the eight that pin a per-service version — acm, athena,
// bedrockagentcorecontrol, cloudwatch, cloudwatchlogs, eks, lambdacore,
// lambdamicrovms — carry no `aws_sdk_go_version` key at all. Requiring a core
// pin would therefore hard-fail on every one of them.
//
// Callers must handle an empty core version. Model fetching only needs the core
// tag when the per-service tag is absent, so an empty core version alongside a
// present service version is valid, not an error.
func readGenerateMetadata(controllerPath string) (string, string, error) {
	path := filepath.Join(controllerPath, "apis", "v1alpha1", "ack-generate-metadata.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("unable to read %s: %s", path, err)
	}

	var meta ackGenerateMetadata
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return "", "", fmt.Errorf("unable to unmarshal %s: %s", path, err)
	}
	if meta.AWSSDKGoVersion == "" && meta.AWSServiceSDKVersion == "" {
		return "", "", fmt.Errorf(
			"%s has neither aws_sdk_go_version nor aws_service_sdk_version", path,
		)
	}
	return meta.AWSSDKGoVersion, meta.AWSServiceSDKVersion, nil
}

// readGoModServiceVersion returns the version of the service's aws-sdk-go-v2
// module the controller's go.mod requires, or "" when it requires none or has no
// go.mod.
//
// This is the SDK release the controller builds against, and it is not the same
// thing as the model it was generated from. ec2-controller was generated from core
// v1.41.1 but requires service/ec2 v1.290.1, whose model already has
// SecondaryNetwork; reporting that as new since the pin was wrong, because the
// controller has had it in its SDK for many releases.
//
// A line scan rather than golang.org/x/mod, which is not a dependency here: a
// require line is `<path> <version>` whether or not it sits in a block.
func readGoModServiceVersion(controllerPath, packageName string) (string, error) {
	data, err := os.ReadFile(filepath.Join(controllerPath, "go.mod"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("unable to read go.mod: %s", err)
	}
	module := "github.com/aws/aws-sdk-go-v2/service/" + packageName
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "require "))
		if len(fields) >= 2 && fields[0] == module {
			return fields[1], nil
		}
	}
	return "", nil
}
