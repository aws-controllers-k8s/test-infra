package issuefinder

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/go-github/v63/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestClient(t *testing.T, h http.Handler) *github.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	client := github.NewClient(nil)
	base, err := url.Parse(srv.URL + "/")
	require.NoError(t, err)
	client.BaseURL = base
	return client
}

func TestFetchIssuesAndTeam(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/issues", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "all", r.URL.Query().Get("state"))
		assert.Equal(t, apiChangeLabel, r.URL.Query().Get("labels"))
		fmt.Fprint(w, `[
		  {"number":27,"html_url":"https://x/27","state":"open","user":{"login":"ack-bot"},
		   "labels":[{"name":"service/s3"}],"body":"b"},
		  {"number":28,"state":"open","pull_request":{"url":"u"}},
		  {"number":29,"html_url":"https://x/29","state":"closed","user":{"login":"ack-bot"},"body":"c"}]`)
	})
	mux.HandleFunc("/repos/o/r/issues/27/comments", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"user":{"login":"maintainer"},"body":"/autopilot wontfix sha256:0123456789abcdef"}]`)
	})
	// No handler for issue 29's comments: closed issues must not be asked for them.
	mux.HandleFunc("/orgs/o/teams/t/members", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		fmt.Fprint(w, `[{"login":"maintainer"}]`)
	})
	client := newTestClient(t, mux)

	issues, err := FetchIssues(context.Background(), client, "o", "r")
	require.NoError(t, err)
	require.Len(t, issues, 2)
	assert.Equal(t, Issue{
		Number: 27, URL: "https://x/27", Open: true, Author: "ack-bot",
		Labels: []string{"service/s3"}, Body: "b",
		Comments: []Comment{{Author: "maintainer", Body: "/autopilot wontfix sha256:0123456789abcdef"}},
	}, issues[0])
	assert.Equal(t, 29, issues[1].Number)
	assert.False(t, issues[1].Open)
	assert.Nil(t, issues[1].Comments)

	team, err := FetchTeam(context.Background(), client, "o", "t")
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"maintainer": true}, team)
}

// paged serves page 1 with a Link header pointing at page 2, and page 2 without.
func paged(t *testing.T, page1, page2 string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		switch p := r.URL.Query().Get("page"); p {
		case "", "1":
			q := r.URL.Query()
			q.Set("page", "2")
			w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?%s>; rel="next"`, r.Host, r.URL.Path, q.Encode()))
			fmt.Fprint(w, page1)
		case "2":
			fmt.Fprint(w, page2)
		default:
			t.Errorf("unexpected page %q", p)
		}
	}
}

func TestFetchIssuesAndTeamPaginate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/issues", paged(t,
		`[{"number":1,"state":"open","user":{"login":"b"}}]`,
		`[{"number":2,"state":"open","user":{"login":"b"}}]`))
	mux.HandleFunc("/repos/o/r/issues/1/comments", paged(t,
		`[{"user":{"login":"a"},"body":"one"}]`,
		`[{"user":{"login":"b"},"body":"two"}]`))
	mux.HandleFunc("/repos/o/r/issues/2/comments", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[]`)
	})
	mux.HandleFunc("/orgs/o/teams/t/members", paged(t, `[{"login":"a"}]`, `[{"login":"b"}]`))
	client := newTestClient(t, mux)

	issues, err := FetchIssues(context.Background(), client, "o", "r")
	require.NoError(t, err)
	require.Len(t, issues, 2)
	assert.Equal(t, 1, issues[0].Number)
	assert.Equal(t, []Comment{{Author: "a", Body: "one"}, {Author: "b", Body: "two"}}, issues[0].Comments)
	assert.Equal(t, 2, issues[1].Number)

	team, err := FetchTeam(context.Background(), client, "o", "t")
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"a": true, "b": true}, team)
}

func TestFetchErrors(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	_, err := FetchIssues(context.Background(), client, "o", "r")
	assert.Error(t, err)
	_, err = FetchTeam(context.Background(), client, "o", "t")
	assert.Error(t, err)
}

func TestFetchIssuesFailsWhenPagesShift(t *testing.T) {
	// An issue filed or relabelled mid-listing shifts the pages, so the same
	// issue shows up twice (and another may be missed).
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/issues", paged(t,
		`[{"number":1,"state":"closed","user":{"login":"b"}},{"number":2,"state":"closed","user":{"login":"b"}}]`,
		`[{"number":2,"state":"closed","user":{"login":"b"}}]`))
	client := newTestClient(t, mux)

	_, err := FetchIssues(context.Background(), client, "o", "r")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "#2")
}

func TestFetchTeamSkipsEmptyLogins(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/orgs/o/teams/t/members", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"login":""},{},{"login":"a"}]`)
	})
	client := newTestClient(t, mux)

	team, err := FetchTeam(context.Background(), client, "o", "t")
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"a": true}, team)
}
