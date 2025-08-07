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

// DefaultTimeout is the standard timeout for API operations
const DefaultTimeout = 30 * time.Second

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
	s3BucketName     string
}

// NewServer creates a new webhook server
func NewServer(
	workflowConfig *prowjobpkg.WorkflowConfig,
	prowJobGenerator prowjobpkg.Generator,
	githubClient githubClient,
	tokenGenerator func() []byte,
	allowedTeam string,
	s3BucketName string,
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
		s3BucketName:     s3BucketName,
	}, nil
}

// ContextWithDefaultTimeout returns a new context with the default timeout
func ContextWithDefaultTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), DefaultTimeout)
}
