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

const jobsDir = "./prow/jobs"
const imagesConfigPath = "/images_config.yaml"
const templatesDir = "/templates"

const jobsImageConfig = jobsDir + imagesConfigPath
const jobsConfig = jobsDir + "/jobs_config.yaml"
const jobsTemplateDir = jobsDir + templatesDir
const jobsOutput = jobsDir + "/jobs.yaml"
const labelsOutput = jobsDir + "/labels.yaml"

const agentWorkflowsDir = "./prow/agent-workflows"
const agentWorkflowsImageConfig = agentWorkflowsDir + imagesConfigPath
const agentWorkflowsTemplateDir = agentWorkflowsDir + templatesDir
const agentWorkflowsOutput = agentWorkflowsDir + "/agent-workflows.yaml"

const pluginsDir = "./prow/plugins"
const pluginsImageConfig = pluginsDir + imagesConfigPath
const pluginsTemplateDir = pluginsDir + templatesDir
const pluginsOutputDir = pluginsDir + "/deployments"

func main() {

	err := generator.Generate(
		"jobs",
		jobsConfig,
		jobsImageConfig,
		jobsTemplateDir,
		jobsOutput,
	)
	if err != nil {
		panic(err)
	}

	err = generator.Generate(
		"labels",
		jobsConfig,
		jobsImageConfig,
		jobsTemplateDir,
		labelsOutput)
	if err != nil {
		panic(err)
	}

	err = generator.GenerateAgentWorkflows(
		agentWorkflowsImageConfig,
		agentWorkflowsTemplateDir,
		agentWorkflowsOutput,
	)
	if err != nil {
		panic(err)
	}

	_, err = generator.GeneratePlugins(
		pluginsImageConfig,
		pluginsTemplateDir,
		pluginsOutputDir,
	)
	if err != nil {
		panic(err)
	}
}
