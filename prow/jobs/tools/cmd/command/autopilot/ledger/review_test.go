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

package ledger

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	headSHA = "1111111111111111111111111111111111111111"
	baseSHA = "2222222222222222222222222222222222222222"
)

// reviewedItem is an item in HUMAN_REVIEW with a passing gate and a review.
func reviewedItem(key string) *WorkItem {
	w := sampleItem(key)
	id := "autopilot-" + strings.TrimPrefix(KeyHash(key), "sha256:") + "-g1-r1"
	w.State = HumanReview
	w.History = append(w.History, Transition{TS: "2026-11-11T09:00:00Z", To: HumanReview, By: "dispatcher"})
	w.Attempts = &Attempts{Runs: 1, MaxRuns: 3}
	w.Links = Links{PR: "https://github.com/aws-controllers-k8s/s3-controller/pull/7", ProwJobs: []string{id}}
	w.Gate = &Gate{Source: GateSourceSDKBump, LaunchID: id, HeadSHA: headSHA, BaseSHA: baseSHA,
		Verdict: GatePass, Digest: "sha256:abc", Gates: 1, TS: "2026-11-11T09:00:00Z"}
	w.Review = &Review{Number: 7, Author: "ack-bot", WouldMerge: WouldMergeYes, LastActivity: "2026-11-11T09:00:00Z"}
	return w
}

func TestReviewRoundTripAndLintClean(t *testing.T) {
	root := t.TempDir()
	w := reviewedItem("sdk-bump|s3|*|v1")
	require.NoError(t, Save(root, map[string]*WorkItem{w.Key: w}))
	assert.FileExists(t, filepath.Join(root, "workitems", "inflight", w.FileName()))
	got, err := Load(root)
	require.NoError(t, err)
	assert.Equal(t, w, got[w.Key])
	assert.Empty(t, Lint(root))
}

func TestLintReviewRules(t *testing.T) {
	cases := map[string]struct {
		edit func(w *WorkItem)
		want string
	}{
		"bad source":       {func(w *WorkItem) { w.Gate.Source = "agent" }, `gate source "agent"`},
		"unknown launch":   {func(w *WorkItem) { w.Gate.LaunchID = "autopilot-0000000000000000-g1-d1" }, "gate launch_id"},
		"short head":       {func(w *WorkItem) { w.Gate.HeadSHA = "abc" }, "full commit SHAs"},
		"bad verdict":      {func(w *WorkItem) { w.Gate.Verdict = "maybe" }, `gate verdict "maybe"`},
		"too many retries": {func(w *WorkItem) { w.Gate.EnvRetries = 3 }, "env_retries=3"},
		"no gates":         {func(w *WorkItem) { w.Gate.Gates = 0 }, "gates=0"},
		"gate ts":          {func(w *WorkItem) { w.Gate.TS = "now" }, `gate ts "now"`},
		"gate without pr": {func(w *WorkItem) {
			w.Links.PR = ""
			w.Review = nil
		}, "a gate needs links.pr"},
		"other pr":       {func(w *WorkItem) { w.Review.Number = 8 }, "review number 8"},
		"bad author":     {func(w *WorkItem) { w.Review.Author = "a b" }, `review author "a b"`},
		"negative":       {func(w *WorkItem) { w.Review.HumanCommits = -1 }, "must not be negative"},
		"would merge":    {func(w *WorkItem) { w.Review.WouldMerge = "maybe" }, `would_merge "maybe"`},
		"pinned and yes": {func(w *WorkItem) { w.Review.PinnedHumanMerge = true }, "pinned to human-merge"},
		"merge sha":      {func(w *WorkItem) { w.Review.MergeSHA = "x" }, "merge_sha"},
		"review ts":      {func(w *WorkItem) { w.Review.MergedAt = "2026-11-11" }, "merged_at"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			w := reviewedItem("sdk-bump|s3|*|v1")
			c.edit(w)
			writeItem(t, root, "inflight", w.FileName(), w)
			errs := Lint(root)
			require.NotEmpty(t, errs)
			assert.Contains(t, errors.Join(errs...).Error(), c.want)
		})
	}
}

func TestLoadRejectsUnknownReviewField(t *testing.T) {
	root := t.TempDir()
	w := reviewedItem("sdk-bump|s3|*|v1")
	b, err := Marshal(w)
	require.NoError(t, err)
	b = []byte(strings.Replace(string(b), `"stale_requeues": 0`, `"stale_requeues": 0, "reverts": 1`, 1))
	writeRaw(t, root, "inflight", w.FileName(), b)
	_, err = Load(root)
	assert.ErrorContains(t, err, "reverts")
}
