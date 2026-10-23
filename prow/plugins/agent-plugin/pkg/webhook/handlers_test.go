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
	"errors"
	"testing"

	"sigs.k8s.io/prow/pkg/github"
)

type fakeGitHubClient struct {
	teamMembers map[string]bool
	teamErr     error
	teamLookups int
	// members is what ListTeamMembersBySlug returns.
	members  []string
	comments []string
}

func (f *fakeGitHubClient) CreateComment(_, _ string, _ int, comment string) error {
	f.comments = append(f.comments, comment)
	return nil
}

func (f *fakeGitHubClient) ListTeamMembersBySlug(_, _, _ string) ([]github.TeamMember, error) {
	out := make([]github.TeamMember, 0, len(f.members))
	for _, m := range f.members {
		out = append(out, github.TeamMember{Login: m})
	}
	return out, nil
}

func (f *fakeGitHubClient) TeamBySlugHasMember(_, _, memberLogin string) (bool, error) {
	f.teamLookups++
	if f.teamErr != nil {
		return false, f.teamErr
	}
	return f.teamMembers[memberLogin], nil
}

func newReviewEvent(action github.ReviewEventAction, prAuthor, reviewer string) *github.ReviewEvent {
	return &github.ReviewEvent{
		Action: action,
		Repo: github.Repo{
			Owner:    github.User{Login: "aws-controllers-k8s"},
			Name:     "s3-controller",
			FullName: "aws-controllers-k8s/s3-controller",
		},
		PullRequest: github.PullRequest{
			Number: 42,
			User:   github.User{Login: prAuthor},
		},
		Review: github.Review{
			User:  github.User{Login: reviewer},
			State: "changes_requested",
		},
	}
}

func TestIsAllowedAgentReview(t *testing.T) {
	tests := []struct {
		name            string
		prAuthor        string
		state           github.ReviewState
		event           *github.ReviewEvent
		teamErr         error
		want            bool
		wantErr         bool
		wantTeamLookups int
	}{
		{
			name:            "team member reviews agent PR",
			event:           newReviewEvent(github.ReviewActionSubmitted, "ack-agent", "maintainer"),
			want:            true,
			wantTeamLookups: 1,
		},
		{
			name:            "agent login matched case-insensitively",
			event:           newReviewEvent(github.ReviewActionSubmitted, "ACK-Agent", "maintainer"),
			want:            true,
			wantTeamLookups: 1,
		},
		{
			name:            "configured author normalized",
			prAuthor:        "@ACK-Agent",
			event:           newReviewEvent(github.ReviewActionSubmitted, "ack-agent", "maintainer"),
			want:            true,
			wantTeamLookups: 1,
		},
		{
			name:            "comment-only review accepted",
			state:           "commented",
			event:           newReviewEvent(github.ReviewActionSubmitted, "ack-agent", "maintainer"),
			want:            true,
			wantTeamLookups: 1,
		},
		{
			name:            "approval ignored",
			state:           "approved",
			event:           newReviewEvent(github.ReviewActionSubmitted, "ack-agent", "maintainer"),
			want:            false,
			wantTeamLookups: 0,
		},
		{
			name:            "non-team member reviews agent PR",
			event:           newReviewEvent(github.ReviewActionSubmitted, "ack-agent", "outsider"),
			want:            false,
			wantTeamLookups: 1,
		},
		{
			name:            "PR not authored by agent",
			event:           newReviewEvent(github.ReviewActionSubmitted, "someone-else", "maintainer"),
			want:            false,
			wantTeamLookups: 0,
		},
		{
			name:            "edited review ignored",
			event:           newReviewEvent(github.ReviewActionEdited, "ack-agent", "maintainer"),
			want:            false,
			wantTeamLookups: 0,
		},
		{
			name:            "dismissed review ignored",
			event:           newReviewEvent(github.ReviewActionDismissed, "ack-agent", "maintainer"),
			want:            false,
			wantTeamLookups: 0,
		},
		{
			name:            "team lookup error",
			event:           newReviewEvent(github.ReviewActionSubmitted, "ack-agent", "maintainer"),
			teamErr:         errors.New("boom"),
			want:            false,
			wantErr:         true,
			wantTeamLookups: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gh := &fakeGitHubClient{
				teamMembers: map[string]bool{"maintainer": true},
				teamErr:     tt.teamErr,
			}
			prAuthor := tt.prAuthor
			if prAuthor == "" {
				prAuthor = "ack-agent"
			}
			if tt.state != "" {
				tt.event.Review.State = tt.state
			}
			s := &Server{githubClient: gh, allowedTeam: "ack-agent-users", prAuthor: prAuthor}

			got, err := s.isAllowedAgentReview(tt.event)
			if (err != nil) != tt.wantErr {
				t.Fatalf("isAllowedAgentReview() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("isAllowedAgentReview() = %v, want %v", got, tt.want)
			}
			if gh.teamLookups != tt.wantTeamLookups {
				t.Errorf("team lookups = %d, want %d", gh.teamLookups, tt.wantTeamLookups)
			}
		})
	}
}
