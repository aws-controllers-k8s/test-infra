package webhook

import (
	"context"
	"log"
	"os"
	"time"

	"k8s.io/client-go/kubernetes"

	k8sclient "github.com/aws-controllers-k8s/test-infra/experimental/prow/pkg/k8s"
	prowjobpkg "github.com/aws-controllers-k8s/test-infra/experimental/prow/pkg/prowjob"
)

type githubClient interface {
	CreateComment(org, repo string, number int, comment string) error
	TeamBySlugHasMember(org string, teamSlug string, memberLogin string) (bool, error)
}

// Server handles webhook requests
type Server struct {
	tokenGenerator   func() []byte
	workflowConfig   *prowjobpkg.WorkflowConfig
	prowJobGenerator prowjobpkg.Generator
	githubClient     githubClient
	k8sClient        kubernetes.Interface
	k8sProwClient    k8sclient.Client
	prowJobNamespace string
	allowedTeam      string
}

// NewServer creates a new webhook server
func NewServer(
	workflowConfig *prowjobpkg.WorkflowConfig,
	prowJobGenerator prowjobpkg.Generator,
	githubClient githubClient,
	tokenGenerator func() []byte,
	allowedTeam string,
) (*Server, error) {
	prowJobNamespace := os.Getenv("PROW_JOB_NAMESPACE")
	if prowJobNamespace == "" {
		prowJobNamespace = "prow-jobs"
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
		tokenGenerator:   tokenGenerator,
		workflowConfig:   workflowConfig,
		prowJobGenerator: prowJobGenerator,
		githubClient:     githubClient,
		k8sClient:        k8sClient,
		k8sProwClient:    k8sProwClient,
		prowJobNamespace: prowJobNamespace,
		allowedTeam:      allowedTeam,
	}, nil
}

// DefaultTimeout is the standard timeout for API operations
const DefaultTimeout = 30 * time.Second

// ContextWithDefaultTimeout returns a new context with the default timeout
func ContextWithDefaultTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), DefaultTimeout)
}
