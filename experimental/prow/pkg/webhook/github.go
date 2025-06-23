package webhook

import (
	"log"

	githubclient "github.com/aws-controllers-k8s/test-infra/experimental/prow/pkg/github"
)

// postComment posts a comment to GitHub issue/PR with formatting and signature
func (s *Server) postComment(repo *githubclient.Repository, issueNumber int, message string) error {
	ctx, cancel := ContextWithDefaultTimeout()
	defer cancel()
	return s.githubClient.PostComment(ctx, repo, issueNumber, message)
}

// getDefaultBranch returns the default branch for a repository
func (s *Server) getDefaultBranch(repo *githubclient.Repository) string {
	ctx, cancel := ContextWithDefaultTimeout()
	defer cancel()
	defaultBranch, err := s.githubClient.GetDefaultBranch(ctx, repo)
	if err != nil {
		log.Printf("Failed to get default branch: %v", err)
		return "main"
	}
	return defaultBranch
}

// getLatestCommitSHA returns the latest commit SHA for a branch
func (s *Server) getLatestCommitSHA(repo *githubclient.Repository, branch string) string {
	ctx, cancel := ContextWithDefaultTimeout()
	defer cancel()
	sha, err := s.githubClient.GetLatestCommitSHA(ctx, repo, branch)
	if err != nil {
		log.Printf("Failed to get latest commit SHA: %v", err)
		return "latest"
	}
	return sha
}

