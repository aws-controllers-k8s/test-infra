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
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/prow/pkg/github"

	prowjobpkg "github.com/aws-controllers-k8s/test-infra/experimental/prow/pkg/prowjob"
)

// addressFeedbackWorkflow is the workflow an accepted review starts. It is
// never started by an /agent command, because its inputs (the review and the
// team member list) must come from the review event, not from a commenter.
const addressFeedbackWorkflow = "address-feedback"

var (
	// Same rules as workflows/validation.py in agent-workflows.
	resourceNameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)
	fieldPathRE    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*(?:\.[A-Za-z][A-Za-z0-9]*)*$`)
	commitSHARE    = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// agentBranch is what an agent PR's head branch says about the change.
type agentBranch struct {
	Service  string
	Resource string
	// Field is set for PRs opened by the add-field workflow.
	Field string
}

// parseAgentBranch reads the service, resource and field from the head branch
// of a PR opened by an agent workflow. prow-job.sh names those branches
// <service>-add-<Resource> (add-resource) or <service>-add-<Resource>-<field>
// (add-field), and the repository is <service>-controller.
func parseAgentBranch(repoName, headRef string) (agentBranch, error) {
	service, ok := strings.CutSuffix(repoName, "-controller")
	if !ok || service == "" {
		return agentBranch{}, fmt.Errorf("repository %q is not a service controller", repoName)
	}
	rest, ok := strings.CutPrefix(headRef, service+"-add-")
	if !ok {
		return agentBranch{}, fmt.Errorf("branch %q does not match %s-add-<Resource>[-<field>]", headRef, service)
	}
	// Neither resource names nor field paths contain hyphens, so the first
	// hyphen separates them.
	resource, field, _ := strings.Cut(rest, "-")
	if !resourceNameRE.MatchString(resource) {
		return agentBranch{}, fmt.Errorf("branch %q has an invalid resource name %q", headRef, resource)
	}
	if field != "" && !fieldPathRE.MatchString(field) {
		return agentBranch{}, fmt.Errorf("branch %q has an invalid field path %q", headRef, field)
	}
	return agentBranch{Service: service, Resource: resource, Field: field}, nil
}

// startFeedbackRun starts an address-feedback ProwJob for an accepted review.
// It runs at most one job per PR at a time, and tells the PR when it starts a
// job or why it did not.
func (s *Server) startFeedbackRun(event *github.ReviewEvent, reviewSHA string) error {
	org := event.Repo.Owner.Login
	repo := event.Repo.Name
	num := event.PullRequest.Number
	comment := func(message string) error {
		return s.githubClient.CreateComment(org, repo, num, message)
	}

	workflow, err := s.workflowConfig.GetWorkflowByName(addressFeedbackWorkflow)
	if err != nil {
		return fmt.Errorf("cannot address review on %s#%d: %w", event.Repo.FullName, num, err)
	}
	branch, err := parseAgentBranch(repo, event.PullRequest.Head.Ref)
	if err != nil {
		log.Printf("Not addressing review on %s#%d: %v", event.Repo.FullName, num, err)
		return comment(fmt.Sprintf("The agent can't address feedback on this pull request: %v.", err))
	}
	if !commitSHARE.MatchString(reviewSHA) {
		return fmt.Errorf("review %d on %s#%d has no valid commit_id: %q",
			event.Review.ID, event.Repo.FullName, num, reviewSHA)
	}

	// Two reviews close together arrive as concurrent webhooks. Holding the
	// lock from the check to the submit keeps them from both starting a job.
	// The plugin runs as a single replica, so a process-local lock is enough.
	s.feedbackMu.Lock()
	defer s.feedbackMu.Unlock()

	active, err := s.activeFeedbackJob(org, repo, num)
	if err != nil {
		return err
	}
	if active != "" {
		log.Printf("Not addressing review on %s#%d: job %s is still running", event.Repo.FullName, num, active)
		return comment(fmt.Sprintf(
			"The agent is already addressing feedback on this pull request (job `%s`). "+
				"Submit another review after it finishes to have this feedback addressed.", active))
	}

	members, err := s.githubClient.ListTeamMembersBySlug(org, s.allowedTeam, github.RoleAll)
	if err != nil {
		return fmt.Errorf("failed to list members of team %s: %w", s.allowedTeam, err)
	}
	logins := make([]string, 0, len(members))
	for _, m := range members {
		logins = append(logins, github.NormLogin(m.Login))
	}
	sort.Strings(logins)

	args := map[string]string{
		"service":      branch.Service,
		"resource":     branch.Resource,
		"review-id":    strconv.Itoa(event.Review.ID),
		"review-sha":   reviewSHA,
		"team-members": strings.Join(logins, ","),
	}
	if branch.Field != "" {
		args["field"] = branch.Field
	}

	prowJob, err := s.prowJobGenerator.CreateWorkflowProwJob(
		addressFeedbackWorkflow,
		args,
		nil,
		github.Issue{Number: num, User: event.PullRequest.User},
		event.Repo,
		workflow.Timeout,
		s.prowJobNamespace,
		s.s3BucketName,
	)
	if err != nil {
		return comment(fmt.Sprintf("Failed to create workflow job: %v", err))
	}
	if err := s.submitProwJob(prowJob); err != nil {
		log.Printf("Failed to submit ProwJob %s: %v", prowJob.Name, err)
		return comment(fmt.Sprintf("Failed to submit workflow job: %v", err))
	}

	log.Printf("Started %s job %s for review %d on %s#%d",
		addressFeedbackWorkflow, prowJob.Name, event.Review.ID, event.Repo.FullName, num)
	return comment(fmt.Sprintf("Started workflow `%s` as job `%s` to address this review.",
		addressFeedbackWorkflow, prowJob.Name))
}

// activeFeedbackJob returns the name of an address-feedback job for the PR
// that has not completed, or "" if there is none.
func (s *Server) activeFeedbackJob(org, repo string, num int) (string, error) {
	selector := labels.SelectorFromSet(labels.Set{
		prowjobpkg.WorkflowTypeLabel: addressFeedbackWorkflow,
		prowjobpkg.OrgLabel:          org,
		prowjobpkg.RepoLabel:         repo,
		prowjobpkg.IssueNumberLabel:  strconv.Itoa(num),
	}).String()

	ctx, cancel := ContextWithDefaultTimeout()
	defer cancel()
	jobs, err := s.k8sProwClient.ListProwJobs(ctx, s.prowJobNamespace, selector)
	if err != nil {
		return "", fmt.Errorf("failed to check for running %s jobs: %w", addressFeedbackWorkflow, err)
	}
	for i := range jobs {
		if !jobs[i].Complete() {
			return jobs[i].Name, nil
		}
	}
	return "", nil
}
