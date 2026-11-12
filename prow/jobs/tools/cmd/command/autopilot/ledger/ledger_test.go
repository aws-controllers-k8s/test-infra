package ledger

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKeyHash(t *testing.T) {
	// sha256("a") = ca978112ca1bbdca…
	assert.Equal(t, "sha256:ca978112ca1bbdca", KeyHash("a"))
}

func TestStateDirs(t *testing.T) {
	for s, want := range map[State]string{
		Detected: "open", Running: "inflight", Done: "done",
		Superseded: "done", WontFix: "wontfix", Escalated: "escalated",
	} {
		got, ok := s.Dir()
		assert.True(t, ok, s)
		assert.Equal(t, want, got, s)
	}
	_, ok := State("BOGUS").Dir()
	assert.False(t, ok)
	assert.True(t, Detected.PreDispatch())
	assert.False(t, Running.PreDispatch())
	assert.True(t, WontFix.Terminal())
	assert.False(t, Detected.Terminal())
}

func sampleItem(key string) *WorkItem {
	return &WorkItem{
		Schema: Schema, Key: key, KeyHash: KeyHash(key), Generation: 1,
		Source: "api-change", Service: "s3", Repo: "aws-controllers-k8s/s3-controller",
		GapClass: "needs-config", ChangeClass: "config-only", AutonomyLevel: "human-merge",
		PriorityBand: 4, State: Detected,
		History: []Transition{{TS: "2026-11-11T08:30:00Z", To: Detected, By: "issue-finder"}},
	}
}

func TestMarshalIsDeterministicAndReadable(t *testing.T) {
	w := sampleItem("api-change|s3|Bucket|A->B|needs-config")
	a, err := Marshal(w)
	require.NoError(t, err)
	b, err := Marshal(w)
	require.NoError(t, err)
	assert.Equal(t, a, b)
	assert.Contains(t, string(a), `"key": "api-change|s3|Bucket|A->B|needs-config"`)
	assert.True(t, strings.HasSuffix(string(a), "}\n"))
}

func TestSaveLoadAndMove(t *testing.T) {
	root := t.TempDir()
	w := sampleItem("api-change|s3|Bucket|Mode|needs-config")
	require.NoError(t, Save(root, map[string]*WorkItem{w.Key: w}))
	assert.FileExists(t, filepath.Join(root, "workitems", "open", w.FileName()))

	w.State = WontFix
	w.History = append(w.History, Transition{TS: "2026-11-12T08:30:00Z", To: WontFix, By: "issue-finder"})
	require.NoError(t, Save(root, map[string]*WorkItem{w.Key: w}))
	assert.NoFileExists(t, filepath.Join(root, "workitems", "open", w.FileName()))
	assert.FileExists(t, filepath.Join(root, "workitems", "wontfix", w.FileName()))

	got, err := Load(root)
	require.NoError(t, err)
	assert.Equal(t, map[string]*WorkItem{w.Key: w}, got)
	assert.Empty(t, Lint(root))
}

func TestLoadEmptyLedger(t *testing.T) {
	got, err := Load(t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "workitems", "open")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x.json"), []byte(`{"surprise": 1}`), 0o644))
	_, err := Load(root)
	assert.ErrorContains(t, err, "surprise")
}

func TestLintFindsProblems(t *testing.T) {
	root := t.TempDir()
	write := func(dir, name string, w *WorkItem) {
		b, err := Marshal(w)
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Join(root, "workitems", dir), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, "workitems", dir, name), b, 0o644))
	}
	wrongDir := sampleItem("k1")
	write("done", wrongDir.FileName(), wrongDir)
	wrongHash := sampleItem("k2")
	wrongHash.KeyHash = "sha256:0000000000000000"
	write("open", wrongHash.FileName(), wrongHash)
	wrongHistory := sampleItem("k3")
	wrongHistory.State = Queued
	write("open", wrongHistory.FileName(), wrongHistory)

	errs := Lint(root)
	require.Len(t, errs, 3)
	joined := errors.Join(errs...).Error()
	for _, want := range []string{"belongs in workitems/open", "key_hash", "last history entry"} {
		assert.Equal(t, 1, strings.Count(joined, want), want)
	}
}

func TestEverDispatchedAndFinderOwned(t *testing.T) {
	at := func(to State) Transition { return Transition{TS: "2026-11-11T08:30:00Z", To: to, By: "t"} }
	cases := []struct {
		name       string
		state      State
		history    []Transition
		dispatched bool
		owned      bool
	}{
		{"fresh", Detected, []Transition{at(Detected)}, false, true},
		{"queued never run", Queued, []Transition{at(Detected), at(Queued)}, false, true},
		{"plan pending", PlanPending, []Transition{at(Detected), at(PlanPending)}, false, true},
		{"running", Running, []Transition{at(Queued), at(Running)}, true, false},
		{"back to blocked after run", Blocked, []Transition{at(Queued), at(Running), at(RetryEnv), at(Blocked)}, true, false},
		{"requeued after run", Queued, []Transition{at(Running), at(Queued)}, true, false},
		{"wontfix never run", WontFix, []Transition{at(Detected), at(WontFix)}, false, false},
		{"no history", Detected, nil, false, true},
	}
	for _, c := range cases {
		w := WorkItem{State: c.state, History: c.history}
		assert.Equal(t, c.dispatched, w.EverDispatched(), c.name)
		assert.Equal(t, c.owned, w.FinderOwned(), c.name)
	}
}

func TestSaveRejectsFileNameCollision(t *testing.T) {
	root := t.TempDir()
	a, b := sampleItem("k1"), sampleItem("k2")
	b.KeyHash = a.KeyHash // simulated collision
	err := Save(root, map[string]*WorkItem{a.Key: a, b.Key: b})
	assert.ErrorContains(t, err, "collision")
	assert.NoDirExists(t, filepath.Join(root, "workitems"))
}

func TestLintKeyValidation(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "workitems", "open")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	// empty key
	empty := sampleItem("")
	b, err := Marshal(empty)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, empty.FileName()), b, 0o644))
	// key with a raw invalid UTF-8 byte
	bad := sampleItem("k\x00")
	b, err = Marshal(bad)
	require.NoError(t, err)
	b = []byte(strings.Replace(string(b), `"key": "k\u0000"`, "\"key\": \"k\xff\"", 1))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.json"), b, 0o644))

	joined := errors.Join(Lint(root)...).Error()
	assert.Contains(t, joined, "key is empty")
	assert.Contains(t, joined, "key is not valid UTF-8")
}

func TestEveryStateConstant(t *testing.T) {
	cases := []struct {
		state    State
		dir      string
		pre, end bool
	}{
		{Detected, "open", true, false},
		{Triaged, "open", true, false},
		{Queued, "open", true, false},
		{Blocked, "open", true, false},
		{PlanPending, "open", true, false},
		{Running, "inflight", false, false},
		{Gating, "inflight", false, false},
		{RetryEnv, "inflight", false, false},
		{PRReady, "inflight", false, false},
		{HumanReview, "inflight", false, false},
		{Feedback, "inflight", false, false},
		{MergeSoak, "inflight", false, false},
		{Stale, "inflight", false, false},
		{Merged, "inflight", false, false},
		{Released, "inflight", false, false},
		{Verified, "inflight", false, false},
		{Done, "done", false, true},
		{Superseded, "done", false, true},
		{WontFix, "wontfix", false, true},
		{Escalated, "escalated", false, false},
	}
	// Every state in the table, and no other, has a directory.
	assert.Len(t, stateDir, len(cases))
	for _, c := range cases {
		dir, ok := c.state.Dir()
		assert.True(t, ok, c.state)
		assert.Equal(t, c.dir, dir, c.state)
		assert.Equal(t, c.pre, c.state.PreDispatch(), "PreDispatch %s", c.state)
		assert.Equal(t, c.end, c.state.Terminal(), "Terminal %s", c.state)
	}
}

func TestMarshalGolden(t *testing.T) {
	w := &WorkItem{
		Schema: Schema, Key: "api-change|s3|Bucket|A->B|needs-config", KeyHash: "sha256:0123456789abcdef",
		Generation: 2, Source: "api-change", Service: "s3", Repo: "aws-controllers-k8s/s3-controller",
		Resource: "Bucket", FieldPath: "Spec.Mode", GapClass: "needs-config", ChangeClass: "config-only",
		AutonomyLevel: "human-merge", PriorityBand: 4, DetectorWork: "create-only",
		Evidence:  Evidence{SDKFrom: "v1.0.0", SDKTo: "v1.1.0", Findings: []string{"Bucket.Mode"}},
		DependsOn: []string{"sha256:fedcba9876543210"},
		State:     Blocked,
		Links:     Links{ViewIssue: "https://github.com/aws-controllers-k8s/ack-private/issues/27"},
		Verdict:   &Verdict{Outcome: "wontfix", Reason: "a<b & c"},
		History: []Transition{
			{TS: "2026-11-11T08:30:00Z", To: Detected, By: "issue-finder"},
			{TS: "2026-11-11T09:00:00Z", To: Blocked, By: "triage", Reason: "needs <config>"},
		},
	}
	got, err := Marshal(w)
	require.NoError(t, err)
	assert.Equal(t, goldenFull, string(got))

	// Zero values: nil slices and verdict marshal as null, omitempty fields vanish.
	min, err := Marshal(&WorkItem{})
	require.NoError(t, err)
	assert.Equal(t, goldenMin, string(min))

	// A non-nil empty slice is [] rather than null.
	empty, err := Marshal(&WorkItem{History: []Transition{}})
	require.NoError(t, err)
	assert.Equal(t, strings.Replace(goldenMin, `"history": null`, `"history": []`, 1), string(empty))
}

const goldenFull = `{
  "schema": "ack.autopilot.workitem/v1",
  "key": "api-change|s3|Bucket|A->B|needs-config",
  "key_hash": "sha256:0123456789abcdef",
  "generation": 2,
  "source": "api-change",
  "service": "s3",
  "repo": "aws-controllers-k8s/s3-controller",
  "resource": "Bucket",
  "field_path": "Spec.Mode",
  "gap_class": "needs-config",
  "change_class": "config-only",
  "autonomy_level": "human-merge",
  "priority_band": 4,
  "detector_work": "create-only",
  "evidence": {
    "sdk_from": "v1.0.0",
    "sdk_to": "v1.1.0",
    "findings": [
      "Bucket.Mode"
    ]
  },
  "depends_on": [
    "sha256:fedcba9876543210"
  ],
  "state": "BLOCKED",
  "links": {
    "view_issue": "https://github.com/aws-controllers-k8s/ack-private/issues/27"
  },
  "verdict": {
    "outcome": "wontfix",
    "reason": "a<b & c"
  },
  "history": [
    {
      "ts": "2026-11-11T08:30:00Z",
      "to": "DETECTED",
      "by": "issue-finder"
    },
    {
      "ts": "2026-11-11T09:00:00Z",
      "to": "BLOCKED",
      "by": "triage",
      "reason": "needs <config>"
    }
  ]
}
`
const goldenMin = `{
  "schema": "",
  "key": "",
  "key_hash": "",
  "generation": 0,
  "source": "",
  "service": "",
  "repo": "",
  "gap_class": "",
  "change_class": "",
  "autonomy_level": "",
  "priority_band": 0,
  "evidence": {},
  "state": "",
  "links": {},
  "verdict": null,
  "history": null
}
`
