package webhook

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	cmd "github.com/aws-controllers-k8s/test-infra/experimental/prow/pkg/command"
	githubclient "github.com/aws-controllers-k8s/test-infra/experimental/prow/pkg/github"
)

// HandleWebhook processes GitHub webhook events
func (s *Server) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("Failed to read request body: %v", err)
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}

	eventType := r.Header.Get("X-GitHub-Event")
	switch eventType {
	case "issue_comment":
		var event githubclient.IssueCommentEvent
		if err := json.Unmarshal(body, &event); err != nil {
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
		var event githubclient.IssueEvent
		if err := json.Unmarshal(body, &event); err != nil {
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
func (s *Server) handleIssueComment(event *githubclient.IssueCommentEvent) error {
	action := event.GetAction()
	if action != "created" && action != "edited" {
		return nil
	}

	comment := event.GetComment().GetBody()
	return s.processAgentCommand(comment, event.GetRepo(), event.GetIssue(), func(message string) error {
		return s.postComment(event.GetRepo(), event.GetIssue().GetNumber(), message)
	})
}

// handleIssueEvent processes issue creation events for /agent commands
func (s *Server) handleIssueEvent(event *githubclient.IssueEvent) error {
	if event.GetAction() != "opened" {
		return nil
	}

	body := event.GetIssue().GetBody()
	return s.processAgentCommand(body, event.GetRepo(), event.GetIssue(), func(message string) error {
		return s.postComment(event.GetRepo(), event.GetIssue().GetNumber(), message)
	})
}

// processAgentCommand handles the common logic for processing /agent commands
func (s *Server) processAgentCommand(commandText string, repo *githubclient.Repository, issue *githubclient.Issue, postComment func(string) error) error {
	agentCmd, err := cmd.ParseAgentCommand(commandText)
	if err != nil {
		return nil
	}

	log.Printf("Processing agent command: workflow=%s, args=%v, flags=%v, issue=#%d", 
		agentCmd.WorkflowName, agentCmd.Args, agentCmd.Flags, issue.GetNumber())

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