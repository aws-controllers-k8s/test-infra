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

package prs

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-github/v63/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dispatcher"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/sdkbump"
)

func client(t *testing.T, h http.Handler) *github.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := github.NewClient(srv.Client())
	u, err := url.Parse(srv.URL + "/")
	require.NoError(t, err)
	c.BaseURL = u
	return c
}

func TestReaderPR(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/s3-controller/pulls/7", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"number":7,"state":"closed","draft":false,"user":{"login":"ack-bot"},
			"head":{"sha":"h","ref":"autopilot/s3/sdk-bump"},"base":{"sha":"b"},"body":"x\n\nGate-Verdict: sha256:1",
			"labels":[{"name":"autopilot/blocked-server-default"}],"created_at":"2026-11-12T08:00:00+02:00",
			"merged_at":"2026-11-12T09:00:00Z","closed_at":"2026-11-12T09:00:00Z","merge_commit_sha":"m"}`)
	})
	got, err := (&Reader{Client: client(t, mux)}).PR(context.Background(), "o/s3-controller", 7)
	require.NoError(t, err)
	assert.Equal(t, dispatcher.PR{Number: 7, State: "closed", Merged: true, Author: "ack-bot", HeadSHA: "h",
		HeadRef: "autopilot/s3/sdk-bump", BaseSHA: "b", Labels: []string{"autopilot/blocked-server-default"},
		Body: "x\n\nGate-Verdict: sha256:1", CreatedAt: "2026-11-12T06:00:00Z", MergedAt: "2026-11-12T09:00:00Z",
		ClosedAt: "2026-11-12T09:00:00Z", MergeSHA: "m"}, got)
}

func TestReaderKeepsTheEndOfALongBody(t *testing.T) {
	body := strings.Repeat("a", maxBodyBytes) + "\n\nGate-Verdict: sha256:1"
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/pulls/1", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"number":1,"state":"open","body":%q}`, body)
	})
	got, err := (&Reader{Client: client(t, mux)}).PR(context.Background(), "o/r", 1)
	require.NoError(t, err)
	assert.Len(t, got.Body, maxBodyBytes)
	assert.True(t, strings.HasSuffix(got.Body, "Gate-Verdict: sha256:1"))
	assert.Empty(t, got.MergeSHA)
}

func TestReaderCommitsAndReviewsPage(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/pulls/1/commits", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, `[{"sha":"c2","commit":{"committer":{"date":"2026-11-12T09:00:00Z"}}}]`)
			return
		}
		w.Header().Set("Link", `<`+"http://"+r.Host+r.URL.Path+`?page=2>; rel="next"`)
		fmt.Fprint(w, `[{"sha":"c1","author":{"login":"ack-agent"},"commit":{"committer":{"date":"2026-11-12T08:00:00Z"}}}]`)
	})
	mux.HandleFunc("/repos/o/r/pulls/1/reviews", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[{"id":5,"user":{"login":"alice"},"state":"CHANGES_REQUESTED","submitted_at":"2026-11-12T08:30:00Z"}]`)
	})
	r := &Reader{Client: client(t, mux)}
	cs, err := r.Commits(context.Background(), "o/r", 1)
	require.NoError(t, err)
	assert.Equal(t, []dispatcher.PRCommit{
		{SHA: "c1", Author: "ack-agent", Date: "2026-11-12T08:00:00Z"},
		{SHA: "c2", Date: "2026-11-12T09:00:00Z"},
	}, cs)
	rs, err := r.Reviews(context.Background(), "o/r", 1)
	require.NoError(t, err)
	assert.Equal(t, []dispatcher.PRReview{{ID: 5, Author: "alice", State: "CHANGES_REQUESTED", SubmittedAt: "2026-11-12T08:30:00Z"}}, rs)
}

func TestReaderReviewsFailClosedPastTheBound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/pulls/1/reviews", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `<`+"http://"+r.Host+r.URL.Path+`?page=9>; rel="next"`)
		fmt.Fprint(w, `[]`)
	})
	_, err := (&Reader{Client: client(t, mux)}).Reviews(context.Background(), "o/r", 1)
	assert.ErrorContains(t, err, "more than")
}

func TestReaderTreeAndAncestry(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/git/commits/h", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"sha":"h","tree":{"sha":"t"}}`)
	})
	status := "ahead"
	mux.HandleFunc("/repos/o/r/compare/b...h", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"status":%q}`, status)
	})
	r := &Reader{Client: client(t, mux)}
	tree, err := r.TreeSHA(context.Background(), "o/r", "h")
	require.NoError(t, err)
	assert.Equal(t, "t", tree)
	for s, want := range map[string]bool{"ahead": true, "identical": true, "behind": false, "diverged": false} {
		status = s
		got, err := r.IsAncestor(context.Background(), "o/r", "b", "h")
		require.NoError(t, err)
		assert.Equal(t, want, got, s)
	}
}

// TestReaderUnknownCommitsAreAnswers (Plan 9 review F4): a base or commit
// GitHub does not know (404, 422) is "not an ancestor" and "no tree", so the
// gate env-retries instead of stalling; other errors stay errors.
func TestReaderUnknownCommitsAreAnswers(t *testing.T) {
	code := http.StatusNotFound
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/git/commits/h", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		fmt.Fprint(w, `{"message":"No commit found"}`)
	})
	mux.HandleFunc("/repos/o/r/compare/b...h", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		fmt.Fprint(w, `{"message":"No common ancestor"}`)
	})
	r := &Reader{Client: client(t, mux)}
	for _, c := range []int{http.StatusNotFound, http.StatusUnprocessableEntity} {
		code = c
		tree, err := r.TreeSHA(context.Background(), "o/r", "h")
		require.NoError(t, err, c)
		assert.Empty(t, tree, c)
		ok, err := r.IsAncestor(context.Background(), "o/r", "b", "h")
		require.NoError(t, err, c)
		assert.False(t, ok, c)
	}
	code = http.StatusBadGateway
	_, err := r.TreeSHA(context.Background(), "o/r", "h")
	assert.Error(t, err)
	_, err = r.IsAncestor(context.Background(), "o/r", "b", "h")
	assert.Error(t, err)
}

func TestReaderRejectsABadRepo(t *testing.T) {
	_, err := (&Reader{Client: github.NewClient(nil)}).PR(context.Background(), "nope", 1)
	assert.ErrorContains(t, err, "not <owner>/<name>")
}

func TestWriterMarksReady(t *testing.T) {
	var query string
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/pulls/3", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"number":3,"node_id":"PR_x"}`)
	})
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		query = string(b)
		fmt.Fprint(w, `{"data":{}}`)
	})
	w := &Writer{Forge: &sdkbump.GitHubForge{Client: client(t, mux)}}
	require.NoError(t, w.MarkReady(context.Background(), "o/r", 3))
	assert.Contains(t, query, "markPullRequestReadyForReview")
}
