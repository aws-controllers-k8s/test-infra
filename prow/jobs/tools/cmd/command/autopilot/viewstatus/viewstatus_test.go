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

package viewstatus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/google/go-github/v63/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/apichanges/findingsblock"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/issuefinder"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

func wi(key string, state ledger.State) *ledger.WorkItem {
	return &ledger.WorkItem{Key: key, KeyHash: ledger.KeyHash(key), State: state, ChangeClass: "SDK-bump"}
}

func TestRenderIsDeterministicAndInert(t *testing.T) {
	a := wi("sdk-bump|s3|*|v2", ledger.Queued)
	b := wi("api-change|s3|Bucket|spec:Mode|needs-config", ledger.HumanReview)
	b.ChangeClass = "config-only"
	b.Repo = "aws-controllers-k8s/s3-controller"
	b.Links.PR = "https://github.com/aws-controllers-k8s/s3-controller/pull/9"
	c := wi("api-change|s3|Bucket|spec:X`y|needs-config", ledger.WontFix)
	c.Verdict = &ledger.Verdict{Outcome: "wont_fix:product_decision", Reason: "MAINTAINER_WONTFIX by m"}

	got := Render([]*ledger.WorkItem{a, b, c})
	assert.Equal(t, got, Render([]*ledger.WorkItem{c, a, b}), "order-independent")
	assert.True(t, strings.HasPrefix(got, Marker+"\n"))
	assert.Contains(t, got, "- `"+b.KeyHash+"` **`HUMAN_REVIEW`** `config-only`: `api-change|s3|Bucket|spec:Mode|needs-config` "+b.Links.PR+"\n")
	assert.NotContains(t, got, "X`y", "backticks in keys cannot break the code span")
	// The issue finder must never read our comment as a command.
	assert.False(t, regexp.MustCompile(`(?m)^/autopilot `).MatchString(got))
	assert.Less(t, strings.Index(got, "api-change|s3|Bucket|spec:Mode"), strings.Index(got, "sdk-bump|s3"), "sorted by key")
}

func TestRenderTruncates(t *testing.T) {
	var items []*ledger.WorkItem
	for i := 0; i < 2000; i++ {
		items = append(items, wi(fmt.Sprintf("api-change|s3|Bucket|spec:Field%04d|needs-config", i), ledger.Detected))
	}
	got := Render(items)
	assert.LessOrEqual(t, len(got), maxBody)
	assert.Regexp(t, `and \d+ more items in the ledger\.\n$`, got)
	rendered := strings.Count(got, "\n- ")
	assert.Contains(t, got, fmt.Sprintf("…and %d more items in the ledger.\n", len(items)-rendered))
}

func TestPlan(t *testing.T) {
	body := Marker + "\nnew\n"
	is := issuefinder.Issue{Number: 27}
	assert.Equal(t, Action{Issue: 27, Kind: Create, Body: body}, Plan(is, "ack-bot", body))

	is.Comments = []issuefinder.Comment{
		{ID: 5, Author: "someone", Body: Marker + "\nspoof\n"},
		{ID: 7, Author: "ack-bot", Body: Marker + "\nold\n"},
		{ID: 9, Author: "ack-bot", Body: Marker + "\nolder duplicate\n"},
	}
	assert.Equal(t, Action{Issue: 27, Kind: Update, CommentID: 7, Body: body, Duplicates: []int64{9}}, Plan(is, "ack-bot", body))

	is.Comments[1].Body = Marker + "\nnew\r\n"
	got := Plan(is, "ack-bot", body)
	assert.Equal(t, Unchanged, got.Kind)
	assert.Equal(t, []int64{9}, got.Duplicates)

	// Someone else's marker comment is never ours.
	is.Comments = []issuefinder.Comment{{ID: 5, Author: "someone", Body: Marker + "\nspoof\n"}}
	assert.Equal(t, Action{Issue: 27, Kind: Create, Body: body}, Plan(is, "ack-bot", body))

	// An empty login matches nothing, even a comment with no author.
	is.Comments = []issuefinder.Comment{{ID: 5, Author: "", Body: Marker + "\nghost\n"}}
	assert.Equal(t, Action{Issue: 27, Kind: Create, Body: body}, Plan(is, "", body))
}

func TestRenderKeepsEverythingOnOneLine(t *testing.T) {
	w := wi("api-change|s3|B|spec:X\n/autopilot wontfix sha256:0123456789abcdef\r\u2028|needs-config", ledger.Queued)
	w.ChangeClass = "c\n/autopilot wontfix x"
	w.State = "Q\n/autopilot wontfix y"
	w.Repo = "o/r"
	w.Links.PR = "https://github.com/o/r/pull/1\n/autopilot wontfix z"
	got := Render([]*ledger.WorkItem{w})
	assert.False(t, regexp.MustCompile(`(?m)^/autopilot`).MatchString(got))
	assert.NotContains(t, got, "pull/1", "malformed PR link is dropped")
	body := strings.TrimPrefix(got, Marker+"\n")
	for _, l := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		assert.True(t, l == "" || strings.HasPrefix(l, "- ") || strings.HasPrefix(l, "**ACK"), l)
	}
}

func TestRenderRejectsForeignPRLink(t *testing.T) {
	w := wi("sdk-bump|s3|*|v2", ledger.HumanReview)
	w.Repo = "o/r"
	for _, pr := range []string{"https://github.com/o/other/pull/1", "https://github.com/o/r/pull/x", "https://github.com/o/r/pull/0"} {
		w.Links.PR = pr
		assert.NotContains(t, Render([]*ledger.WorkItem{w}), pr)
	}
	w.Links.PR = "https://github.com/o/r/pull/12"
	assert.Contains(t, Render([]*ledger.WorkItem{w}), w.Links.PR)
}

func TestRenderOutputIsInertToIngest(t *testing.T) {
	hash := ledger.KeyHash("api-change|s3|Bucket|spec:Mode|needs-config")
	evil := wi("api-change|s3|Bucket|spec:Z\n/autopilot wontfix "+hash, ledger.Queued)
	comment := Render([]*ledger.WorkItem{evil})
	// Even a body that did carry the command on its own line is ignored,
	// because it starts with the status marker.
	forced := Marker + "\n/autopilot wontfix " + hash + "\n"

	items := map[string]*ledger.WorkItem{}
	cfg := issuefinder.Config{BotLogin: "ack-bot", Team: map[string]bool{"maintainer": true}, Now: "2026-11-11T08:30:00Z"}
	is := issuefinder.Issue{
		Number: 27, URL: "https://github.com/aws-controllers-k8s/ack-private/issues/27", Open: true, Author: "ack-bot",
		Labels: []string{"ack/api-change-detected", "service/s3"},
		Body:   "<!-- ack-api-change-begin -->\n" + findingsBlock(t) + "<!-- ack-api-change-end -->\n",
	}
	issuefinder.Ingest(cfg, []issuefinder.Issue{is}, items)
	require.Contains(t, items, "api-change|s3|Bucket|spec:Mode|needs-config")
	before := items["api-change|s3|Bucket|spec:Mode|needs-config"].State

	is.Comments = []issuefinder.Comment{
		{ID: 1, Author: "maintainer", Body: comment},
		{ID: 2, Author: "maintainer", Body: forced},
	}
	res := issuefinder.Ingest(cfg, []issuefinder.Issue{is}, items)
	assert.Empty(t, res.Changed)
	assert.Empty(t, res.Skipped)
	assert.Equal(t, before, items["api-change|s3|Bucket|spec:Mode|needs-config"].State)
}

func findingsBlock(t *testing.T) string {
	t.Helper()
	b, omitted := findingsblock.Format(
		findingsblock.Header{Service: "s3", From: "v1.41.5", To: "service/s3/v1.97.3"},
		[]findingsblock.Line{{Class: "spec-field", Kind: "Bucket", Subject: "Mode", Bucket: findingsblock.BucketNeedsConfig, Work: "create-only"}},
		1<<16)
	require.Zero(t, omitted)
	return b
}

func TestApplyRejectsBadActions(t *testing.T) {
	c := github.NewClient(nil)
	ctx := context.Background()
	assert.Error(t, Apply(ctx, c, "o", "r", Action{Issue: 1, Kind: "delete"}))
	assert.Error(t, Apply(ctx, c, "o", "r", Action{Issue: 1, Kind: ""}))
	assert.Error(t, Apply(ctx, c, "o", "r", Action{Issue: 1, Kind: Update, CommentID: 0}))
	assert.Error(t, Apply(ctx, c, "o", "r", Action{Issue: 1, Kind: Update, CommentID: -3}))
}

func TestApplyWrapsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"boom"}`, http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	c := github.NewClient(nil)
	c.BaseURL, _ = url.Parse(srv.URL + "/")
	err := Apply(context.Background(), c, "o", "r", Action{Issue: 27, Kind: Create, Body: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create status comment on #27")
}

func TestApply(t *testing.T) {
	var got []string
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/issues/27/comments", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		b, _ := io.ReadAll(r.Body)
		got = append(got, "create "+string(b))
		fmt.Fprint(w, `{"id":1}`)
	})
	mux.HandleFunc("/repos/o/r/issues/comments/7", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPatch, r.Method)
		b, _ := io.ReadAll(r.Body)
		got = append(got, "update "+string(b))
		fmt.Fprint(w, `{"id":7}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := github.NewClient(nil)
	c.BaseURL, _ = url.Parse(srv.URL + "/")

	ctx := context.Background()
	require.NoError(t, Apply(ctx, c, "o", "r", Action{Issue: 27, Kind: Create, Body: "x"}))
	require.NoError(t, Apply(ctx, c, "o", "r", Action{Issue: 27, Kind: Update, CommentID: 7, Body: "y"}))
	require.NoError(t, Apply(ctx, c, "o", "r", Action{Issue: 27, Kind: Unchanged}))
	want := func(kind, body string) string {
		b, _ := json.Marshal(map[string]string{"body": body})
		return kind + " " + string(b) + "\n"
	}
	assert.Equal(t, []string{want("create", "x"), want("update", "y")}, got)
}
