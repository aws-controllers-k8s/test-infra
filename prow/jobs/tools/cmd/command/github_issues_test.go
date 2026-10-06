package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-github/v63/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestGitHubClient returns a client whose requests are served by handler.
func newTestGitHubClient(t *testing.T, handler http.Handler) *github.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client := github.NewClient(nil)
	url, err := client.BaseURL.Parse(server.URL + "/")
	require.NoError(t, err)
	client.BaseURL = url
	return client
}

// testKnownServices is the set of services a run configures. Attribution is
// matched against it, so anything outside it is not this run's business.
func testKnownServices(names ...string) map[string]bool {
	known := map[string]bool{}
	for _, name := range names {
		known[name] = true
	}
	return known
}

// emptySearchPage is a well-formed search response with no items.
const emptySearchPage = `{"total_count": 0, "incomplete_results": false, "items": []}`

// issueSearchHandler serves the is:open and is:closed searches separately so
// a pass never sees the other's page, which would add a state-mismatch warning
// and break the exact warning counts the tests assert.
func issueSearchHandler(t *testing.T, openPage, closedPage string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("q")
		switch {
		case strings.Contains(query, "is:open"):
			fmt.Fprint(w, openPage)
		case strings.Contains(query, "is:closed"):
			fmt.Fprint(w, closedPage)
		default:
			// t.Fatalf cannot stop the test from the server's goroutine.
			t.Errorf("search query names neither is:open nor is:closed: %q", query)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}
}

func TestListAPIChangeIssuesSkipsUnfingerprintedIssues(t *testing.T) {
	// Guards: an issue without a fingerprint marker is never managed, but still
	// holds a slot under the cap since it is open.
	body, _ := renderIssueBody("s3", "v1.41.5", "v1.44.0", sampleFindings())
	client := newTestGitHubClient(t, issueSearchHandler(t, fmt.Sprintf(
		`{"total_count": 2, "incomplete_results": false, "items": [
			{"number": 7,  "state": "open", "body": "please support field X", "labels": [{"name": "service/s3"}]},
			{"number": 42, "state": "open", "body": %q, "labels": [{"name": "service/s3"}]}
		]}`, body), emptySearchPage))

	openByService, openCount, closed, warnings, err := listAPIChangeIssues(
		context.Background(), client, "o", "community", "ack-bot", testKnownServices("s3"))
	require.NoError(t, err)
	assert.Equal(t, 2, openCount, "an open issue the search returned holds a slot, fingerprint or not")
	assert.Empty(t, closed)
	require.Contains(t, openByService, "s3")
	assert.Equal(t, 42, openByService["s3"].GetNumber())

	// An unreadable orphan must be surfaced, or a duplicate gets filed against it.
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "#7")
	assert.Contains(t, warnings[0], "no readable fingerprint")
}

func TestListAPIChangeIssuesSkipsSearchIndexLagQuietly(t *testing.T) {
	// The search index lags writes, so a just-closed issue can come back from the
	// is:open search. Skip it without warning; the next run clears it.
	body, _ := renderIssueBody("s3", "v1.41.5", "v1.44.0", sampleFindings())
	item := func(state string) string {
		return fmt.Sprintf(`{"total_count": 1, "incomplete_results": false, "items": [
			{"number": 42, "state": %q, "body": %q, "labels": [{"name": "service/s3"}]}
		]}`, state, body)
	}

	for name, handler := range map[string]http.HandlerFunc{
		"just closed":   issueSearchHandler(t, item("closed"), emptySearchPage),
		"just reopened": issueSearchHandler(t, emptySearchPage, item("open")),
	} {
		t.Run(name, func(t *testing.T) {
			openByService, openCount, closed, warnings, err := listAPIChangeIssues(
				context.Background(), newTestGitHubClient(t, handler),
				"o", "community", "ack-bot", testKnownServices("s3"))
			require.NoError(t, err)
			assert.Empty(t, openByService)
			assert.Equal(t, 0, openCount)
			assert.Empty(t, closed)
			assert.Empty(t, warnings, "index lag is not something a human needs to fix")
		})
	}
}

func TestListAPIChangeIssuesIgnoresClosedIssuesOfUncheckedServices(t *testing.T) {
	// Closed issues only matter for services this run checks; an open issue for a
	// dropped service still warns because it needs closing.
	body, _ := renderIssueBody("sns", "v1.41.5", "v1.44.0", sampleFindings())
	item := func(state string) string {
		return fmt.Sprintf(`{"total_count": 1, "incomplete_results": false, "items": [
			{"number": 2, "state": %q, "body": %q, "labels": [{"name": "service/sns"}]}
		]}`, state, body)
	}

	_, _, closed, warnings, err := listAPIChangeIssues(
		context.Background(),
		newTestGitHubClient(t, issueSearchHandler(t, emptySearchPage, item("closed"))),
		"o", "community", "ack-bot", testKnownServices("s3"))
	require.NoError(t, err)
	assert.Empty(t, closed)
	assert.Empty(t, warnings)

	_, _, _, warnings, err = listAPIChangeIssues(
		context.Background(),
		newTestGitHubClient(t, issueSearchHandler(t, item("open"), emptySearchPage)),
		"o", "community", "ack-bot", testKnownServices("s3"))
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "#2 carries no known service/ label")
}

func TestListAPIChangeIssuesWarnsOnAnUnrecognisedState(t *testing.T) {
	// Guards: an unexpected state is surfaced, not read as closed (which would
	// suppress filing silently). Checked on both passes.
	body, _ := renderIssueBody("s3", "v1.41.5", "v1.44.0", sampleFindings())
	odd := fmt.Sprintf(`{"total_count": 1, "incomplete_results": false, "items": [
		{"number": 42, "state": "merged", "body": %q, "labels": [{"name": "service/s3"}]}
	]}`, body)

	for name, handler := range map[string]http.HandlerFunc{
		"open":   issueSearchHandler(t, odd, emptySearchPage),
		"closed": issueSearchHandler(t, emptySearchPage, odd),
	} {
		t.Run(name, func(t *testing.T) {
			openByService, openCount, closed, warnings, err := listAPIChangeIssues(
				context.Background(), newTestGitHubClient(t, handler),
				"o", "community", "ack-bot", testKnownServices("s3"))
			require.NoError(t, err)
			assert.Empty(t, openByService, "an issue whose state we cannot read must not be edited")
			assert.Equal(t, 0, openCount)
			assert.Empty(t, closed,
				"an unreadable state must not be taken for a maintainer's dismissal")
			require.Len(t, warnings, 1)
			assert.Contains(t, warnings[0], "#42")
			assert.Contains(t, warnings[0], `"merged"`)
		})
	}
}

func TestListAPIChangeIssuesScopesBothQueries(t *testing.T) {
	// The open search gates the cap and must be exact; the closed one only feeds
	// suppression, so they are separate queries.
	var queries, sorts, orders []string
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query().Get("q"))
		sorts = append(sorts, r.URL.Query().Get("sort"))
		orders = append(orders, r.URL.Query().Get("order"))
		fmt.Fprint(w, emptySearchPage)
	}))

	_, _, _, _, err := listAPIChangeIssues(
		context.Background(), client, "o", "community", "ack-bot", testKnownServices("s3"))
	require.NoError(t, err)

	require.Len(t, queries, 2, "one search for open issues and one for closed")
	for _, query := range queries {
		assert.Contains(t, query, "repo:o/community")
		assert.Contains(t, query, "is:issue", "search/issues matches PRs too")
		assert.Contains(t, query, "label:ack/api-change-detected")
		assert.Contains(t, query, "author:ack-bot")
		assert.NotContains(t, query, "kind/api-change",
			"the Prow triage label must not be the ownership marker")
	}
	assert.Contains(t, queries[0], "is:open")
	assert.Contains(t, queries[1], "is:closed",
		"closed issues must be fetched, or closing one just means it is re-filed tomorrow")

	// Oldest-first on the open pass makes duplicate resolution stable; newest-first
	// on the closed pass keeps recent fingerprints inside the window.
	assert.Equal(t, []string{"created", "created"}, sorts)
	assert.Equal(t, []string{"asc", "desc"}, orders)
}

func TestListAPIChangeIssuesRefusesAnOpenCountPastTheSearchWindow(t *testing.T) {
	// An open count past the 1,000-result search window means the cap already failed.
	client := newTestGitHubClient(t, issueSearchHandler(t,
		`{"total_count": 1001, "incomplete_results": false, "items": []}`, emptySearchPage))

	_, _, _, _, err := listAPIChangeIssues(
		context.Background(), client, "o", "community", "ack-bot", testKnownServices("s3"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1001")
	assert.Contains(t, err.Error(), "search window")
}

func TestListAPIChangeIssuesOnlyWarnsOnClosedIssuesPastTheSearchWindow(t *testing.T) {
	// Closed issues are unbounded, so overrunning the window only warns: lost
	// suppression should not stop the job.
	body, _ := renderIssueBody("s3", "v1.41.5", "v1.44.0", sampleFindings())
	client := newTestGitHubClient(t, issueSearchHandler(t, emptySearchPage, fmt.Sprintf(
		`{"total_count": 4096, "incomplete_results": false, "items": [
			{"number": 42, "state": "closed", "body": %q, "labels": [{"name": "service/s3"}]}
		]}`, body)))

	_, _, closed, warnings, err := listAPIChangeIssues(
		context.Background(), client, "o", "community", "ack-bot", testKnownServices("s3"))
	require.NoError(t, err, "an overrun of the closed window must not stop the run")
	assert.Len(t, closed["s3"], 1)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "4096")
	assert.Contains(t, warnings[0], "suppression may be incomplete")
}

func TestListAPIChangeIssuesReportsDuplicates(t *testing.T) {
	body, _ := renderIssueBody("s3", "v1.41.5", "v1.44.0", sampleFindings())
	client := newTestGitHubClient(t, issueSearchHandler(t, fmt.Sprintf(
		`{"total_count": 2, "incomplete_results": false, "items": [
			{"number": 42, "state": "open", "body": %q, "labels": [{"name": "service/s3"}]},
			{"number": 58, "state": "open", "body": %q, "labels": [{"name": "service/s3"}]}
		]}`, body, body), emptySearchPage))

	openByService, openCount, _, warnings, err := listAPIChangeIssues(
		context.Background(), client, "o", "community", "ack-bot", testKnownServices("s3"))
	require.NoError(t, err)
	// The oldest wins and the duplicate is reported.
	assert.Equal(t, 42, openByService["s3"].GetNumber())
	// openCount includes duplicates.
	assert.Equal(t, 2, openCount)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "#58")
}

func TestListAPIChangeIssuesRequiresUnambiguousService(t *testing.T) {
	// Guards against s3 vs s3control mixups: response label order is not
	// guaranteed, so an issue with more than one known service label is ambiguous.
	body, _ := renderIssueBody("s3", "v1.41.5", "v1.44.0", sampleFindings())
	client := newTestGitHubClient(t, issueSearchHandler(t, fmt.Sprintf(
		`{"total_count": 4, "incomplete_results": false, "items": [
			{"number": 11, "state": "open", "body": %q, "labels": [{"name": "kind/api-change"}]},
			{"number": 12, "state": "open", "body": %q, "labels": [{"name": "service/"}]},
			{"number": 13, "state": "open", "body": %q, "labels": [{"name": "service/notaservice"}]},
			{"number": 14, "state": "open", "body": %q, "labels": [
				{"name": "service/s3control"}, {"name": "service/s3"}
			]}
		]}`, body, body, body, body), emptySearchPage))

	openByService, openCount, _, warnings, err := listAPIChangeIssues(
		context.Background(), client, "o", "community", "ack-bot",
		testKnownServices("s3", "s3control"))
	require.NoError(t, err)
	assert.Empty(t, openByService, "an issue that cannot be attributed must not be edited")
	// Each is still an open bot issue, so each holds a slot under the cap.
	assert.Equal(t, 4, openCount)
	require.Len(t, warnings, 4)
	assert.Contains(t, warnings[0], "#11")
	assert.Contains(t, warnings[0], "no known service/ label")
	assert.Contains(t, warnings[1], "#12", "a bare service/ label must not key the empty service")
	assert.Contains(t, warnings[2], "#13", "a service this run does not know about is not its business")
	assert.Contains(t, warnings[3], "#14")
	assert.Contains(t, warnings[3], "s3, s3control")
	assert.Contains(t, warnings[3], "refusing to guess")
}

func TestListAPIChangeIssuesRecordsClosedFingerprints(t *testing.T) {
	// Guards: closing an issue suppresses re-filing the same fingerprint.
	body, fingerprint := renderIssueBody("s3", "v1.41.5", "v1.44.0", sampleFindings())
	client := newTestGitHubClient(t, issueSearchHandler(t, emptySearchPage, fmt.Sprintf(
		`{"total_count": 1, "incomplete_results": false, "items": [
			{"number": 42, "state": "closed", "body": %q, "labels": [{"name": "service/s3"}]}
		]}`, body)))

	openByService, openCount, closed, warnings, err := listAPIChangeIssues(
		context.Background(), client, "o", "community", "ack-bot", testKnownServices("s3"))
	require.NoError(t, err)
	assert.Empty(t, openByService)
	assert.Empty(t, warnings)
	assert.Equal(t, 0, openCount)
	assert.True(t, closed["s3"][fingerprint])
	assert.False(t, closed["s3"]["0000000000000000000000000000000000000000000000000000000000000000"],
		"a genuinely new finding set must still file, or closing one issue silences the service")
}

func TestListAPIChangeIssuesRefusesIncompleteResults(t *testing.T) {
	// Partial results would undercount the cap or drop suppression, so both
	// passes refuse rather than warn.
	partial := `{"total_count": 2, "incomplete_results": true, "items": []}`
	for name, handler := range map[string]http.HandlerFunc{
		"open":   issueSearchHandler(t, partial, emptySearchPage),
		"closed": issueSearchHandler(t, emptySearchPage, partial),
	} {
		t.Run(name, func(t *testing.T) {
			_, _, _, _, err := listAPIChangeIssues(context.Background(),
				newTestGitHubClient(t, handler), "o", "community", "ack-bot",
				testKnownServices("s3"))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "incomplete")
		})
	}
}

// paginatedSearchHandler serves two pages to the pass named by qualifier and an
// empty response to the other, counting the requests that pass made.
func paginatedSearchHandler(t *testing.T, qualifier, page1, page2 string, pages *int) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Query().Get("q"), qualifier) {
			fmt.Fprint(w, emptySearchPage)
			return
		}
		*pages++
		if r.URL.Query().Get("page") == "" {
			w.Header().Set("Link", fmt.Sprintf(`<%s/search/issues?page=2>; rel="next"`, "http://"+r.Host))
			fmt.Fprint(w, page1)
			return
		}
		fmt.Fprint(w, page2)
	}
}

func TestListAPIChangeIssuesPaginates(t *testing.T) {
	body, _ := renderIssueBody("s3", "v1.41.5", "v1.44.0", sampleFindings())
	var pages int
	client := newTestGitHubClient(t, paginatedSearchHandler(t, "is:open",
		fmt.Sprintf(`{"total_count": 2, "incomplete_results": false, "items": [
			{"number": 42, "state": "open", "body": %q, "labels": [{"name": "service/s3"}]}
		]}`, body),
		fmt.Sprintf(`{"total_count": 2, "incomplete_results": false, "items": [
			{"number": 43, "state": "open", "body": %q, "labels": [{"name": "service/ec2"}]}
		]}`, body),
		&pages))

	openByService, openCount, _, _, err := listAPIChangeIssues(
		context.Background(), client, "o", "community", "ack-bot", testKnownServices("s3", "ec2"))
	require.NoError(t, err)
	assert.Equal(t, 2, pages, "a second page must be followed")
	assert.Equal(t, 2, openCount)
	assert.Contains(t, openByService, "s3")
	assert.Contains(t, openByService, "ec2")
}

func TestListAPIChangeIssuesPaginatesTheClosedPassToo(t *testing.T) {
	// Within the window, suppression must read every page.
	body, fingerprint := renderIssueBody("ec2", "v1.41.5", "v1.44.0", sampleFindings())
	var pages int
	client := newTestGitHubClient(t, paginatedSearchHandler(t, "is:closed",
		`{"total_count": 2, "incomplete_results": false, "items": [
			{"number": 7, "state": "closed", "body": "please support field X", "labels": [{"name": "service/s3"}]}
		]}`,
		fmt.Sprintf(`{"total_count": 2, "incomplete_results": false, "items": [
			{"number": 43, "state": "closed", "body": %q, "labels": [{"name": "service/ec2"}]}
		]}`, body),
		&pages))

	_, _, closed, _, err := listAPIChangeIssues(
		context.Background(), client, "o", "community", "ack-bot", testKnownServices("s3", "ec2"))
	require.NoError(t, err)
	assert.Equal(t, 2, pages, "a second page must be followed")
	assert.True(t, closed["ec2"][fingerprint])
}

func TestListAPIChangeIssuesPaginatesPastAFullyFilteredPage(t *testing.T) {
	// A page with nothing manageable must not end pagination, or later services
	// look uncovered and get re-filed.
	body, _ := renderIssueBody("s3", "v1.41.5", "v1.44.0", sampleFindings())
	var pages int
	client := newTestGitHubClient(t, paginatedSearchHandler(t, "is:open",
		`{"total_count": 2, "incomplete_results": false, "items": [
			{"number": 7, "state": "open", "body": "please support field X", "labels": [{"name": "service/s3"}]}
		]}`,
		fmt.Sprintf(`{"total_count": 2, "incomplete_results": false, "items": [
			{"number": 42, "state": "open", "body": %q, "labels": [{"name": "service/s3"}]}
		]}`, body),
		&pages))

	openByService, openCount, _, _, err := listAPIChangeIssues(
		context.Background(), client, "o", "community", "ack-bot", testKnownServices("s3"))
	require.NoError(t, err)
	assert.Equal(t, 2, pages)
	assert.Equal(t, 2, openCount)
	require.Contains(t, openByService, "s3")
	assert.Equal(t, 42, openByService["s3"].GetNumber())
}

func TestUpdateAndCommentIssue(t *testing.T) {
	var patched, commented bool
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/o/r/issues/42":
			patched = true
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues/42/comments":
			commented = true
		default:
			// t.Fatalf cannot stop the test from the server's goroutine.
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))

	require.NoError(t, updateGithubIssueBody(context.Background(), client, "o", "r", 42, "new body"))
	require.NoError(t, commentOnGithubIssue(context.Background(), client, "o", "r", 42, "a comment"))
	assert.True(t, patched)
	assert.True(t, commented)
}

func TestUpdateGithubIssueBodySendsOnlyBody(t *testing.T) {
	// Guards: a body refresh leaves the maintainer's title, labels and assignees alone.
	var payload map[string]any
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		fmt.Fprint(w, `{"number": 42}`)
	}))

	require.NoError(t, updateGithubIssueBody(
		context.Background(), client, "o", "community", 42, "new body"))
	assert.Equal(t, map[string]any{"body": "new body"}, payload)
}

func TestCreateGithubIssueOmitsLabelsWhenNone(t *testing.T) {
	// A nil slice must not be sent as `"labels": null`.
	var payload map[string]any
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		fmt.Fprint(w, `{"number": 42}`)
	}))

	_, err := createGithubIssueWithClient(
		context.Background(), client, "o", "community", "t", "b", nil)
	require.NoError(t, err)
	assert.NotContains(t, payload, "labels")
	assert.Equal(t, map[string]any{"title": "t", "body": "b"}, payload)
}

func TestCreateGithubIssueRequiresOwnershipLabelToStick(t *testing.T) {
	// GitHub drops labels on create without push access; an unlabelled issue is
	// invisible to later searches, so the job would re-file it daily.
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"number": 99, "labels": [{"name": "service/s3"}]}`)
	}))

	issue, err := createGithubIssueWithClient(context.Background(), client,
		"o", "community", "t", "b", []string{apiChangeLabel, "service/s3"})
	assert.Nil(t, issue)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "#99")
	assert.Contains(t, err.Error(), apiChangeLabel)
	assert.Contains(t, err.Error(), "push access")
}

func TestCreateGithubIssueAcceptsLabelsThatStuck(t *testing.T) {
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"number": 99, "labels": [{"name": %q}, {"name": "service/s3"}]}`, apiChangeLabel)
	}))

	issue, err := createGithubIssueWithClient(context.Background(), client,
		"o", "community", "t", "b", []string{apiChangeLabel, "service/s3"})
	require.NoError(t, err)
	assert.Equal(t, 99, issue.GetNumber())
}

func TestCreateGithubIssueDoesNotRequireLabelsItWasNotAskedFor(t *testing.T) {
	// Other callers (scan_controllers_cve.go) use their own labels; the check must not fire.
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"number": 99}`)
	}))

	issue, err := createGithubIssueWithClient(context.Background(), client,
		"o", "community", "t", "b", []string{"kind/security"})
	require.NoError(t, err)
	assert.Equal(t, 99, issue.GetNumber())
}

func TestGithubHelpersReportAPIErrors(t *testing.T) {
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message": "Not Found"}`)
	}))
	ctx := context.Background()

	_, _, _, _, err := listAPIChangeIssues(ctx, client, "o", "community", "ack-bot",
		testKnownServices("s3"))
	assert.ErrorContains(t, err, "404")

	_, err = githubLogin(ctx, client)
	assert.ErrorContains(t, err, "404")

	assert.ErrorContains(t, updateGithubIssueBody(ctx, client, "o", "community", 42, "b"), "404")
	assert.ErrorContains(t, commentOnGithubIssue(ctx, client, "o", "community", 42, "b"), "404")

	_, err = createGithubIssueWithClient(ctx, client, "o", "community", "t", "b", nil)
	assert.ErrorContains(t, err, "404")
}

func TestGithubHelpersSurfaceRateLimitErrors(t *testing.T) {
	// Errors are wrapped with %w so callers can detect rate limits.
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "30")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1750000000")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message": "API rate limit exceeded", "documentation_url": "https://docs.github.com/"}`)
	}))

	_, _, _, _, err := listAPIChangeIssues(
		context.Background(), client, "o", "community", "ack-bot", testKnownServices("s3"))
	require.Error(t, err)
	var rateLimit *github.RateLimitError
	assert.True(t, errors.As(err, &rateLimit),
		"the wrapped error must stay inspectable, or a rate limit is indistinguishable from a real failure")
}

func TestGithubLogin(t *testing.T) {
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/user", r.URL.Path)
		fmt.Fprint(w, `{"login": "ack-bot"}`)
	}))
	login, err := githubLogin(context.Background(), client)
	require.NoError(t, err)
	assert.Equal(t, "ack-bot", login)
}

func TestGithubLoginRejectsAnEmptyLogin(t *testing.T) {
	// An empty login would build `author:`, which matches other users' issues.
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"login": ""}`)
	}))
	_, err := githubLogin(context.Background(), client)
	assert.ErrorContains(t, err, "no login")
}

func TestNewGithubClientFromEnv(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	_, err := newGithubClientFromEnv()
	assert.ErrorContains(t, err, "GITHUB_TOKEN")

	t.Setenv("GITHUB_TOKEN", "t0ken")
	client, err := newGithubClientFromEnv()
	require.NoError(t, err)
	assert.NotNil(t, client)
}

func TestCreateGithubIssueClassifiesFailures(t *testing.T) {
	// Only a 4xx proves nothing was filed; a 5xx or dropped connection may follow a committed create.
	for name, tc := range map[string]struct {
		handler       http.HandlerFunc
		indeterminate bool
	}{
		"422": {func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"message": "Validation Failed"}`)
		}, false},
		"403": {func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message": "Forbidden"}`)
		}, false},
		"502": {func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprint(w, `{"message": "Bad Gateway"}`)
		}, true},
		"connection dropped": {func(w http.ResponseWriter, r *http.Request) {
			panic(http.ErrAbortHandler)
		}, true},
	} {
		t.Run(name, func(t *testing.T) {
			client := newTestGitHubClient(t, tc.handler)
			_, err := createGithubIssueWithClient(context.Background(), client, "o", "community",
				"t", "b", []string{apiChangeLabel})
			require.Error(t, err)
			assert.Equal(t, tc.indeterminate, errors.Is(err, errIssueCreateIndeterminate), "%v", err)
		})
	}
}

func TestRefetchManagedIssueReportsAFailedRead(t *testing.T) {
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message": "Not Found"}`)
	}))
	listed := &github.Issue{Number: github.Int(42), User: &github.User{Login: github.String("ack-bot")}}
	_, err := refetchManagedIssue(context.Background(), client, "o", "community", "s3", listed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "#42")
}
