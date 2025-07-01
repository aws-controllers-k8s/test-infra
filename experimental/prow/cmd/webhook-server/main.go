package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/aws-controllers-k8s/test-infra/experimental/prow/pkg/prowjob"
	"github.com/aws-controllers-k8s/test-infra/experimental/prow/pkg/webhook"
	"github.com/sirupsen/logrus"
	"sigs.k8s.io/prow/pkg/config"
	prowflagutil "sigs.k8s.io/prow/pkg/flagutil"
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

	webhookSecretFile  string
	workflowConfigPath string
}

func (o *options) Validate() error {
	for idx, group := range []prowflagutil.OptionGroup{&o.github} {
		if err := group.Validate(o.dryRun); err != nil {
			return fmt.Errorf("%d: %w", idx, err)
		}
	}

	return nil
}

func gatherOptions() options {
	o := options{}
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	fs.IntVar(&o.port, "port", 8888, "Port to listen on.")
	fs.BoolVar(&o.dryRun, "dry-run", true, "Dry run for testing. Uses API tokens but does not mutate.")
	fs.StringVar(&o.webhookSecretFile, "hmac-secret-file", "/etc/webhook/hmac", "Path to the file containing the GitHub HMAC secret.")
	fs.StringVar(&o.workflowConfigPath, "workflow-config-path", "/etc/workflows/workflows.yaml", "Path to the workflow config file.")
	fs.StringVar(&o.logLevel, "log-level", "info", fmt.Sprintf("Log level is one of %v.", logrus.AllLevels))
	for _, group := range []prowflagutil.OptionGroup{&o.github, &o.instrumentationOptions} {
		group.AddFlags(fs)
	}
	fs.Parse(os.Args[1:])
	return o
}

const pluginName = "agent-workflow"

func main() {
	logrusutil.ComponentInit()

	logrus.Info("Starting workflow-agent webhook server...")
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

	// Load workflow configuration
	workflowConfig, err := prowjob.LoadWorkflowConfig(o.workflowConfigPath)
	if err != nil {
		logrus.Fatalf("Failed to load workflows: %v", err)
	}

	logrus.Infof("Loaded %d workflows from %s", len(workflowConfig.GetWorkflowsMap()), o.workflowConfigPath)

	prowJobGenerator := prowjob.NewGenerator(workflowConfig.GetWorkflowsMap())
	server, err := webhook.NewServer(workflowConfig, prowJobGenerator)
	if err != nil {
		logrus.Fatalf("Failed to create webhook server: %v", err)
	}

	health := pjutil.NewHealthOnPort(o.instrumentationOptions.HealthPort)
	health.ServeReady()

	mux := http.NewServeMux()
	mux.HandleFunc("/tamer", server.HandleWebhook)

	httpServer := &http.Server{
		Addr:    ":" + strconv.Itoa(o.port),
		Handler: mux,
	}

	externalplugins.ServeExternalPluginHelp(mux, log, HelpProvider)

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

	go func() {
		logrus.Printf("Starting HTTP server on port %s", strconv.Itoa(o.port))
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logrus.Fatalf("HTTP server failed: %v", err)
		}
	}()

	<-shutdown
	logrus.Info("Shutting down webhook server...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(ctx); err != nil {
		logrus.Errorf("HTTP server shutdown failed: %v", err)
	} else {
		logrus.Info("HTTP server stopped gracefully")
	}
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
