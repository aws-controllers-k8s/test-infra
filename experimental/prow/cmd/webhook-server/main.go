package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aws-controllers-k8s/test-infra/experimental/prow/pkg/prowjob"
	"github.com/aws-controllers-k8s/test-infra/experimental/prow/pkg/webhook"
)

func main() {
	log.Println("Starting workflow-agent webhook server...")

	// Load workflow configuration
	configPath := os.Getenv("WORKFLOWS_CONFIG_PATH")
	if configPath == "" {
		configPath = "/etc/workflows/workflows.yaml"
	}
	workflowConfig, err := prowjob.LoadWorkflowConfig(configPath)
	if err != nil {
		log.Fatalf("Failed to load workflows: %v", err)
	}

	log.Printf("Loaded %d workflows from %s", len(workflowConfig.GetWorkflowsMap()), configPath)
	
	prowJobGenerator := prowjob.NewGenerator(workflowConfig.GetWorkflowsMap())
	server, err := webhook.NewServer(workflowConfig, prowJobGenerator)
	if err != nil {
		log.Fatalf("Failed to create webhook server: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/tamer", server.HandleWebhook)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	httpServer := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("Starting HTTP server on port %s", port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server failed: %v", err)
		}
	}()

	<-shutdown
	log.Println("Shutting down webhook server...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("HTTP server shutdown failed: %v", err)
	} else {
		log.Println("HTTP server stopped gracefully")
	}
}