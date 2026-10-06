package command

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// operationFindings is a release that only adds operations: one on a resource the
// controller already has, one that does not classify.
func operationFindings() []Finding {
	return []Finding{
		{Kind: "Widget", Class: ClassNewOperation, Subject: "PutWidgetPolicy", NewSincePin: true},
		{Class: ClassUnknownOperation, Subject: "ResetWidget", NewSincePin: true},
	}
}

func TestReconcileIssueFilesNothingForOperationsAlone(t *testing.T) {
	// Operation-only releases neither file an issue nor use a cap slot. The nil client
	// panics on any request.
	outcome, err := reconcileIssue(context.Background(), nil, "o", "community", "demo",
		"v1.41.5", "v1.44.0", operationFindings(), nil, nil, 1, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueUnchanged, outcome)
}

func TestReconcileIssueOperationsAloneLeaveAnOpenIssueStale(t *testing.T) {
	// An open issue left with only operation findings is reported stale and not written.
	filed, _ := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	outcome, err := reconcileIssue(context.Background(), nil, "o", "community", "demo",
		"v1.41.5", "v1.44.0", operationFindings(), issueFor(42, filed), nil, 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueStaleOpenIssue, outcome)
}

func TestFingerprintIgnoresOperations(t *testing.T) {
	// A new operation alongside an unchanged resource and field set is not a new
	// finding set: it must not re-file an issue a maintainer closed.
	_, closed := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	withOp := append(sampleFindings(),
		Finding{Kind: "Widget", Class: ClassNewOperation, Subject: "TagWidget", NewSincePin: true})
	_, got := renderIssueBody("demo", "v1.41.5", "v1.44.0", withOp)
	assert.Equal(t, closed, got)

	outcome, err := reconcileIssue(context.Background(), nil, "o", "community", "demo",
		"v1.41.5", "v1.44.0", withOp, nil, map[string]bool{closed: true}, 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueSuppressedByClosed, outcome)
}

func TestReconcileIssueNewOperationRewordsSilently(t *testing.T) {
	// On an open issue the new operation still reaches the body, through the silent
	// same-fingerprint rewrite: no comment notifies subscribers about it.
	filed, _ := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	withOp := append(sampleFindings(),
		Finding{Kind: "Widget", Class: ClassNewOperation, Subject: "TagWidget", NewSincePin: true})

	var patched bool
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == "/repos/o/community/issues/42" {
			patched = true
			fmt.Fprint(w, `{}`)
			return
		}
		t.Errorf("only a silent PATCH expected, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))

	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", withOp, issueFor(42, filed), nil, 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueUpdated, outcome)
	assert.True(t, patched)
}

// withDescribeGizmo is sampleFindings after a release adds DescribeGizmo: it is
// folded into the resource's Evidence, also returns an existing field, and is
// listed as a new operation.
func withDescribeGizmo() []Finding {
	findings := sampleFindings()
	findings[0].Evidence = "CreateGizmo,DeleteGizmo,DescribeGizmo"
	findings[2].Evidence = "CreateWidget,DescribeGizmo"
	return append(findings,
		Finding{Kind: "Gizmo", Class: ClassNewOperation, Subject: "DescribeGizmo", NewSincePin: true})
}

func TestFingerprintIgnoresOperationEvidence(t *testing.T) {
	// The operation reaches resource and field Evidence too, which must not re-file
	// a closed issue either.
	_, closed := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	_, got := renderIssueBody("demo", "v1.41.5", "v1.44.0", withDescribeGizmo())
	assert.Equal(t, closed, got)

	outcome, err := reconcileIssue(context.Background(), nil, "o", "community", "demo",
		"v1.41.5", "v1.44.0", withDescribeGizmo(), nil, map[string]bool{closed: true}, 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueSuppressedByClosed, outcome)
}

func TestReconcileIssueEvidenceGrowthRewordsSilently(t *testing.T) {
	// The grown Evidence still reaches an open issue's body, without a comment.
	filed, _ := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())

	var patchedBody string
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == "/repos/o/community/issues/42" {
			var payload struct{ Body string }
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			patchedBody = payload.Body
			fmt.Fprint(w, `{}`)
			return
		}
		t.Errorf("only a silent PATCH expected, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))

	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", withDescribeGizmo(), issueFor(42, filed), nil, 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueUpdated, outcome)
	assert.Contains(t, patchedBody, "DescribeGizmo")
}

func TestReconcileServicesTreatsOperationsAloneAsNothingToFile(t *testing.T) {
	logged := captureLog(t)
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("nothing to file, so nothing may be written, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	analyze := func(_ context.Context, service string) ([]Finding, string, string, error) {
		return operationFindings(), "v1.41.5", "v1.44.0", nil
	}
	outputDir := t.TempDir()

	_, _, _, err := reconcileServices(context.Background(), client, "o", "community",
		[]string{"svc1"}, nil, nil, 1, 0, analyze, true, outputDir)
	require.NoError(t, err)
	assert.Contains(t, logged.String(), "svc1: 2 operation finding(s) only")
	assert.NoFileExists(t, filepath.Join(outputDir, "svc1.md"), "no issue, so no preview of one")
}

// manyDropped returns n dropped operations whose reasons are long enough that the
// whole list is several times GitHub's body limit.
func manyDropped(n int) []Finding {
	out := make([]Finding, 0, n)
	for i := range n {
		out = append(out, Finding{
			Class: ClassDroppedOperation, Subject: fmt.Sprintf("StartFlowCapture%05d", i),
			Detail: strings.Repeat("a reason that goes on ", 10), NewSincePin: true,
		})
	}
	return out
}

func TestRenderIssueBodyBoundsAnOversizedDroppedAppendix(t *testing.T) {
	dropped := manyDropped(2000)
	require.Greater(t, len(droppedBlock(dropped, math.MaxInt)), githubMaxIssueBody)

	_, plainFingerprint := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	body, fingerprint := renderIssueBody("demo", "v1.41.5", "v1.44.0",
		append(sampleFindings(), dropped...))

	assert.Less(t, len(body), githubMaxIssueBody)
	assert.Contains(t, body, "## Resource: Gizmo")
	assert.Contains(t, body, "- `Description` — `CreateWidget`")
	assert.Contains(t, body, "- `ResetWidget`")
	assert.Contains(t, body, "<summary>2000 new operations not reported, each with the reason</summary>")
	assert.Regexp(t, `- _and \d+ more_\n\n</details>\n\n---\n`, body)
	assert.Equal(t, plainFingerprint, fingerprint)
}

func TestRenderIssueBodyBoundsAnOversizedPreexistingAppendix(t *testing.T) {
	// maxPreexistingEntries bounds the count, not the bytes: 50 entries with long
	// evidence lists are still well over the limit.
	ops := make([]string, 200)
	for i := range ops {
		ops[i] = fmt.Sprintf("DescribeSomethingQuiteLong%03d", i)
	}
	var old []Finding
	for i := range maxPreexistingEntries {
		old = append(old, Finding{Kind: "Old", Class: ClassSpecField,
			Subject: fmt.Sprintf("Field%02d", i), Evidence: strings.Join(ops, ",")})
	}
	require.Greater(t, len(preexistingBlock(old, "v1.41.5", math.MaxInt)), githubMaxIssueBody)

	body, _ := renderIssueBody("demo", "v1.41.5", "v1.44.0", append(sampleFindings(), old...))
	assert.Less(t, len(body), githubMaxIssueBody)
	assert.Contains(t, body, "## Resource: Gizmo")
	assert.Contains(t, body, "already in v1.41.5 and missing from the controller")
}

func TestRenderIssueBodyBoundsOversizedFindingsAndAppendixTogether(t *testing.T) {
	// Both overflow: the findings truncate to leave the appendix its reserve, and the
	// appendix truncates to what is left.
	findings := manyDropped(2000)
	for i := range 3000 {
		findings = append(findings, Finding{Kind: "Widget", Class: ClassSpecField,
			Subject: fmt.Sprintf("Field%05d", i), Evidence: "CreateWidget", NewSincePin: true})
	}
	body, _ := renderIssueBody("demo", "v1.41.5", "v1.44.0", findings)
	assert.Less(t, len(body), githubMaxIssueBody)
	assert.Contains(t, body, "- `Field00000`")
	assert.Contains(t, body, "finding(s) omitted to keep this issue body within GitHub's size limit")
	assert.Contains(t, body, "new operations not reported, each with the reason")
}

func TestCollapsedListNeverExceedsItsLimit(t *testing.T) {
	head := "<details>\n<summary>s</summary>\n\n"
	entries := []string{"- one\n", "- two\n", "- three\n", "- four\n"}
	full := collapsedList(head, entries, len(entries), math.MaxInt)
	assert.Equal(t, head+strings.Join(entries, "")+"\n</details>\n\n", full)

	for limit := 0; limit <= len(full); limit++ {
		got := collapsedList(head, entries, len(entries), limit)
		assert.LessOrEqual(t, len(got), limit, "limit %d", limit)
		if got != "" {
			assert.True(t, strings.HasPrefix(got, head), "limit %d", limit)
			assert.True(t, strings.HasSuffix(got, "\n</details>\n\n"), "limit %d", limit)
		}
	}
	assert.Equal(t, "", collapsedList(head, entries, len(entries), len(head)))
	assert.Equal(t, head+"- one\n- _and 3 more_\n\n</details>\n\n", collapsedList(head, entries, 1, math.MaxInt))
}
