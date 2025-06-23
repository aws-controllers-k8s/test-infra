package github

import (
	"github.com/google/go-github/v57/github"
)

// GitHub type aliases for better organization and clarity
type (
	PullRequest       = github.PullRequest
	User              = github.User  
	PullRequestBranch = github.PullRequestBranch
	Repository        = github.Repository
	IssueCommentEvent = github.IssueCommentEvent
	Issue             = github.Issue
	Comment           = github.IssueComment
	IssueEvent        = github.IssuesEvent
)