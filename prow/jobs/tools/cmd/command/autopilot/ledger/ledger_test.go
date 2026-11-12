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
