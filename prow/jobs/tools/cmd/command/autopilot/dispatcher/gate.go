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

package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/policy"
)

// dodWorkflow is the Dispatcher's own gate job (prowlaunch Allowed): ack-dod
// on a PR head, in a pod with no secrets (Plan 9 decision 1).
const dodWorkflow = "dod"

// dodTimeout bounds an autopilot-dod job (Plan 5's job takes 30-90 minutes).
const dodTimeout = 2 * time.Hour

// LabelBlockedServerDefault is sdk-bump's merge block (sdkbump
// LabelBlockedServerDefault; a test keeps them equal): while it is on the PR
// the Dispatcher leaves the PR draft.
const LabelBlockedServerDefault = "autopilot/blocked-server-default"

// envSignatures are dod reasons that say the environment failed, not the
// change (Plan 5 carry-over; sdkbump envReasons).
var envSignatures = map[string]bool{"REGEN_FAILED": true, "REGEN_SETUP_FAILED": true, "CRD_COMPAT_RUN_FAILED": true}

// prEnv is what the PR steps (gate, trackPRs) share in one tick.
type prEnv struct {
	gh PRClient
	// pw is nil unless PR writes are on.
	pw PRWriter
	l  Launcher
	// team are the logins whose reviews count as feedback.
	team map[string]bool
	// authors maps a lane's workflow to the login its PRs must have.
	authors map[string]string
	// bots are logins whose commits are not human.
	bots []string
	// sw holds gate launches like any launch: paused, abort_inflight, a
	// paused repo or a service not enabled.
	sw    policy.Switches
	grace time.Duration
	now   time.Time
	ts    string
}

// gate runs GATING (design 5.4) for every item in GATING or RETRY_ENV:
// it reads the verdict for the PR head (sdk-bump's own, or an autopilot-dod
// job's it launches), checks it with dod.VerifyWith against the head through
// the GitHub API, and moves the item to PR_READY -> HUMAN_REVIEW, RETRY_ENV,
// back to QUEUED (attempt+1) or ESCALATED. It returns one problem per item it
// could not read; those items stay as they are.
func gate(ctx context.Context, items map[string]*ledger.WorkItem, e prEnv) []string {
	var problems []string
	for _, k := range sortedKeys(items) {
		w := items[k]
		if w.State != ledger.Gating && w.State != ledger.RetryEnv {
			continue
		}
		if err := gateOne(ctx, w, e); err != nil {
			problems = append(problems, fmt.Sprintf("gate %s: %v", w.Key, err))
		}
	}
	return problems
}

func gateOne(ctx context.Context, w *ledger.WorkItem, e prEnv) error {
	n, ok := prNumber(w)
	if !ok {
		finish(w, ledger.Escalated, e.ts, "INVALID_PR_LINK", &ledger.Verdict{Outcome: OutcomeNeedsHuman, Reason: "INVALID_PR_LINK"})
		return nil
	}
	pr, err := e.gh.PR(ctx, w.Repo, n)
	if err != nil {
		return err
	}
	if pr.State != "open" {
		// trackPRs records the merge or close.
		return nil
	}
	if want := e.authors[laneWorkflow(w)]; want == "" || pr.Author != want {
		abortClaim(ctx, w, e)
		finish(w, ledger.Escalated, e.ts, "PR_AUTHOR_MISMATCH", &ledger.Verdict{Outcome: OutcomeNeedsHuman, Reason: "PR_AUTHOR_MISMATCH"})
		return nil
	}
	if w.State == ledger.RetryEnv || w.Claim == nil || (w.Claim.Workflow != dodWorkflow && w.Claim.Workflow != "sdk-bump") {
		// RETRY_ENV, or an add-field/add-resource run that just opened its
		// PR: the verdict comes from a dod job on the head.
		return launchGate(ctx, w, pr, e, "")
	}
	if w.Claim.Workflow == dodWorkflow {
		st, err := e.l.Status(ctx, w.Claim.LaunchID)
		if err != nil {
			return err
		}
		switch {
		case st.Phase == PhaseFinished && (st.Job == JobAborted || st.Job == JobErrored):
			return envRetry(ctx, w, pr, e, "GATE_JOB_"+strings.ToUpper(string(st.Job)))
		case st.Phase == PhaseFinished:
		case overdue(w.Claim, e.grace, e.now):
			if !e.l.Shadow() {
				if err := e.l.Abort(ctx, w.Claim.LaunchID); err != nil {
					return err
				}
			}
			return envRetry(ctx, w, pr, e, "GATE_OVERDUE")
		default:
			return nil // still gating
		}
	}
	data, err := readVerdict(ctx, e.l, w.Claim.LaunchID)
	switch {
	case errors.Is(err, ErrNoVerdict):
		return envRetry(ctx, w, pr, e, "GATE_NO_VERDICT")
	case err != nil:
		return err
	}
	// sdk-bump's verdict is bound to the PR body's Gate-Verdict trailer; a
	// dod job's is the Dispatcher's own, so its digest is the file's.
	want := dod.Digest(data)
	head := pr.HeadSHA
	if w.Claim.Workflow == "sdk-bump" {
		t, ok := dod.TrailerValue([]byte(pr.Body))
		if !ok {
			return envRetry(ctx, w, pr, e, "GATE_TRAILER_MISSING")
		}
		want = t
	} else if w.Gate != nil && w.Gate.HeadSHA != "" {
		head = w.Gate.HeadSHA
	}
	if head != pr.HeadSHA {
		// The head moved while the dod job ran: gate the new head.
		return launchGate(ctx, w, pr, e, "GATE_HEAD_MOVED")
	}
	v, err := dod.VerifyWith(ctx, repoCommits{e.gh, w.Repo}, head, data, want)
	switch {
	case errors.Is(err, dod.ErrDigestMismatch), errors.Is(err, dod.ErrTreeMismatch), errors.Is(err, dod.ErrBaseNotAncestor):
		return envRetry(ctx, w, pr, e, "GATE_VERIFY_FAILED")
	case err != nil && v == nil:
		return envRetry(ctx, w, pr, e, "GATE_VERDICT_INVALID")
	case err != nil:
		return err
	}
	if v.Inputs.WorkItem != w.KeyHash || v.Inputs.ChangeClass != w.ChangeClass || v.Subject.Repo != w.Repo {
		abortClaim(ctx, w, e)
		finish(w, ledger.Escalated, e.ts, "GATE_VERDICT_FOR_OTHER_ITEM", &ledger.Verdict{Outcome: OutcomeNeedsHuman, Reason: "GATE_VERDICT_FOR_OTHER_ITEM"})
		return nil
	}
	recordGate(w, pr, v, want, e.ts)
	switch {
	case v.Verdict == dod.StatusPass:
		passGate(ctx, w, pr, e)
	case v.ReclassifyTo != "":
		reason := "GATE_RECLASSIFY_" + strings.ToUpper(strings.ReplaceAll(v.ReclassifyTo, "-", "_"))
		finish(w, ledger.Escalated, e.ts, cleanReason(reason, "GATE_RECLASSIFY"), &ledger.Verdict{Outcome: OutcomeNeedsHuman, Reason: cleanReason(reason, "GATE_RECLASSIFY")})
	case allEnvReasons(v.Reasons):
		return envRetry(ctx, w, pr, e, "GATE_ENV")
	case w.Review != nil && w.Review.PinnedHumanMerge:
		// A person has changed the PR: a new run would overwrite it.
		finish(w, ledger.Escalated, e.ts, "GATE_FAILED_AFTER_REVIEW", &ledger.Verdict{Outcome: OutcomeNeedsHuman, Reason: "GATE_FAILED_AFTER_REVIEW"})
	default:
		retry(w, "GATE_FAILED", e.ts)
	}
	return nil
}

// launchGate starts an autopilot-dod job on the PR head and moves the item to
// GATING holding it. reason, when set, is why the item re-gates. With a
// shadow launcher nothing changes: the item waits where it is.
func launchGate(ctx context.Context, w *ledger.WorkItem, pr PR, e prEnv, reason string) error {
	if e.sw.Paused || e.sw.AbortInflight || e.sw.RepoPaused(w.Repo) || !e.sw.ServiceEnabled(w.Service) {
		return nil // waits, like any launch
	}
	id, ok := nextDodID(w)
	if !ok {
		finish(w, ledger.Escalated, e.ts, "GATE_RUNS_EXHAUSTED", &ledger.Verdict{Outcome: OutcomeNeedsHuman, Reason: "GATE_RUNS_EXHAUSTED"})
		return nil
	}
	req := LaunchRequest{LaunchID: id, Workflow: dodWorkflow, Timeout: dodTimeout, Args: map[string]string{
		"service": w.Service, "pr": strconv.Itoa(pr.Number), "head-sha": pr.HeadSHA, "base-sha": pr.BaseSHA,
		"pr-author": pr.Author, "work-item": w.KeyHash, "change-class": w.ChangeClass,
	}}
	if err := e.l.Launch(ctx, req); err != nil {
		return err
	}
	if e.l.Shadow() {
		return nil
	}
	retries, gates := 0, 0
	if g := w.Gate; g != nil {
		gates = g.Gates
		if g.HeadSHA == pr.HeadSHA {
			retries = g.EnvRetries
		}
	}
	if w.State == ledger.RetryEnv {
		retries++
	}
	w.Gate = &ledger.Gate{Source: ledger.GateSourceDod, LaunchID: id, HeadSHA: pr.HeadSHA, BaseSHA: pr.BaseSHA,
		EnvRetries: retries, Gates: gates + 1, TS: e.ts}
	if w.Attempts == nil {
		w.Attempts = &ledger.Attempts{Runs: 0, MaxRuns: 1}
	}
	w.Claim = &ledger.Claim{LaunchID: id, Workflow: dodWorkflow, TS: e.ts, Deadline: e.now.Add(dodTimeout).UTC().Format(time.RFC3339)}
	w.Links.ProwJobs = append(w.Links.ProwJobs, id)
	if reason == "" {
		reason = id
	} else {
		reason += " " + id
	}
	transition(w, ledger.Gating, e.ts, reason)
	return nil
}

// envRetry is GATING -> RETRY_ENV (design 5.4): no attempt is charged, at
// most ledger.MaxEnvRetries per head, then ESCALATED. The next gate pass
// launches the dod job.
func envRetry(ctx context.Context, w *ledger.WorkItem, pr PR, e prEnv, reason string) error {
	used := 0
	if w.Gate != nil && w.Gate.HeadSHA == pr.HeadSHA {
		used = w.Gate.EnvRetries
	}
	if used >= ledger.MaxEnvRetries {
		finish(w, ledger.Escalated, e.ts, "GATE_ENV_EXHAUSTED", &ledger.Verdict{Outcome: OutcomeFailed, Reason: reason})
		return nil
	}
	if w.State != ledger.RetryEnv {
		transition(w, ledger.RetryEnv, e.ts, reason)
	}
	return launchGate(ctx, w, pr, e, "")
}

// passGate is GATING -> PR_READY -> HUMAN_REVIEW: a human-merge draft is
// marked ready (when PR writes are on and nothing blocks it); a draft-level
// item (new-resource) stays draft for its API review.
func passGate(ctx context.Context, w *ledger.WorkItem, pr PR, e prEnv) {
	if w.Review == nil {
		w.Review = &ledger.Review{Number: pr.Number, Author: pr.Author}
	}
	if !w.Review.PinnedHumanMerge {
		w.Review.WouldMerge = ledger.WouldMergeYes
	}
	w.Review.LastActivity = latest(w.Review.LastActivity, e.ts)
	finish(w, ledger.PRReady, e.ts, "GATE_PASSED", nil)
	w.Verdict = nil
	reason := "READY_PENDING"
	if ensureReady(ctx, w, pr, e) {
		reason = "MARKED_READY"
	} else if w.AutonomyLevel != "human-merge" {
		reason = "DRAFT_LEVEL"
	}
	transition(w, ledger.HumanReview, e.ts, reason)
}

// ensureReady marks a gated human-merge draft ready for review once, and
// reports whether the PR is ready now. It leaves the PR draft while sdk-bump's
// merge block is on, while PR writes are off, or when the gate did not pass
// for the current head.
func ensureReady(ctx context.Context, w *ledger.WorkItem, pr PR, e prEnv) bool {
	if w.AutonomyLevel != "human-merge" || w.Gate == nil || w.Gate.Verdict != ledger.GatePass || w.Gate.HeadSHA != pr.HeadSHA {
		return false
	}
	if !pr.Draft {
		return true
	}
	if e.pw == nil || slices.Contains(pr.Labels, LabelBlockedServerDefault) || (w.Review != nil && w.Review.ReadyAt != "") {
		return false
	}
	if err := e.pw.MarkReady(ctx, w.Repo, pr.Number); err != nil {
		return false
	}
	w.Review.ReadyAt = e.ts
	return true
}

func recordGate(w *ledger.WorkItem, pr PR, v *dod.Verdict, digest, ts string) {
	g := w.Gate
	if g == nil || g.LaunchID != w.Claim.LaunchID {
		// sdk-bump's in-process gate: the first gate of the PR.
		g = &ledger.Gate{Source: ledger.GateSourceSDKBump, LaunchID: w.Claim.LaunchID, Gates: 1}
		if w.Gate != nil {
			g.Gates = w.Gate.Gates + 1
		}
	}
	g.HeadSHA, g.BaseSHA, g.TS = pr.HeadSHA, v.Subject.BaseSHA, ts
	g.Verdict, g.Digest = string(v.Verdict), digest
	g.Reasons, g.ReclassifyTo, g.NotCovered = v.Reasons, v.ReclassifyTo, v.NotCovered
	w.Gate = g
}

func readVerdict(ctx context.Context, l Launcher, id string) ([]byte, error) {
	vr, ok := l.(VerdictReader)
	if !ok {
		return nil, ErrNoVerdict
	}
	return vr.Verdict(ctx, id)
}

// abortClaim stops the item's gate job, if any; best effort.
func abortClaim(ctx context.Context, w *ledger.WorkItem, e prEnv) {
	if w.Claim != nil && w.Claim.Workflow == dodWorkflow && !e.l.Shadow() {
		_ = e.l.Abort(ctx, w.Claim.LaunchID)
	}
}

// allEnvReasons: every reason ("<check>:<signature>") is an environment one.
func allEnvReasons(reasons []string) bool {
	if len(reasons) == 0 {
		return false
	}
	for _, r := range reasons {
		_, sig, _ := strings.Cut(r, ":")
		if !envSignatures[sig] {
			return false
		}
	}
	return true
}

// nextDodID names the item's next dod job: autopilot-<hash>-g<gen>-d<n>.
func nextDodID(w *ledger.WorkItem) (string, bool) {
	prefix := fmt.Sprintf("autopilot-%s-g%d-d", strings.TrimPrefix(ledger.KeyHash(w.Key), "sha256:"), w.Generation)
	n := 1
	for _, id := range w.Links.ProwJobs {
		if strings.HasPrefix(id, prefix) {
			n++
		}
	}
	if n > 999 {
		return "", false
	}
	return prefix + strconv.Itoa(n), true
}

// laneWorkflow is the workflow the item's lane runs.
func laneWorkflow(w *ledger.WorkItem) string {
	return lanes[w.ChangeClass].Workflow
}

// latest returns the later of two RFC 3339 UTC timestamps.
func latest(a, b string) string {
	if a > b {
		return a
	}
	return b
}
