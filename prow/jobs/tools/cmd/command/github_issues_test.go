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

// emptySearchPage is a complete, well-formed search response with nothing in it —
// what a pass a fixture has nothing to say about should answer.
const emptySearchPage = `{"total_count": 0, "incomplete_results": false, "items": []}`

// issueSearchHandler serves the two searches listAPIChangeIssues makes, keyed on
// the is:open / is:closed qualifier in the query.
//
// Fixtures must distinguish them, but not for the reason it first looks. An open
// issue shown to the closed pass is *not* recorded as a closed fingerprint: the
// state check inside attribute already declines it, and a handler serving one
// open-state page to both passes yields openCount=1 with an empty closed set. The
// production guard is what protects that, not this fixture — do not weaken the state
// check on the strength of these tests routing by query.
//
// The narrower reason: such a handler injects a spurious state-mismatch warning into
// every pass it does not belong to, which breaks the exact `require.Len(warnings, N)`
// assertions the window and attribution tests rely on to prove no *other* warning
// fired.
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
			// t.Fatalf cannot stop the test from the server's goroutine, and this
			// should read as "the handler got a query it does not recognise"
			// rather than as whichever assertion trips next.
			t.Errorf("search query names neither is:open nor is:closed: %q", query)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}
}

func TestListAPIChangeIssuesSkipsUnfingerprintedIssues(t *testing.T) {
	// The failure this guards: `kind/api-change` and `service/s3` are both
	// addedBy:anyone, so a maintainer triaging a contributor's issue used to make
	// it a candidate for wholesale body replacement. A human issue has no
	// fingerprint marker.
	body, _ := renderIssueBody("s3", "v1.41.5", "v1.44.0", sampleFindings())
	client := newTestGitHubClient(t, issueSearchHandler(t, fmt.Sprintf(
		`{"total_count": 2, "incomplete_results": false, "items": [
			{"number": 7,  "state": "open", "body": "please support field X", "labels": [{"name": "service/s3"}]},
			{"number": 42, "state": "open", "body": %q, "labels": [{"name": "service/s3"}]}
		]}`, body), emptySearchPage))

	openByService, openCount, closed, warnings, err := listAPIChangeIssues(
		context.Background(), client, "o", "community", "ack-bot", testKnownServices("s3"))
	require.NoError(t, err)
	assert.Equal(t, 1, openCount, "the human-filed issue must not be counted against the cap")
	assert.Empty(t, closed)
	require.Contains(t, openByService, "s3")
	assert.Equal(t, 42, openByService["s3"].GetNumber())

	// Skipping it is right, but silently skipping it is not: the same shape occurs
	// when the bot's own issue has an unreadable body, and that orphan then gets a
	// duplicate filed against it and never counts towards the cap. A human has to
	// see it.
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "#7")
	assert.Contains(t, warnings[0], "no readable fingerprint")
}

func TestListAPIChangeIssuesSkipsSearchIndexLagQuietly(t *testing.T) {
	// GitHub's search index lags writes by seconds. Observed live: closing an issue
	// and running straight away returned it from the is:open search reporting
	// "closed". The issue must still be skipped — the search result is stale — but
	// flagging it "needs a human" is a false alarm that clears on the next run.
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
	// Observed live: sns dropped out of the run's service list and its closed
	// issue warned "needs a human" on every run with nothing for anyone to do. A
	// closed issue only feeds suppression for a service this run checks. The open
	// pass must keep warning — an open issue for a dropped service needs closing.
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
	// `GetState() != "open"` read an unexpected or empty state as closed, which
	// suppresses filing *and* skips updating — the service goes quiet with no log
	// line. GitHub documents only open and closed, so this is polarity rather than a
	// reachable bug, but an unattended writer should surface a surprise, not obey it.
	// Both passes are checked. Testing only the open one leaves the original
	// polarity — "anything that is not open is closed" — passing, which is the
	// reading that suppresses filing.
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
	// Two searches with different reliability requirements: the open one gates the
	// cap and must be exact, the closed one only improves suppression. One query
	// doing both is what let the cap be measured against a window of ancient closed
	// issues while every open issue sat outside it.
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

	// Oldest first on the open pass is the only reason the duplicate resolution is
	// stable; under the default best-match sort a different issue can win on a
	// different day. Newest first on the closed pass keeps the most recently closed
	// fingerprints inside the window when there are more than it can hold.
	assert.Equal(t, []string{"created", "created"}, sorts)
	assert.Equal(t, []string{"asc", "desc"}, orders)
}

func TestListAPIChangeIssuesRefusesAnOpenCountPastTheSearchWindow(t *testing.T) {
	// The search window is the first 1,000 results, and total_count stays accurate
	// past it. An open count that large means the cap has already failed, so paging
	// what can be paged and filing against an undercount is the worst response
	// available — and the Order: asc window would hold the oldest issues anyway.
	client := newTestGitHubClient(t, issueSearchHandler(t,
		`{"total_count": 1001, "incomplete_results": false, "items": []}`, emptySearchPage))

	_, _, _, _, err := listAPIChangeIssues(
		context.Background(), client, "o", "community", "ack-bot", testKnownServices("s3"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1001")
	assert.Contains(t, err.Error(), "search window")
}

func TestListAPIChangeIssuesOnlyWarnsOnClosedIssuesPastTheSearchWindow(t *testing.T) {
	// Closed issues accumulate for the life of the job with nothing bounding them,
	// so this pass can legitimately overrun the window. Failing here would stop the
	// job outright over lost suppression, which is a bad day rather than a broken
	// repo — and refusing to run is itself how the service goes quiet.
	body, _ := renderIssueBody("s3", "v1.41.5", "v1.44.0", sampleFindings())
	client := newTestGitHubClient(t, issueSearchHandler(t, emptySearchPage, fmt.Sprintf(
		`{"total_count": 4096, "incomplete_results": false, "items": [
			{"number": 42, "state": "closed", "body": %q, "labels": [{"name": "service/s3"}]}
		]}`, body)))

	_, _, closed, warnings, err := listAPIChangeIssues(
		context.Background(), client, "o", "community", "ack-bot", testKnownServices("s3"))
	require.NoError(t, err, "an overrun of the closed window must not stop the run")
	// The page it did get is still used, so suppression degrades rather than
	// disappearing.
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
	// The oldest wins — see the sort assertions above — and the duplicate is
	// surfaced rather than silently ignored.
	assert.Equal(t, 42, openByService["s3"].GetNumber())
	// openCount counts open issues, duplicates included: it bounds how many issues
	// of the detector's are open, not how many services are covered.
	assert.Equal(t, 2, openCount)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "#58")
}

func TestListAPIChangeIssuesRequiresUnambiguousService(t *testing.T) {
	// `service/s3` and `service/s3control` both exist and are both addedBy:anyone,
	// and response label order is not guaranteed. Taking the first `service/` label
	// meant a bot issue for s3 that a maintainer also tagged `service/s3control`
	// could file a duplicate s3 issue and overwrite the s3 report with the
	// s3control one.
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
	// Each is still an open issue this job filed, so each holds a slot: a service
	// dropped from api_notification_services with its issue open used to free one,
	// letting the run file past the cap.
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
	// Closing is the only way a maintainer can say "I have seen this finding set
	// and it needs no issue". While the search was scoped is:open that said
	// nothing, and the next run re-filed the same fingerprint — daily, for ever.
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
	// A closed issue is not open, so it neither fills the cap nor counts as a
	// duplicate of anything.
	assert.Equal(t, 0, openCount)
	// This is the lookup reconcileIssue makes to decide issueSuppressedByClosed.
	assert.True(t, closed["s3"][fingerprint])
	assert.False(t, closed["s3"]["0000000000000000000000000000000000000000000000000000000000000000"],
		"a genuinely new finding set must still file, or closing one issue silences the service")
}

func TestListAPIChangeIssuesRefusesIncompleteResults(t *testing.T) {
	// total_count and the items are both partial when the search times out. On the
	// open pass that gates the cap; on the closed pass it drops suppression, which is
	// indistinguishable from "nothing was ever closed" — so both passes refuse, and
	// unlike the search-window overrun this is not a degradation to warn about.
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
	// Suppression degrades gracefully when the window overruns, but within the
	// window it must be complete: a closed fingerprint on page two that never got
	// read means the finding set a maintainer dismissed is re-filed tomorrow.
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
	// Pagination must not be tied to having kept anything: a first page of nothing
	// but human-filed issues would otherwise end the listing, and every service on
	// the later pages would look uncovered and be re-filed.
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
	assert.Equal(t, 1, openCount)
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
			// t.Fatalf cannot stop the test from the server's goroutine, and
			// failing here should read as "the handler got an unexpected
			// request" rather than as whichever assertion happens to trip next.
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
	// The safety property: a maintainer's title, labels and assignees on the
	// issue must survive a body refresh. Nothing pinned this.
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
	// Taking the address of a nil slice sends `"labels": null`, which asks the API
	// to interpret an absent value rather than saying "no labels".
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
	// GitHub silently drops `labels` on creation when the token's account lacks
	// push access. An unlabelled issue is invisible to every later search, so the
	// job would file one per service per day while openCount stayed at zero and the
	// cap never intervened.
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
	// scan_controllers_cve.go files with its own labels and none of them is the
	// detector's, so the check must not fire for it.
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"number": 99}`)
	}))

	issue, err := createGithubIssueWithClient(context.Background(), client,
		"o", "community", "t", "b", []string{"kind/security"})
	require.NoError(t, err)
	assert.Equal(t, 99, issue.GetNumber())
}

func TestGithubHelpersReportAPIErrors(t *testing.T) {
	// Every error path was untested. An unattended job's only output is its log.
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
	// %w rather than %s, so Task 14 can back off on a rate limit instead of
	// treating it like a 404.
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
	// An empty login would build the query `author:`, which GitHub answers with
	// somebody else's issues rather than an error.
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
