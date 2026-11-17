// Copyright Amazon.com Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//     http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

package sdkbump

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/go-github/v63/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testForge(t *testing.T, h http.HandlerFunc) *GitHubForge {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := github.NewClient(srv.Client())
	u, err := url.Parse(srv.URL + "/")
	require.NoError(t, err)
	c.BaseURL = u
	return &GitHubForge{Client: c}
}

func TestGitHubForgePRsForBranch(t *testing.T) {
	f := testForge(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/aws-controllers-k8s/s3-controller/pulls", r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, "aws-controllers-k8s:autopilot/s3/sdk-bump", q.Get("head"))
		assert.Equal(t, "all", q.Get("state"))
		assert.Equal(t, "main", q.Get("base"))
		io.WriteString(w, `[{"number":3,"html_url":"https://github.com/aws-controllers-k8s/s3-controller/pull/3","node_id":"N3",
		  "state":"closed","merged_at":"2026-11-01T00:00:00Z","draft":false,"head":{"sha":"a","ref":"autopilot/s3/sdk-bump"},
		  "base":{"ref":"main"},"user":{"login":"ack-bot"},"title":"t3","body":"b3","labels":[{"name":"prow/auto-gen"}]},
		 {"number":9,"html_url":"u9","state":"open","draft":true,"head":{"sha":"b","ref":"autopilot/s3/sdk-bump"},
		  "base":{"ref":"main"},"user":{"login":"ack-bot"}}]`)
	})
	prs, err := f.PRsForBranch(context.Background(), testRepo, Branch(svc))
	require.NoError(t, err)
	require.Len(t, prs, 2)
	assert.Equal(t, 9, prs[0].Number, "newest first")
	assert.Equal(t, PR{Number: 3, URL: "https://github.com/aws-controllers-k8s/s3-controller/pull/3", NodeID: "N3",
		State: "closed", Merged: true, HeadSHA: "a", HeadRef: "autopilot/s3/sdk-bump", BaseRef: "main",
		Author: "ack-bot", Title: "t3", Body: "b3", Labels: []string{"prow/auto-gen"}}, prs[1])
	assert.True(t, f.Writes())
}

func TestGitHubForgeCreatePRGuardsHeadAndBase(t *testing.T) {
	called := false
	f := testForge(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, true, body["draft"])
		assert.Equal(t, "autopilot/s3/sdk-bump", body["head"])
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"number":4,"html_url":"u4","state":"open","draft":true,"head":{"ref":"autopilot/s3/sdk-bump"},"base":{"ref":"main"},"user":{"login":"ack-bot"}}`)
	})
	ctx := context.Background()
	for _, bad := range []NewPR{{Head: "main", Base: "main"}, {Head: Branch(svc), Base: "release-1"}, {Head: "autopilot/s3/other", Base: "main"}} {
		_, err := f.CreatePR(ctx, testRepo, bad)
		assert.Error(t, err)
	}
	assert.False(t, called, "a refused PR makes no request")
	p, err := f.CreatePR(ctx, testRepo, NewPR{Title: "t", Head: Branch(svc), Base: "main", Draft: true})
	require.NoError(t, err)
	assert.Equal(t, 4, p.Number)
}

func TestGitHubForgeSetDraftUsesGraphQL(t *testing.T) {
	f := testForge(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/aws-controllers-k8s/s3-controller/pulls/7":
			io.WriteString(w, `{"number":7,"node_id":"PR_N7"}`)
		case "/graphql":
			var q struct {
				Query     string         `json:"query"`
				Variables map[string]any `json:"variables"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&q))
			assert.Contains(t, q.Query, "markPullRequestReadyForReview")
			assert.Equal(t, "PR_N7", q.Variables["id"])
			io.WriteString(w, `{"data":{}}`)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	})
	require.NoError(t, f.SetDraft(context.Background(), testRepo, 7, false))
}

func TestGitHubForgeGraphQLErrorsAreErrors(t *testing.T) {
	f := testForge(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/graphql" {
			io.WriteString(w, `{"errors":[{"message":"not allowed"}]}`)
			return
		}
		io.WriteString(w, `{"number":7,"node_id":"PR_N7"}`)
	})
	assert.ErrorContains(t, f.SetDraft(context.Background(), testRepo, 7, true), "not allowed")
}

func TestGitHubForgeStatuses(t *testing.T) {
	f := testForge(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/aws-controllers-k8s/s3-controller/statuses/"+sha:
			var s map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&s))
			assert.Equal(t, ContextServerDefault, s["context"])
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{}`)
		case r.URL.Path == "/repos/aws-controllers-k8s/s3-controller/commits/"+sha+"/status":
			io.WriteString(w, `{"statuses":[{"context":"autopilot/gate-verdict","state":"success"}]}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	ctx := context.Background()
	require.NoError(t, f.SetStatus(ctx, testRepo, sha, CommitStatus{Context: ContextServerDefault, State: "failure", Description: "d"}))
	st, err := f.GetStatus(ctx, testRepo, sha, ContextGate)
	require.NoError(t, err)
	assert.Equal(t, "success", st)
	st, err = f.GetStatus(ctx, testRepo, sha, "other")
	require.NoError(t, err)
	assert.Equal(t, "", st)
}
