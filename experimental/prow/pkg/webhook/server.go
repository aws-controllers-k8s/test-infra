package webhook

import (
	"context"
	"log"
	"os"
	"time"

	"k8s.io/client-go/kubernetes"

	githubclient "github.com/aws-controllers-k8s/test-infra/experimental/prow/pkg/github"
	k8sclient "github.com/aws-controllers-k8s/test-infra/experimental/prow/pkg/k8s"
	prowjobpkg "github.com/aws-controllers-k8s/test-infra/experimental/prow/pkg/prowjob"
)

// Server handles webhook requests
type Server struct {
	workflowConfig   *prowjobpkg.WorkflowConfig
	prowJobGenerator prowjobpkg.Generator
	githubClient     githubclient.Client
	k8sClient        kubernetes.Interface
	k8sProwClient    k8sclient.Client
	prowJobNamespace string
}

// NewServer creates a new webhook server
func NewServer(workflowConfig *prowjobpkg.WorkflowConfig, prowJobGenerator prowjobpkg.Generator) (*Server, error) {
	githubAppPath := os.Getenv("GITHUB_APP_PATH")
	if githubAppPath == "" {
		githubAppPath = "/etc/github"
	}

	prowJobNamespace := os.Getenv("PROW_JOB_NAMESPACE")
	if prowJobNamespace == "" {
		prowJobNamespace = "prow-jobs"
	}
	
	// Initialize GitHub client
	githubClient, err := githubclient.NewAppAuthClient(githubAppPath)
	if err != nil {
		log.Printf("Warning: Failed to initialize GitHub client: %v", err)
		log.Printf("Ensure the GitHub App credentials exist at %s and have proper permissions", githubAppPath)
		return nil, err
	}
	
	// Initialize Kubernetes clients
	k8sClient, err := k8sclient.GetKubernetesClientset()
	if err != nil {
		log.Printf("Warning: Failed to initialize Kubernetes client: %v", err)
	}
	
	k8sProwClient, err := k8sclient.NewClientFromConfig()
	if err != nil {
		log.Printf("Warning: Failed to initialize Kubernetes Prow client: %v", err)
		return nil, err
	}
	
	return &Server{
		workflowConfig:   workflowConfig,
		prowJobGenerator: prowJobGenerator,
		githubClient:     githubClient,
		k8sClient:        k8sClient,
		k8sProwClient:    k8sProwClient,
		prowJobNamespace: prowJobNamespace,
	}, nil
}

// DefaultTimeout is the standard timeout for API operations
const DefaultTimeout = 30 * time.Second

// ContextWithDefaultTimeout returns a new context with the default timeout
func ContextWithDefaultTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), DefaultTimeout)
}