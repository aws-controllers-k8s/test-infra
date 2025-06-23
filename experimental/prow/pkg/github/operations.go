package github

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/go-github/v57/github"
)

// PostComment posts a comment to a GitHub issue or PR
func (c *githubClient) PostComment(ctx context.Context, repo *Repository, issueNumber int, body string) error {
	if c.client == nil {
		return fmt.Errorf("github client not initialized")
	}

	repoOwner := repo.GetOwner().GetLogin()
	repoName := repo.GetName()
	
	// Format the comment with timestamp signature
	commentBody := fmt.Sprintf("%s\n\n_Posted by workflow-agent at %s_", body, time.Now().Format(time.RFC3339))
	
	comment := &github.IssueComment{
		Body: &commentBody,
	}
	
	log.Printf("Attempting to post comment to repo=%s/%s, issue=#%d", 
		repoOwner, repoName, issueNumber)
		
	_, resp, err := c.client.Issues.CreateComment(ctx, 
		repoOwner, 
		repoName, 
		issueNumber, 
		comment)
	
	if err != nil {
		if resp != nil {
			log.Printf("GitHub API response status: %s", resp.Status)
			if resp.StatusCode == 401 {
				log.Printf("GitHub authentication failed. Check token permissions for repo: %s/%s", 
					repoOwner, repoName)
			} else if resp.StatusCode == 404 {
				log.Printf("Repository or issue not found: %s/%s #%d", 
					repoOwner, repoName, issueNumber)
			}
		}
		return fmt.Errorf("failed to post GitHub comment: %w", err)
	}
	
	log.Printf("Successfully posted GitHub comment: repo=%s/%s, issue=#%d", 
		repoOwner, repoName, issueNumber)
	return nil
}

// GetDefaultBranch returns the default branch for a repository
func (c *githubClient) GetDefaultBranch(ctx context.Context, repo *Repository) (string, error) {
	if c.client == nil {
		return "", fmt.Errorf("github client not initialized")
	}

	repository, _, err := c.client.Repositories.Get(ctx, 
		repo.GetOwner().GetLogin(), 
		repo.GetName())
	
	if err != nil {
		return "main", fmt.Errorf("failed to get repository info: %w", err)
	}
	
	if repository.DefaultBranch != nil {
		return *repository.DefaultBranch, nil
	}
	
	return "main", nil
}

// GetLatestCommitSHA returns the latest commit SHA for a branch
func (c *githubClient) GetLatestCommitSHA(ctx context.Context, repo *Repository, branch string) (string, error) {
	if c.client == nil {
		return "", fmt.Errorf("github client not initialized")
	}

	ref, _, err := c.client.Git.GetRef(ctx, 
		repo.GetOwner().GetLogin(), 
		repo.GetName(), 
		"refs/heads/"+branch)
	
	if err != nil {
		return "latest", fmt.Errorf("failed to get branch ref: %w", err)
	}
	
	if ref.Object != nil && ref.Object.SHA != nil {
		return *ref.Object.SHA, nil
	}
	
	return "latest", nil
}