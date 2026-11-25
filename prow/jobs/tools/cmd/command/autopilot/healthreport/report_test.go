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

package healthreport

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/policy"
)

var update = flag.Bool("update", false, "rewrite testdata/report-2026-11-18.md")

// entry is "<ts> <STATE> [reason...]".
func entry(s string) ledger.Transition {
	f := strings.SplitN(s, " ", 3)
	t := ledger.Transition{TS: f[0], To: ledger.State(f[1]), By: "autopilot/dispatcher"}
	if len(f) == 3 {
		t.Reason = f[2]
	}
	return t
}

func fixtureItem(key string, history ...string) *ledger.WorkItem {
	svc := strings.Split(key, "|")[1]
	w := &ledger.WorkItem{
		Schema: ledger.Schema, Key: key, KeyHash: ledger.KeyHash(key), Generation: 1,
		Source: "api-change", Service: svc, Repo: "aws-controllers-k8s/" + svc + "-controller",
		ChangeClass: "SDK-bump", AutonomyLevel: "human-merge", PriorityBand: 3,
		Links: ledger.Links{ViewIssue: "https://github.com/aws-controllers-k8s/ack-private/issues/" + map[string]string{
			"s3": "11", "iam": "12", "rds": "13", "ec2": "14", "eks": "15"}[svc]},
	}
	for _, h := range history {
		w.History = append(w.History, entry(h))
	}
	w.State = w.History[len(w.History)-1].To
	return w
}

// fixture is a ledger with one item for each thing the report covers, on
// and around 2026-11-18.
func fixture() map[string]*ledger.WorkItem {
	pr := func(w *ledger.WorkItem, n string) *ledger.WorkItem {
		w.Links.PR = "https://github.com/" + w.Repo + "/pull/" + n
		return w
	}
	gated := pr(fixtureItem("sdk-bump|s3|*|service/s3/v1.2.0",
		"2026-11-17T08:00:00Z DETECTED", "2026-11-17T08:00:00Z QUEUED NO_TRIAGE_NEEDED",
		"2026-11-18T01:00:00Z RUNNING autopilot-a-g1-r1", "2026-11-18T03:00:00Z GATING PR_OPENED",
		"2026-11-18T05:00:00Z PR_READY GATE_PASSED", "2026-11-18T05:00:00Z HUMAN_REVIEW MARKED_READY"), "7")
	gated.Usage = &ledger.Usage{Tokens: 1000, E2ERuns: 0}
	noChange := fixtureItem("sdk-bump|iam|*|service/iam/v1.3.0",
		"2026-11-18T00:00:00Z DETECTED", "2026-11-18T00:00:00Z QUEUED NO_TRIAGE_NEEDED",
		"2026-11-18T02:00:00Z RUNNING autopilot-b-g1-r1", "2026-11-18T04:00:00Z DONE COMPLETED_NO_CHANGE")
	noChange.Verdict = &ledger.Verdict{Outcome: "completed_no_change", Reason: "COMPLETED_NO_CHANGE"}
	escalated := fixtureItem("api-change|rds|DBInstance|spec:Mode|needs-config",
		"2026-11-16T08:00:00Z DETECTED", "2026-11-17T08:00:00Z QUEUED TRIAGE_ACCEPTED",
		"2026-11-17T20:00:00Z RUNNING autopilot-c-g1-r1", "2026-11-18T02:00:00Z QUEUED RETRY INFRA_NO_VERDICT",
		"2026-11-18T03:00:00Z RUNNING autopilot-c-g1-r2", "2026-11-18T09:00:00Z ESCALATED AMBIGUOUS_SPEC")
	escalated.ChangeClass = "config-only"
	escalated.Usage = &ledger.Usage{Tokens: 250000, E2ERuns: 2}
	escalated.Verdict = &ledger.Verdict{Outcome: "needs_human", Reason: "AMBIGUOUS_SPEC"}
	gateFailed := pr(fixtureItem("api-change|ec2|Vpc|spec:Ipv6|needs-config",
		"2026-11-15T08:00:00Z DETECTED", "2026-11-16T08:00:00Z QUEUED TRIAGE_ACCEPTED",
		"2026-11-17T08:00:00Z RUNNING autopilot-d-g1-r1", "2026-11-17T12:00:00Z GATING PR_OPENED",
		"2026-11-17T12:00:00Z GATING autopilot-d-g1-d1", "2026-11-18T06:00:00Z QUEUED RETRY GATE_FAILED"), "40")
	gateFailed.ChangeClass = "config-only"
	wontFix := fixtureItem("sdk-bump|eks|*|service/eks/v1.9.0",
		"2026-11-18T06:00:00Z DETECTED", "2026-11-18T06:00:00Z QUEUED NO_TRIAGE_NEEDED",
		"2026-11-18T07:00:00Z RUNNING autopilot-e-g1-r1", "2026-11-18T08:00:00Z WONT_FIX WONT_FIX_NOT_REAL")
	stalled := pr(fixtureItem("sdk-bump|s3|*|service/s3/v1.1.0",
		"2026-11-10T08:00:00Z DETECTED", "2026-11-10T08:00:00Z QUEUED NO_TRIAGE_NEEDED",
		"2026-11-16T08:00:00Z RUNNING autopilot-f-g1-r1", "2026-11-17T08:00:00Z GATING PR_OPENED",
		"2026-11-18T10:00:00Z ESCALATED GATE_STALLED"), "5")
	blocked := fixtureItem("api-change|s3|Bucket|spec:Mode|needs-config",
		"2026-11-10T08:00:00Z DETECTED", "2026-11-10T08:00:00Z TRIAGED NO_TRIAGE_NEEDED",
		"2026-11-10T08:00:00Z BLOCKED DEPENDS_ON sdk-bump|s3|*|service/s3/v1.2.0",
		"2026-11-12T08:00:00Z BLOCKED DEPENDS_ON sdk-bump|s3|*|service/s3/v1.2.0")
	queued := fixtureItem("sdk-bump|iam|*|service/iam/v1.4.0",
		"2026-11-15T08:00:00Z DETECTED", "2026-11-15T08:00:00Z QUEUED NO_TRIAGE_NEEDED")
	fresh := fixtureItem("sdk-bump|rds|*|service/rds/v2.0.0", "2026-11-18T12:00:00Z DETECTED")
	// Moved after the window: the report sees it QUEUED.
	later := fixtureItem("sdk-bump|ec2|*|service/ec2/v1.5.0",
		"2026-11-14T08:00:00Z DETECTED", "2026-11-14T08:00:00Z QUEUED NO_TRIAGE_NEEDED",
		"2026-11-19T01:00:00Z RUNNING autopilot-g-g1-r1")
	// A gate failure that exhausted attempts, earlier in the week.
	exhausted := pr(fixtureItem("sdk-bump|eks|*|service/eks/v1.8.0",
		"2026-11-12T08:00:00Z DETECTED", "2026-11-12T08:00:00Z QUEUED NO_TRIAGE_NEEDED",
		"2026-11-13T08:00:00Z RUNNING autopilot-h-g1-r3", "2026-11-14T01:00:00Z GATING PR_OPENED",
		"2026-11-14T02:00:00Z ESCALATED ATTEMPTS_EXHAUSTED"), "9")
	exhausted.Verdict = &ledger.Verdict{Outcome: "failed", Reason: "GATE_FAILED"}
	// Reaped with no attempts left.
	reaped := fixtureItem("sdk-bump|rds|*|service/rds/v1.9.0",
		"2026-11-17T08:00:00Z DETECTED", "2026-11-17T08:00:00Z QUEUED NO_TRIAGE_NEEDED",
		"2026-11-18T05:00:00Z RUNNING autopilot-i-g1-r3", "2026-11-18T11:00:00Z ESCALATED ATTEMPTS_EXHAUSTED")
	reaped.Verdict = &ledger.Verdict{Outcome: "failed", Reason: "INFRA_NO_VERDICT"}
	// Text from outside: a key that tries to break out of its code span, and
	// links that are not GitHub's.
	hostile := fixtureItem("api-change|iam|Role|spec:`x`\n# heading|needs-config", "2026-11-18T13:00:00Z DETECTED")
	hostile.Links.ViewIssue = "javascript:alert(1)"
	hostile.Links.PR = "https://example.com/pull/1"
	out := map[string]*ledger.WorkItem{}
	for _, w := range []*ledger.WorkItem{gated, noChange, escalated, gateFailed, wontFix, stalled, blocked, queued, fresh, later, exhausted, reaped, hostile} {
		out[w.Key] = w
	}
	return out
}

var reportDate = time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)

func testPolicy(t *testing.T) *policy.Policy {
	t.Helper()
	p, err := policy.Load("../policy/testdata")
	require.NoError(t, err)
	return &p
}

func TestRenderGolden(t *testing.T) {
	got, sum := Render(fixture(), testPolicy(t), nil, reportDate)
	golden := filepath.Join("testdata", "report-2026-11-18.md")
	if *update {
		require.NoError(t, os.WriteFile(golden, []byte(got), 0o644))
	}
	want, err := os.ReadFile(golden)
	require.NoError(t, err)
	assert.Equal(t, string(want), got)
	assert.Equal(t, Summary{Date: "2026-11-18", Items: 13, RunsCompleted: 2, RunsFailed: 3, Escalated: 3, GateFirstPass: 1, GateFirstFail: 1}, sum)
}

// TestRenderIsDeterministic: map order, later history and the wall clock
// do not change the report.
func TestRenderIsDeterministic(t *testing.T) {
	first, _ := Render(fixture(), testPolicy(t), nil, reportDate)
	for i := 0; i < 20; i++ {
		again, _ := Render(fixture(), testPolicy(t), nil, reportDate.Add(13*time.Hour))
		require.Equal(t, first, again)
	}
	// ESCALATED is final for the Dispatcher (an ATTEMPTS_EXHAUSTED
	// escalation is read with the item's verdict); every other item moves on.
	items := fixture()
	for _, w := range items {
		if w.State != ledger.Escalated {
			w.History = append(w.History, entry("2026-11-20T00:00:00Z ESCALATED LATER"))
		}
	}
	again, _ := Render(items, testPolicy(t), nil, reportDate)
	assert.Equal(t, first, again, "entries after the window change nothing")
}

func TestRenderEmptyLedger(t *testing.T) {
	got, sum := Render(nil, nil, errors.New("policy/autopilot.yaml: no such file"), reportDate)
	assert.Equal(t, Summary{Date: "2026-11-18"}, sum)
	assert.Contains(t, got, "Policy: unreadable (`policy/autopilot.yaml: no such file`).")
	assert.Contains(t, got, "| Failure rate: failed / (completed + failed) | n/a | n/a |")
	assert.Contains(t, got, "| total | 0 |")
	assert.Contains(t, got, "Root causes: not generated (deterministic report only).")
}

func TestRenderEscapesLedgerText(t *testing.T) {
	got, _ := Render(fixture(), nil, nil, reportDate)
	assert.NotContains(t, got, "javascript:")
	assert.NotContains(t, got, "example.com")
	assert.NotContains(t, got, "\n# heading")
	assert.Contains(t, got, "`api-change|iam|Role|spec:'x''# heading|needs-config`")
}

func TestStateAt(t *testing.T) {
	w := fixtureItem("sdk-bump|s3|*|v", "2026-11-10T00:00:00Z QUEUED", "2026-11-11T00:00:00Z QUEUED again",
		"2026-11-12T00:00:00Z RUNNING", "bad-ts BLOCKED")
	s, since, ok := stateAt(w, time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC))
	assert.True(t, ok)
	assert.Equal(t, ledger.Queued, s)
	assert.Equal(t, time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), since, "since the first of consecutive entries")
	_, _, ok = stateAt(w, time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC))
	assert.False(t, ok)
	s, _, _ = stateAt(w, time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC))
	assert.Equal(t, ledger.Running, s, "an unreadable time is skipped")
}

func TestSignature(t *testing.T) {
	w := fixtureItem("sdk-bump|s3|*|v", "2026-11-10T00:00:00Z RUNNING", "2026-11-10T01:00:00Z QUEUED RETRY BUILD_FAILED",
		"2026-11-10T02:00:00Z ESCALATED DEPENDENCY_DEAD sdk-bump|x", "2026-11-10T03:00:00Z ESCALATED lower case")
	assert.Equal(t, "BUILD_FAILED", signature(w, 1))
	assert.Equal(t, "DEPENDENCY_DEAD", signature(w, 2))
	assert.Equal(t, "INVALID_REASON", signature(w, 3))
	assert.Equal(t, "NO_REASON", token(""))
}
