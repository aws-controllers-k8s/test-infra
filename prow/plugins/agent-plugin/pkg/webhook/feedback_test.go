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
	"os"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/prow/pkg/github"

	k8sclient "github.com/aws-controllers-k8s/test-infra/experimental/prow/pkg/k8s"
	prowjobpkg "github.com/aws-controllers-k8s/test-infra/experimental/prow/pkg/prowjob"
)

const testReviewSHA = "da4611c0123456789abcdef0123456789abcdef0"

type fakeProwClient struct {
	existing  []k8sclient.ProwJob
	submitted []*k8sclient.ProwJob
	selectors []string
}

func (f *fakeProwClient) SubmitProwJob(_ context.Context, job *k8sclient.ProwJob, _ string) error {
	f.submitted = append(f.submitted, job)
	return nil
}

func (f *fakeProwClient) ListProwJobs(_ context.Context, _ string, selector string) ([]k8sclient.ProwJob, error) {
	f.selectors = append(f.selectors, selector)
	return f.existing, nil
}

// newFeedbackServer returns a Server with fake clients and a real generator
// that reads a minimal address-feedback workflow config.
func newFeedbackServer(t *testing.T, gh *fakeGitHubClient, prow *fakeProwClient) *Server {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workflows.yaml")
	config := `
workflows:
  address-feedback:
    description: test
    image: repo:tag
    command: ["/app/prow-job.sh"]
    timeout: "5h"
`
	if err := os.WriteFile(path, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	loader := prowjobpkg.NewWorkflowConfigLoader(path)
	return &Server{
		workflowConfig:   loader,
		prowJobGenerator: prowjobpkg.NewGenerator(loader),
		githubClient:     gh,
		k8sProwClient:    prow,
		prowJobNamespace: "prow-jobs",
		allowedTeam:      "ack-agent-users",
		prAuthor:         "ack-agent",
	}
}

func newFeedbackEvent(headRef string) *github.ReviewEvent {
	event := newReviewEvent(github.ReviewActionSubmitted, "ack-agent", "maintainer")
	event.PullRequest.Head.Ref = headRef
	event.Review.ID = 7
	return event
}

// containerArgs turns the job's "--key value" args back into a map.
func containerArgs(t *testing.T, job *k8sclient.ProwJob) map[string]string {
	t.Helper()
	args := job.Spec.PodSpec.Containers[0].Args
	if len(args)%2 != 0 {
		t.Fatalf("args are not --key value pairs: %v", args)
	}
	out := map[string]string{}
	for i := 0; i < len(args); i += 2 {
		out[strings.TrimPrefix(args[i], "--")] = args[i+1]
	}
	return out
}

func TestParseAgentBranch(t *testing.T) {
	tests := []struct {
		name    string
		repo    string
		ref     string
		want    agentBranch
		wantErr bool
	}{
		{name: "add-resource", repo: "s3-controller", ref: "s3-add-Bucket",
			want: agentBranch{Service: "s3", Resource: "Bucket"}},
		{name: "add-field", repo: "s3-controller", ref: "s3-add-Bucket-Tagging",
			want: agentBranch{Service: "s3", Resource: "Bucket", Field: "Tagging"}},
		{name: "nested field", repo: "s3-controller", ref: "s3-add-Bucket-Logging.TargetBucket",
			want: agentBranch{Service: "s3", Resource: "Bucket", Field: "Logging.TargetBucket"}},
		{name: "hyphenated service", repo: "opensearch-serverless-controller", ref: "opensearch-serverless-add-Collection",
			want: agentBranch{Service: "opensearch-serverless", Resource: "Collection"}},
		{name: "not a controller repo", repo: "test-infra", ref: "test-infra-add-Bucket", wantErr: true},
		{name: "other service", repo: "s3-controller", ref: "ec2-add-Vpc", wantErr: true},
		{name: "not an agent branch", repo: "s3-controller", ref: "fix-tagging", wantErr: true},
		{name: "empty resource", repo: "s3-controller", ref: "s3-add-", wantErr: true},
		{name: "invalid field", repo: "s3-controller", ref: "s3-add-Bucket-a-b", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAgentBranch(tt.repo, tt.ref)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseAgentBranch() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("parseAgentBranch() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestStartFeedbackRun(t *testing.T) {
	gh := &fakeGitHubClient{members: []string{"Maintainer", "another"}}
	prow := &fakeProwClient{}
	s := newFeedbackServer(t, gh, prow)

	if err := s.startFeedbackRun(newFeedbackEvent("s3-add-Bucket-Tagging"), testReviewSHA); err != nil {
		t.Fatalf("startFeedbackRun() error = %v", err)
	}
	if len(prow.submitted) != 1 {
		t.Fatalf("submitted %d jobs, want 1", len(prow.submitted))
	}
	job := prow.submitted[0]

	wantArgs := map[string]string{
		"service":      "s3",
		"resource":     "Bucket",
		"field":        "Tagging",
		"review-id":    "7",
		"review-sha":   testReviewSHA,
		"team-members": "another,maintainer",
	}
	gotArgs := containerArgs(t, job)
	for k, v := range wantArgs {
		if gotArgs[k] != v {
			t.Errorf("arg %s = %q, want %q", k, gotArgs[k], v)
		}
	}
	if len(gotArgs) != len(wantArgs) {
		t.Errorf("args = %v, want only %v", gotArgs, wantArgs)
	}
	if got := job.Labels[prowjobpkg.IssueNumberLabel]; got != "42" {
		t.Errorf("issue-number label = %q, want 42", got)
	}
	wantSelector := "prow.k8s.io/refs.org=aws-controllers-k8s,prow.k8s.io/refs.repo=s3-controller," +
		"workflow-agent/issue-number=42,workflow-type=address-feedback"
	if len(prow.selectors) != 1 || prow.selectors[0] != wantSelector {
		t.Errorf("selectors = %v, want [%s]", prow.selectors, wantSelector)
	}
	if len(gh.comments) != 1 || !strings.Contains(gh.comments[0], job.Name) {
		t.Errorf("comments = %v, want one naming %s", gh.comments, job.Name)
	}
}

func TestStartFeedbackRunSkips(t *testing.T) {
	running := k8sclient.ProwJob{ObjectMeta: metav1.ObjectMeta{Name: "running-job"}}
	now := metav1.Now()
	finished := k8sclient.ProwJob{ObjectMeta: metav1.ObjectMeta{Name: "finished-job"}}
	finished.Status.CompletionTime = &now

	tests := []struct {
		name        string
		headRef     string
		existing    []k8sclient.ProwJob
		wantSubmit  bool
		wantComment string
	}{
		{name: "finished job does not block", headRef: "s3-add-Bucket",
			existing: []k8sclient.ProwJob{finished}, wantSubmit: true, wantComment: "Started workflow"},
		{name: "running job blocks", headRef: "s3-add-Bucket",
			existing: []k8sclient.ProwJob{finished, running}, wantComment: "already addressing feedback on this pull request (job `running-job`)"},
		{name: "unrecognized branch", headRef: "fix-tagging",
			wantComment: "can't address feedback on this pull request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gh := &fakeGitHubClient{members: []string{"maintainer"}}
			prow := &fakeProwClient{existing: tt.existing}
			s := newFeedbackServer(t, gh, prow)

			if err := s.startFeedbackRun(newFeedbackEvent(tt.headRef), testReviewSHA); err != nil {
				t.Fatalf("startFeedbackRun() error = %v", err)
			}
			if got := len(prow.submitted) == 1; got != tt.wantSubmit {
				t.Errorf("submitted %d jobs, wantSubmit %v", len(prow.submitted), tt.wantSubmit)
			}
			if len(gh.comments) != 1 || !strings.Contains(gh.comments[0], tt.wantComment) {
				t.Errorf("comments = %v, want one containing %q", gh.comments, tt.wantComment)
			}
		})
	}
}

func TestStartFeedbackRunRejectsBadCommit(t *testing.T) {
	prow := &fakeProwClient{}
	s := newFeedbackServer(t, &fakeGitHubClient{}, prow)
	if err := s.startFeedbackRun(newFeedbackEvent("s3-add-Bucket"), "not-a-sha"); err == nil {
		t.Fatal("startFeedbackRun() error = nil, want an error for an invalid commit_id")
	}
	if len(prow.submitted) != 0 || len(prow.selectors) != 0 {
		t.Errorf("submitted %d jobs and listed %d times, want neither", len(prow.submitted), len(prow.selectors))
	}
}

func TestAgentCommandCannotStartFeedbackRun(t *testing.T) {
	prow := &fakeProwClient{}
	gh := &fakeGitHubClient{}
	s := newFeedbackServer(t, gh, prow)
	err := s.processAgentCommand(
		"/agent address-feedback service=s3 resource=Bucket team-members=attacker",
		github.Repo{Owner: github.User{Login: "aws-controllers-k8s"}, Name: "s3-controller"},
		github.Issue{Number: 1},
		func(m string) error { return gh.CreateComment("", "", 0, m) },
	)
	if err != nil {
		t.Fatalf("processAgentCommand() error = %v", err)
	}
	if len(prow.submitted) != 0 {
		t.Errorf("submitted %d jobs, want 0", len(prow.submitted))
	}
	if len(gh.comments) != 1 || !strings.Contains(gh.comments[0], "can't be run with /agent") {
		t.Errorf("comments = %v, want a refusal", gh.comments)
	}
}
