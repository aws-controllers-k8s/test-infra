package ledger

import (
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
