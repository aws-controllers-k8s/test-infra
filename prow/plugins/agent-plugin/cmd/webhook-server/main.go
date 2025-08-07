// Copyright 2020 Amazon.com Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/aws-controllers-k8s/test-infra/experimental/prow/pkg/prowjob"
	"github.com/aws-controllers-k8s/test-infra/experimental/prow/pkg/webhook"
	"github.com/sirupsen/logrus"
	"sigs.k8s.io/prow/pkg/config"
	"sigs.k8s.io/prow/pkg/config/secret"
	prowflagutil "sigs.k8s.io/prow/pkg/flagutil"
	"sigs.k8s.io/prow/pkg/interrupts"
	"sigs.k8s.io/prow/pkg/logrusutil"
	"sigs.k8s.io/prow/pkg/pjutil"
	"sigs.k8s.io/prow/pkg/pluginhelp"
	"sigs.k8s.io/prow/pkg/pluginhelp/externalplugins"
)

type options struct {
	port int

	dryRun                 bool
	github                 prowflagutil.GitHubOptions
	instrumentationOptions prowflagutil.InstrumentationOptions
	logLevel               string

	allowedTeam        string
	s3BucketName       string
	webhookSecretFile  string
	workflowConfigPath string
}

func (o *options) Validate() error {
	for idx, group := range []prowflagutil.OptionGroup{&o.github} {
		if err := group.Validate(o.dryRun); err != nil {
			return fmt.Errorf("%d: %w", idx, err)
		}
	}

	if o.allowedTeam == "" {
		return fmt.Errorf("allowed-team is required")
	}

	return nil
}

func gatherOptions() options {
	o := options{}
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	fs.IntVar(&o.port, "port", 8888, "Port to listen on.")
	fs.BoolVar(&o.dryRun, "dry-run", true, "Dry run for testing. Uses API tokens but does not mutate.")
	fs.StringVar(&o.allowedTeam, "allowed-team", "", "Team that is allowed to trigger workflows.")
	fs.StringVar(&o.s3BucketName, "s3-bucket-name", "", "The name of the S3 bucket where ProwJob logs are stored.")
	fs.StringVar(&o.webhookSecretFile, "hmac-secret-file", "/etc/webhook/hmac", "Path to the file containing the GitHub HMAC secret.")
	fs.StringVar(&o.workflowConfigPath, "workflow-config-path", "/etc/workflows/workflows.yaml", "Path to the workflow config file.")
	fs.StringVar(&o.logLevel, "log-level", "info", fmt.Sprintf("Log level is one of %v.", logrus.AllLevels))
	for _, group := range []prowflagutil.OptionGroup{&o.github, &o.instrumentationOptions} {
		group.AddFlags(fs)
	}
	fs.Parse(os.Args[1:])
	return o
}

const pluginName = "workflow-agent"

func main() {
	logrusutil.ComponentInit()

	logrus.Info("Starting workflow-agent plugin server...")
	o := gatherOptions()
	if err := o.Validate(); err != nil {
		logrus.Fatalf("Invalid options: %v", err)
	}

	logLevel, err := logrus.ParseLevel(o.logLevel)
	if err != nil {
		logrus.WithError(err).Fatal("Failed to parse loglevel")
	}
	logrus.SetLevel(logLevel)
	log := logrus.StandardLogger().WithField("plugin", pluginName)

	if err := secret.Add(o.webhookSecretFile); err != nil {
		logrus.WithError(err).Fatal("Error starting secrets agent.")
	}

	githubClient, err := o.github.GitHubClient(o.dryRun)
	if err != nil {
		logrus.WithError(err).Fatal("Error getting GitHub client.")
	}

	// Load workflow configuration
	workflowConfig, err := prowjob.LoadWorkflowConfig(o.workflowConfigPath)
	if err != nil {
		logrus.Fatalf("Failed to load workflows: %v", err)
	}

	logrus.Infof("Loaded %d workflows from %s", len(workflowConfig.GetWorkflowsMap()), o.workflowConfigPath)

	prowJobGenerator := prowjob.NewGenerator(workflowConfig.GetWorkflowsMap())
	server, err := webhook.NewServer(
		workflowConfig,
		prowJobGenerator,
		githubClient,
		secret.GetTokenGenerator(o.webhookSecretFile),
		o.allowedTeam,
		o.s3BucketName,
	)
	if err != nil {
		logrus.Fatalf("Failed to create webhook server: %v", err)
	}

	health := pjutil.NewHealthOnPort(o.instrumentationOptions.HealthPort)
	health.ServeReady()

	mux := http.NewServeMux()
	mux.HandleFunc("/tamer", server.HandleWebhook)
	externalplugins.ServeExternalPluginHelp(mux, log, HelpProvider)
	httpServer := &http.Server{
		Addr:    ":" + strconv.Itoa(o.port),
		Handler: mux,
	}

	defer interrupts.WaitForGracefulShutdown()
	interrupts.ListenAndServe(httpServer, 5*time.Second)
}

// HelpProvider construct the pluginhelp.PluginHelp for this plugin.
func HelpProvider(_ []config.OrgRepo) (*pluginhelp.PluginHelp, error) {
	pluginHelp := &pluginhelp.PluginHelp{
		Description: `The agent-workflow plugin is used for starting agentic code-generation workflows`,
	}
	pluginHelp.AddCommand(pluginhelp.Command{
		Usage:       "/agent [workflow_name] [args]",
		Description: "Start the agent workflow with the specified arguments.",
		Featured:    false,
		// depends on how the cherrypick server runs; needs auth by default (--allow-all=false)
		WhoCanUse: "Members of the trusted organization for the repo.",
		Examples:  []string{"/agent ack_resource_workflow service=ecs resource=capacityprovider"},
	})
	return pluginHelp, nil
}
