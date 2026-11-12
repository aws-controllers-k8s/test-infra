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
	b.Links.PR = "https://github.com/aws-controllers-k8s/s3-controller/pull/9"
	c := wi("api-change|s3|Bucket|spec:X`y|needs-config", ledger.WontFix)
	c.Verdict = &ledger.Verdict{Outcome: "wont_fix:product_decision", Reason: "MAINTAINER_WONTFIX by m"}

	got := Render([]*ledger.WorkItem{a, b, c})
	assert.Equal(t, got, Render([]*ledger.WorkItem{c, a, b}), "order-independent")
	assert.True(t, strings.HasPrefix(got, Marker+"\n"))
	assert.Contains(t, got, "- `"+b.KeyHash+"` **HUMAN_REVIEW** config-only: `api-change|s3|Bucket|spec:Mode|needs-config` "+b.Links.PR+"\n")
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
	assert.Equal(t, Action{Issue: 27, Kind: Update, CommentID: 7, Body: body}, Plan(is, "ack-bot", body))

	is.Comments[1].Body = Marker + "\nnew\r\n"
	assert.Equal(t, Unchanged, Plan(is, "ack-bot", body).Kind)
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
