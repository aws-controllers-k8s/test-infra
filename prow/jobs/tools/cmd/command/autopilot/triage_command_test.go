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

package autopilot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v63/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

func TestReadResultShowsTheTriageVerdict(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, readResult("dispatcher/testdata/agent-result-triage-v1.json", "sha256:0123456789abcdef", &out))
	assert.Equal(t, "result outcome=\"completed_no_change\" reason=\"TRIAGE_VERDICT\" pr=\"\" tokens=5380 e2e_runs=0\n"+
		"triage decision=\"field-hook\" class=\"hand-written-code\" refs=3 invalid=\"\"\n", out.String())

	out.Reset()
	require.NoError(t, readResult("dispatcher/testdata/agent-result-triage-v1.json", "", &out))
	assert.Contains(t, out.String(), "triage verdict present; pass --work-item to check it\n")
}

// triageGitHub serves fakeGitHub's bot issue #27, a team holding
// "maintainer", and comments (a JSON array) on #27. Comment POSTs are
// appended to posted.
func triageGitHub(t *testing.T, comments string, posted *[]string) *github.Client {
	t.Helper()
	body, err := json.Marshal(issueBody(t))
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/ack-private/issues", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"number":27,"html_url":"https://x/27","state":"open","user":{"login":"ack-bot"},
		  "labels":[{"name":"ack/api-change-detected"},{"name":"service/s3"}],"body":%s}]`, body)
	})
	mux.HandleFunc("/repos/o/ack-private/issues/27/comments", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			b, _ := io.ReadAll(r.Body)
			*posted = append(*posted, string(b))
			fmt.Fprint(w, `{"id":99}`)
			return
		}
		fmt.Fprint(w, comments)
	})
	mux.HandleFunc("/orgs/o/teams/ack-admin/members", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"login":"maintainer"}]`)
	})
	return newClient(t, mux)
}

// seedProposal gives the Mode item a pending field-config proposal, as a
// reconciled triage run leaves it, and returns the proposal's launch id.
func seedProposal(t *testing.T, dir string) string {
	t.Helper()
	items, err := ledger.Load(dir)
	require.NoError(t, err)
	w := items[modeKey]
	require.NotNil(t, w)
	id := ledger.TriageLaunchID(w, 1)
	w.Triage = &ledger.Triage{Mode: ledger.TriageModeAPI, Input: ledger.TriageInput(w), Runs: 1, Launches: 1,
		Proposal: &ledger.TriageProposal{LaunchID: id, Decision: ledger.DecisionFieldConfig, Class: "config-only",
			Reason: "is_immutable is enough", EvidenceRefs: []string{"op:CreateBucket"}, TS: "2026-11-12T09:15:00Z"},
		Log: []ledger.TriageEvent{
			{TS: "2026-11-12T09:00:00Z", Event: ledger.TriageStarted, LaunchID: id},
			{TS: "2026-11-12T09:15:00Z", Event: ledger.TriageProposed, LaunchID: id, Decision: ledger.DecisionFieldConfig},
		}}
	w.Links.ProwJobs = append(w.Links.ProwJobs, id)
	w.State = ledger.Triaged
	w.History = append(w.History, ledger.Transition{TS: "2026-11-12T09:15:00Z", To: ledger.Triaged, By: "dispatcher", Reason: "TRIAGE_PROPOSED field-config"})
	require.NoError(t, ledger.Save(dir, items))
	require.Empty(t, ledger.Lint(dir))
	return id
}

func TestDispatchAppliesATriageReply(t *testing.T) {
	dir := t.TempDir()
	copyPolicy(t, dir)
	var posted []string
	require.NoError(t, dispatch(context.Background(), triageGitHub(t, `[]`, &posted), opts(dir), tickTime, io.Discard))
	id := seedProposal(t, dir)

	comments := fmt.Sprintf(`[{"id":5,"user":{"login":"maintainer"},"body":"/autopilot triage accept %s"},
	  {"id":6,"user":{"login":"maintainer"},"body":"/autopilot triage reject %s merge-it"}]`, id, id)
	var out strings.Builder
	require.NoError(t, dispatch(context.Background(), triageGitHub(t, comments, &posted), opts(dir), tickTime.Add(time.Hour), &out))
	got := out.String()
	assert.Contains(t, got, "changed "+ledger.KeyHash(modeKey)+" BLOCKED "+modeKey+" (updated)\n")
	assert.Contains(t, got, `skipped #27: /autopilot triage reject `+id+` by maintainer: unknown decision "merge-it"`+"\n")
	assert.Contains(t, got, "triage off: no policy/triage.yaml, or it lists no mode\n")
	assert.Contains(t, got, "triage-stat api create-only field-config proposed=1 invalid=0 accepted=1 rejected=0 overridden=0\n")
	assert.Empty(t, posted, "dispatch never comments")

	items, err := ledger.Load(dir)
	require.NoError(t, err)
	assert.Equal(t, "maintainer", items[modeKey].Triage.Review.By)
	assert.Equal(t, int64(5), items[modeKey].Triage.Review.CommentID)
}

func TestPostStatusPostsATriageProposalOnce(t *testing.T) {
	dir := t.TempDir()
	copyPolicy(t, dir)
	var posted []string
	require.NoError(t, dispatch(context.Background(), triageGitHub(t, `[]`, &posted), opts(dir), tickTime, io.Discard))
	id := seedProposal(t, dir)
	so := statusOptions{Owner: "o", Repo: "ack-private", CommentLogin: "ack-bot", BotLogin: "ack-bot", LedgerDir: dir, DryRun: true}

	var out strings.Builder
	require.NoError(t, postStatus(context.Background(), triageGitHub(t, `[]`, &posted), so, &out))
	assert.Equal(t, "status #27 create\ntriage #27 create "+id+"\ndry run: no comments written\n", out.String())
	assert.Empty(t, posted)

	so.DryRun = false
	out.Reset()
	require.NoError(t, postStatus(context.Background(), triageGitHub(t, `[]`, &posted), so, &out))
	require.Len(t, posted, 2)
	assert.Contains(t, posted[0], `"body":"<!-- ack-autopilot-status v1 -->`)
	assert.Contains(t, posted[1], `"body":"<!-- ack-autopilot-triage v1 `+id+` -->`)

	existing, err := json.Marshal([]map[string]any{{"id": 99, "user": map[string]string{"login": "ack-bot"},
		"body": "<!-- ack-autopilot-triage v1 " + id + " -->\nproposal"}})
	require.NoError(t, err)
	out.Reset()
	require.NoError(t, postStatus(context.Background(), triageGitHub(t, string(existing), &posted), so, &out))
	assert.NotContains(t, out.String(), "triage #27", "a proposal is commented once")
}
