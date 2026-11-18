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
	"fmt"
	"regexp"
	"slices"
)

// Gate sources: where the verdict of a GATING pass came from (Plan 9).
const (
	// GateSourceSDKBump: the sdk-bump run's own in-process ack-dod verdict
	// (its pod runs no LLM).
	GateSourceSDKBump = "sdk-bump"
	// GateSourceDod: an autopilot-dod job the Dispatcher launched on the PR
	// head, in a pod with no secrets (add-field, add-resource, re-gates).
	GateSourceDod = "dod"
)

// Gate verdicts recorded on an item.
const (
	GatePass = "pass"
	GateFail = "fail"
	// GateNone: the gate gave no usable verdict (missing, unreadable, for
	// another head); an environment failure.
	GateNone = "none"
)

// Would-merge values of a Review (design 5.5 rule 4, design 10).
const (
	WouldMergeYes = "yes"
	WouldMergeNo  = "no"
)

// MaxEnvRetries is how many RETRY_ENV gates an item gets per PR head (design
// 5.4: no attempt charged, at most 2).
const MaxEnvRetries = 2

// Gate is the item's last GATING pass (Plan 9): which verdict, for which head.
type Gate struct {
	Source string `json:"source"`
	// LaunchID is the run whose verdict.json was read: the sdk-bump run, or
	// the autopilot-dod job.
	LaunchID string `json:"launch_id"`
	HeadSHA  string `json:"head_sha"`
	BaseSHA  string `json:"base_sha,omitempty"`
	// Verdict is pass, fail or none; empty while a dod job runs.
	Verdict      string   `json:"verdict,omitempty"`
	Digest       string   `json:"digest,omitempty"`
	Reasons      []string `json:"reasons,omitempty"`
	ReclassifyTo string   `json:"reclassify_to,omitempty"`
	NotCovered   []string `json:"not_covered,omitempty"`
	// EnvRetries counts RETRY_ENV gates on HeadSHA.
	EnvRetries int `json:"env_retries"`
	// Gates counts every gate run on the item's PR, re-gates included.
	Gates int    `json:"gates"`
	TS    string `json:"ts"`
}

// Review is the PR's human outcome so far: the gate-agreement inputs (design
// 10) that Plan 10 records beside the gate's verdict. Only the Dispatcher
// writes it, from the PR that links.pr names.
type Review struct {
	Number int    `json:"number"`
	Author string `json:"author"`
	// ReadyAt is when the Dispatcher marked the draft ready for review.
	ReadyAt string `json:"ready_at,omitempty"`
	// ReviewRounds counts team members' non-approval reviews.
	ReviewRounds int `json:"review_rounds"`
	// LastReviewID is the newest review already counted.
	LastReviewID int64 `json:"last_review_id,omitempty"`
	// HumanCommits counts commits on the PR not authored by the bot.
	HumanCommits int `json:"human_commits"`
	// FeedbackRounds counts HUMAN_REVIEW -> FEEDBACK moves.
	FeedbackRounds int `json:"feedback_rounds"`
	// WouldMerge is "yes" while the gate passed and nobody changed or asked
	// to change the PR, "no" for good after (design 5.5 rule 4), empty
	// before the first pass.
	WouldMerge string `json:"would_merge,omitempty"`
	// PinnedHumanMerge: a human changed or asked to change the PR, so it is
	// human-merge for the rest of its life.
	PinnedHumanMerge bool `json:"pinned_human_merge"`
	// LastActivity is the newest gate, review or commit time; idle is
	// measured from it.
	LastActivity  string `json:"last_activity,omitempty"`
	StaleRequeues int    `json:"stale_requeues"`
	MergedAt      string `json:"merged_at,omitempty"`
	MergeSHA      string `json:"merge_sha,omitempty"`
	ClosedAt      string `json:"closed_at,omitempty"`
}

var (
	shaRE      = regexp.MustCompile(`^[0-9a-f]{40}$`)
	reviewerRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,37}[A-Za-z0-9])?(\[bot\])?$`)
	gateIDRE   = regexp.MustCompile(`^autopilot-[0-9a-f]{16}-g[0-9]{1,4}-(r|d)[1-9][0-9]{0,2}$`)
)

// lintReview checks an item's gate and review records.
func lintReview(w *WorkItem) []string {
	var out []string
	if g := w.Gate; g != nil {
		if g.Source != GateSourceSDKBump && g.Source != GateSourceDod {
			out = append(out, fmt.Sprintf("gate source %q is not %s or %s", g.Source, GateSourceSDKBump, GateSourceDod))
		}
		if !gateIDRE.MatchString(g.LaunchID) || !slices.Contains(w.Links.ProwJobs, g.LaunchID) {
			out = append(out, fmt.Sprintf("gate launch_id %q is not a run or dod launch id in links.prowjobs", g.LaunchID))
		}
		if !shaRE.MatchString(g.HeadSHA) || (g.BaseSHA != "" && !shaRE.MatchString(g.BaseSHA)) {
			out = append(out, "gate head_sha and base_sha must be full commit SHAs")
		}
		switch g.Verdict {
		case "", GatePass, GateFail, GateNone:
		default:
			out = append(out, fmt.Sprintf("gate verdict %q is not pass, fail or none", g.Verdict))
		}
		if g.EnvRetries < 0 || g.EnvRetries > MaxEnvRetries || g.Gates < 1 {
			out = append(out, fmt.Sprintf("gate env_retries=%d (0..%d) gates=%d (at least 1) out of range", g.EnvRetries, MaxEnvRetries, g.Gates))
		}
		if !utcRFC3339(g.TS) {
			out = append(out, fmt.Sprintf("gate ts %q is not RFC 3339 UTC", g.TS))
		}
		if w.Links.PR == "" {
			out = append(out, "a gate needs links.pr")
		}
	}
	if r := w.Review; r != nil {
		if w.Links.PR == "" || !PRInRepo(w.Repo, w.Links.PR) || w.Links.PR != fmt.Sprintf("https://github.com/%s/pull/%d", w.Repo, r.Number) {
			out = append(out, fmt.Sprintf("review number %d is not the PR links.pr names", r.Number))
		}
		if !reviewerRE.MatchString(r.Author) {
			out = append(out, fmt.Sprintf("review author %q is not a GitHub login", r.Author))
		}
		if r.ReviewRounds < 0 || r.HumanCommits < 0 || r.FeedbackRounds < 0 || r.StaleRequeues < 0 || r.LastReviewID < 0 {
			out = append(out, "review counters must not be negative")
		}
		switch r.WouldMerge {
		case "", WouldMergeYes, WouldMergeNo:
		default:
			out = append(out, fmt.Sprintf("review would_merge %q is not yes or no", r.WouldMerge))
		}
		if r.PinnedHumanMerge && r.WouldMerge == WouldMergeYes {
			out = append(out, "a PR pinned to human-merge cannot be would_merge yes")
		}
		if r.MergeSHA != "" && !shaRE.MatchString(r.MergeSHA) {
			out = append(out, "review merge_sha must be a full commit SHA")
		}
		for name, ts := range map[string]string{"ready_at": r.ReadyAt, "last_activity": r.LastActivity, "merged_at": r.MergedAt, "closed_at": r.ClosedAt} {
			if ts != "" && !utcRFC3339(ts) {
				out = append(out, fmt.Sprintf("review %s %q is not RFC 3339 UTC", name, ts))
			}
		}
	}
	slices.Sort(out)
	return out
}
