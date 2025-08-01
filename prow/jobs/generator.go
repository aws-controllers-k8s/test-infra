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

package main

import "github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/generator"

func main() {

	err := generator.Generate(
		"jobs",
		"./prow/jobs/jobs_config.yaml",
		"./prow/jobs/images_config.yaml",
		"./prow/jobs/templates",
		"./prow/jobs/jobs.yaml",
	)
	if err != nil {
		panic(err)
	}

	err = generator.Generate(
		"labels",
		"./prow/jobs/jobs_config.yaml",
		"./prow/jobs/images_config.yaml",
		"./prow/jobs/templates",
		"./prow/jobs/labels.yaml")
	if err != nil {
		panic(err)
	}

	err = generator.GenerateAgentWorkflows(
		"./prow/agent-workflows/images_config.yaml",
		"./prow/agent-workflows/templates",
		"./prow/agent-workflows/agent-workflows.yaml",
	)
	if err != nil {
		panic(err)
	}

	_, err = generator.GeneratePlugins(
		"./prow/plugins/images_config.yaml",
		"./prow/plugins/templates",
		"./prow/plugins/deployments",
	)
	if err != nil {
		panic(err)
	}
}
