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
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	cmd "github.com/aws-controllers-k8s/test-infra/experimental/prow/pkg/command"
	"sigs.k8s.io/prow/pkg/github"
)

// HandleWebhook processes GitHub webhook events
func (s *Server) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	eventType, _, payload, ok, _ := github.ValidateWebhook(w, r, s.tokenGenerator)
	if !ok {
		return
	}

	switch eventType {
	case "issue_comment":
		var event github.IssueCommentEvent
		if err := json.Unmarshal(payload, &event); err != nil {
			log.Printf("Failed to parse issue comment event: %v", err)
			http.Error(w, "Failed to parse event", http.StatusBadRequest)
			return
		}
		if err := s.handleIssueComment(&event); err != nil {
			log.Printf("Failed to handle issue comment: %v", err)
			http.Error(w, fmt.Sprintf("Failed to handle comment: %v", err), http.StatusInternalServerError)
			return
		}
	case "issues":
		var event github.IssueEvent
		if err := json.Unmarshal(payload, &event); err != nil {
			log.Printf("Failed to parse issue event: %v", err)
			http.Error(w, "Failed to parse event", http.StatusBadRequest)
			return
		}
		if err := s.handleIssueEvent(&event); err != nil {
			log.Printf("Failed to handle issue event: %v", err)
			http.Error(w, fmt.Sprintf("Failed to handle issue: %v", err), http.StatusInternalServerError)
			return
		}
	default:
		log.Printf("Ignoring unsupported event type: %s", eventType)
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// handleIssueComment processes issue comment events for /agent commands
func (s *Server) handleIssueComment(event *github.IssueCommentEvent) error {
	action := event.Action
	if action != "created" && action != "edited" {
		return nil
	}

	org := event.Repo.Owner.Login
	repo := event.Repo.Name
	num := event.Issue.Number
	commentAuthor := event.Comment.User.Login

	ok, err := s.githubClient.TeamBySlugHasMember(org, s.allowedTeam, commentAuthor)
	if err != nil {
		return fmt.Errorf("failed to check if user is a team member: %v", err)
	}
	if !ok {
		log.Printf("Ignoring comment from non-team member: %s", commentAuthor)
		return nil
	}

	comment := event.Comment.Body
	return s.processAgentCommand(comment, event.Repo, event.Issue, func(message string) error {
		return s.githubClient.CreateComment(org, repo, num, message)
	})
}

// handleIssueEvent processes issue creation events for /agent commands
func (s *Server) handleIssueEvent(event *github.IssueEvent) error {
	if event.Action != "opened" {
		return nil
	}

	body := event.Issue.Body
	org := event.Repo.Owner.Login
	repo := event.Repo
	issue := event.Issue
	user := event.Sender.Login

	ok, err := s.githubClient.TeamBySlugHasMember(org, s.allowedTeam, user)
	if err != nil {
		return fmt.Errorf("failed to check if user is a team member: %v", err)
	}
	if !ok {
		log.Printf("Ignoring comment from non-team member: %s", user)
		return nil
	}

	return s.processAgentCommand(body, repo, issue, func(message string) error {
		return s.githubClient.CreateComment(org, repo.Name, issue.Number, message)
	})
}

// processAgentCommand handles the common logic for processing /agent commands
func (s *Server) processAgentCommand(commandText string, repo github.Repo, issue github.Issue, postComment func(string) error) error {
	agentCmd, err := cmd.ParseAgentCommand(commandText)
	if err != nil {
		return nil
	}

	log.Printf("Processing agent command: workflow=%s, args=%v, flags=%v, issue=#%d",
		agentCmd.WorkflowName, agentCmd.Args, agentCmd.Flags, issue.Number)

	workflow, err := s.workflowConfig.GetWorkflowByName(agentCmd.WorkflowName)
	if err != nil {
		return postComment(fmt.Sprintf("Unknown workflow: %s", agentCmd.WorkflowName))
	}

	if err := agentCmd.ValidateRequiredArgs(workflow.RequiredArgs); err != nil {
		return postComment(err.Error())
	}
	if err := agentCmd.ValidateTimeout(); err != nil {
		return postComment(err.Error())
	}

	timeout := agentCmd.GetTimeout()
	if timeout == "" {
		timeout = workflow.Timeout
	}

	// Create and submit ProwJob
	prowJob, err := s.prowJobGenerator.CreateWorkflowProwJob(agentCmd.WorkflowName, agentCmd.Args, agentCmd.Flags, issue, repo, timeout)
	if err != nil {
		return postComment(fmt.Sprintf("Failed to create workflow job: %v", err))
	}

	if err := s.submitProwJob(prowJob); err != nil {
		log.Printf("Failed to submit ProwJob %s: %v", prowJob.Name, err)
		return postComment(fmt.Sprintf("Failed to submit workflow job: %v", err))
	}

	log.Printf("Successfully created and submitted ProwJob: name=%s, workflow=%s, timeout=%s",
		prowJob.Name, agentCmd.WorkflowName, timeout)

	message := fmt.Sprintf("Started workflow `%s` as job `%s`", agentCmd.WorkflowName, prowJob.Name)
	if timeout != "" {
		message += fmt.Sprintf(" (timeout: %s)", timeout)
	}
	return postComment(message)
}
