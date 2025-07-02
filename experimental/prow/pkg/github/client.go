package github

import (
	"context"

	"github.com/google/go-github/v57/github"
)

// Client defines the interface for GitHub operations
type Client interface {
	PostComment(ctx context.Context, repo *Repository, issueNumber int, body string) error
	GetDefaultBranch(ctx context.Context, repo *Repository) (string, error)
	GetLatestCommitSHA(ctx context.Context, repo *Repository, branch string) (string, error)
}

// Implementation of the Client interface using GitHub API
type githubClient struct {
	client *github.Client
}

// NewClient creates a new GitHub client
func NewClient(client *github.Client) Client {
	return &githubClient{
		client: client,
	}
}

