package webhook

import (
	githubclient "github.com/aws-controllers-k8s/test-infra/experimental/prow/pkg/github"
	k8sclient "github.com/aws-controllers-k8s/test-infra/experimental/prow/pkg/k8s"
)

// postComment posts a comment to GitHub issue/PR with formatting and signature
func (s *Server) postComment(repo *githubclient.Repository, issueNumber int, message string) error {
	ctx, cancel := ContextWithDefaultTimeout()
	defer cancel()
	return s.githubClient.PostComment(ctx, repo, issueNumber, message)
}

// submitProwJob submits a ProwJob to the Kubernetes cluster
func (s *Server) submitProwJob(prowJob *k8sclient.ProwJob) error {
	ctx, cancel := ContextWithDefaultTimeout()
	defer cancel()
	return s.k8sProwClient.SubmitProwJob(ctx, prowJob, s.prowJobNamespace)
}