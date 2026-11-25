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
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

// bumpGating is an SDK-bump item gating its own PR, with its verdict and
// the PR's Gate-Verdict trailer.
func bumpGating(t *testing.T, status dod.Status, reclassify string, reasons ...string) (*ledger.WorkItem, *fakeGH, gateLauncher) {
	t.Helper()
	w := gating(running(item(bumpKey, "SDK-bump", ledger.Queued), "2026-11-12T08:30:00Z", 1, 3))
	v := verdictFor(t, w, status, reclassify, reasons...)
	gh := newFakeGH()
	pr := openPR("ack-bot")
	pr.Body = "card\n\nWork-Item: " + w.KeyHash + "\n" + dod.Trailer(v)
	gh.prs[7] = pr
	l := gateLauncher{fakeLauncher: &fakeLauncher{statuses: map[string]Status{}}, verdicts: map[string][]byte{w.Claim.LaunchID: v}, sigs: map[string][]byte{}}
	return w, gh, l
}

func TestGateSDKBumpPassMarksReady(t *testing.T) {
	w, gh, l := bumpGating(t, dod.StatusPass, "")
	items := ledgerOf(w)
	assert.Empty(t, gate(context.Background(), items, testEnv(gh, l, true)))
	assert.Equal(t, ledger.HumanReview, w.State)
	assert.Equal(t, []ledger.State{ledger.PRReady, ledger.HumanReview}, states(w)[len(w.History)-2:])
	assert.Equal(t, "MARKED_READY", last(w).Reason)
	assert.Equal(t, []int{7}, gh.ready)
	assert.Nil(t, w.Claim)
	require.NotNil(t, w.Gate)
	assert.Equal(t, ledger.GateSourceSDKBump, w.Gate.Source)
	assert.Equal(t, ledger.GatePass, w.Gate.Verdict)
	assert.Equal(t, head1, w.Gate.HeadSHA)
	assert.Equal(t, []string{"real-aws-e2e"}, w.Gate.NotCovered)
	assert.Equal(t, &ledger.Review{Number: 7, Author: "ack-bot", WouldMerge: ledger.WouldMergeYes, ReadyAt: testEnv(gh, l, true).ts, LastActivity: testEnv(gh, l, true).ts}, w.Review)
	assertLints(t, items)
}

func TestGatePassWithWritesOffStaysDraft(t *testing.T) {
	w, gh, l := bumpGating(t, dod.StatusPass, "")
	gate(context.Background(), ledgerOf(w), testEnv(gh, l, false))
	assert.Equal(t, ledger.HumanReview, w.State)
	assert.Equal(t, "READY_PENDING", last(w).Reason)
	assert.Empty(t, gh.ready)
}

func TestGatePassKeepsAServerDefaultBlockDraft(t *testing.T) {
	w, gh, l := bumpGating(t, dod.StatusPass, "")
	pr := gh.prs[7]
	pr.Labels = []string{LabelBlockedServerDefault}
	gh.prs[7] = pr
	gate(context.Background(), ledgerOf(w), testEnv(gh, l, true))
	assert.Equal(t, ledger.HumanReview, w.State)
	assert.Empty(t, gh.ready)
}

func TestGateOutcomes(t *testing.T) {
	for name, tc := range map[string]struct {
		status     dod.Status
		reclassify string
		reasons    []string
		state      ledger.State
		reason     string
	}{
		"reclassify escalates": {dod.StatusFail, "hand-written-code", []string{"path-allowlist:PATH_NOT_ALLOWED"}, ledger.Escalated, "GATE_RECLASSIFY_HAND_WRITTEN_CODE"},
		"env retries":          {dod.StatusFail, "", []string{"regen-clean:REGEN_FAILED"}, ledger.Gating, ""},
		// Plan 9 review F1: a pod that cannot sandbox the build never passes.
		"no sandbox env retries": {dod.StatusFail, "", []string{"build:BUILD_SANDBOX_FAILED"}, ledger.Gating, ""},
		"product fail retries":   {dod.StatusFail, "", []string{"build:BUILD_FAILED"}, ledger.Queued, "RETRY GATE_FAILED"},
	} {
		t.Run(name, func(t *testing.T) {
			w, gh, l := bumpGating(t, tc.status, tc.reclassify, tc.reasons...)
			items := ledgerOf(w)
			assert.Empty(t, gate(context.Background(), items, testEnv(gh, l, true)))
			assert.Equal(t, tc.state, w.State)
			if tc.reason != "" {
				assert.Equal(t, tc.reason, last(w).Reason)
			}
			assert.Empty(t, gh.ready, "a failed gate leaves the PR draft")
			assertLints(t, items)
		})
	}
}

func TestGateEnvFailureLaunchesADodJob(t *testing.T) {
	w, gh, l := bumpGating(t, dod.StatusFail, "", "regen-clean:REGEN_FAILED")
	items := ledgerOf(w)
	gate(context.Background(), items, testEnv(gh, l, true))
	require.Len(t, l.launched, 1)
	req := l.launched[0]
	assert.Equal(t, dodWorkflow, req.Workflow)
	assert.Equal(t, "autopilot-"+w.KeyHash[7:]+"-g1-d1", req.LaunchID)
	assert.Equal(t, map[string]string{
		"service": "s3", "pr": "7", "head-sha": head1, "base-sha": base1, "pr-author": "ack-bot",
		"work-item": w.KeyHash, "change-class": "SDK-bump",
	}, req.Args)
	assert.Equal(t, []ledger.State{ledger.RetryEnv, ledger.Gating}, states(w)[len(w.History)-2:])
	assert.Equal(t, req.LaunchID, w.Claim.LaunchID)
	assert.Equal(t, dodWorkflow, w.Claim.Workflow)
	assert.Equal(t, 1, w.Gate.EnvRetries)
	assert.Equal(t, 1, w.Attempts.Runs, "RETRY_ENV charges no attempt")
	assertLints(t, items)
}

func TestGateEnvRetriesAreCapped(t *testing.T) {
	for _, reason := range []string{"regen-clean:REGEN_FAILED", "build:BUILD_SANDBOX_FAILED"} {
		t.Run(reason, func(t *testing.T) { testGateEnvRetriesAreCapped(t, reason) })
	}
}

func testGateEnvRetriesAreCapped(t *testing.T, reason string) {
	w, gh, l := bumpGating(t, dod.StatusFail, "", reason)
	e := testEnv(gh, l, true)
	gate(context.Background(), ledgerOf(w), e) // d1, retry 1
	for i := 0; i < 2; i++ {
		id := w.Claim.LaunchID
		l.statuses[id] = Status{Phase: PhaseFinished, Job: JobFailed}
		l.verdicts[id] = l.verdicts[w.Links.ProwJobs[0]]
		l.sigs[id] = signFor(t, l.verdicts[id])
		gate(context.Background(), ledgerOf(w), e)
	}
	assert.Equal(t, ledger.Escalated, w.State)
	assert.Equal(t, "GATE_ENV_EXHAUSTED", last(w).Reason)
	assert.Len(t, l.launched, 2, "two env retries, then escalate")
}

func TestGateAddFieldLaunchesDodThenPasses(t *testing.T) {
	w := gating(running(item(fieldKey, "config-only", ledger.Queued), "2026-11-12T08:30:00Z", 1, 3))
	gh := newFakeGH()
	gh.prs[7] = openPR("ack-agent")
	l := gateLauncher{fakeLauncher: &fakeLauncher{statuses: map[string]Status{}}, verdicts: map[string][]byte{}, sigs: map[string][]byte{}}
	items := ledgerOf(w)
	e := testEnv(gh, l, true)

	gate(context.Background(), items, e)
	require.Len(t, l.launched, 1)
	id := l.launched[0].LaunchID
	assert.Equal(t, ledger.Gating, w.State)
	assert.Equal(t, ledger.GateSourceDod, w.Gate.Source)
	assert.Equal(t, 1, w.Gate.Gates)
	assertLints(t, items)

	// Still running: nothing changes.
	l.statuses[id] = Status{Phase: PhaseRunning}
	gate(context.Background(), items, e)
	assert.Equal(t, ledger.Gating, w.State)

	l.statuses[id] = Status{Phase: PhaseFinished, Job: JobSucceeded}
	l.verdicts[id] = verdictFor(t, w, dod.StatusPass, "")
	l.sigs[id] = signFor(t, l.verdicts[id])
	assert.Empty(t, gate(context.Background(), items, e))
	assert.Equal(t, ledger.HumanReview, w.State)
	assert.Equal(t, ledger.GatePass, w.Gate.Verdict)
	assert.Equal(t, dod.Digest(l.verdicts[id]), w.Gate.Digest)
	assert.Equal(t, []int{7}, gh.ready)
	assertLints(t, items)
}

func TestGateNewResourceStaysDraft(t *testing.T) {
	w := item("api-change|s3|Widget|new-resource", "new-resource", ledger.Queued)
	w.AutonomyLevel = "draft"
	w = gating(running(w, "2026-11-12T08:30:00Z", 1, 3))
	gh := newFakeGH()
	gh.prs[7] = openPR("ack-agent")
	l := gateLauncher{fakeLauncher: &fakeLauncher{statuses: map[string]Status{}}, verdicts: map[string][]byte{}, sigs: map[string][]byte{}}
	e := testEnv(gh, l, true)
	gate(context.Background(), ledgerOf(w), e)
	id := w.Claim.LaunchID
	l.statuses[id] = Status{Phase: PhaseFinished, Job: JobSucceeded}
	l.verdicts[id] = verdictFor(t, w, dod.StatusPass, "")
	l.sigs[id] = signFor(t, l.verdicts[id])
	gate(context.Background(), ledgerOf(w), e)
	assert.Equal(t, ledger.HumanReview, w.State)
	assert.Equal(t, "DRAFT_LEVEL", last(w).Reason)
	assert.Empty(t, gh.ready, "new-resource PRs stay draft for their API review")
}

// setSubject replaces the item's verdict with one naming base and tree, and
// points the PR body's trailer at it.
func setSubject(t *testing.T, w *ledger.WorkItem, gh *fakeGH, l gateLauncher, base, tree string) {
	t.Helper()
	v, err := dod.ParseVerdict(l.verdicts[w.Claim.LaunchID])
	require.NoError(t, err)
	v.Subject.BaseSHA, v.Subject.HeadTree = base, tree
	b, err := dod.Marshal(v)
	require.NoError(t, err)
	l.verdicts[w.Claim.LaunchID] = b
	p := gh.prs[7]
	p.Body = "card\n\n" + dod.Trailer(b)
	gh.prs[7] = p
}

func TestGateRejectsWhatItCannotTrust(t *testing.T) {
	for name, tc := range map[string]struct {
		edit  func(w *ledger.WorkItem, gh *fakeGH, l gateLauncher)
		state ledger.State
		why   string
	}{
		"another author": {func(_ *ledger.WorkItem, gh *fakeGH, _ gateLauncher) {
			p := gh.prs[7]
			p.Author = "mallory"
			gh.prs[7] = p
		}, ledger.Escalated, "PR_AUTHOR_MISMATCH"},
		"trailer for other bytes": {func(_ *ledger.WorkItem, gh *fakeGH, _ gateLauncher) {
			p := gh.prs[7]
			p.Body = "card\n\nGate-Verdict: sha256:0000"
			gh.prs[7] = p
		}, ledger.Gating, "GATE_VERIFY_FAILED"},
		"no trailer": {func(_ *ledger.WorkItem, gh *fakeGH, _ gateLauncher) {
			p := gh.prs[7]
			p.Body = "card"
			gh.prs[7] = p
		}, ledger.Gating, "GATE_TRAILER_MISSING"},
		"head has another tree": {func(_ *ledger.WorkItem, gh *fakeGH, _ gateLauncher) {
			gh.trees[head1] = tree2
		}, ledger.Gating, "GATE_VERIFY_FAILED"},
		"no verdict": {func(w *ledger.WorkItem, _ *fakeGH, l gateLauncher) {
			delete(l.verdicts, w.Claim.LaunchID)
		}, ledger.Gating, "GATE_NO_VERDICT"},
		// Plan 9 review F4: a subject that is not 40 lowercase hex.
		"base is not a SHA": {func(w *ledger.WorkItem, gh *fakeGH, l gateLauncher) {
			setSubject(t, w, gh, l, "main", tree1)
		}, ledger.Gating, "GATE_VERDICT_INVALID"},
		"head tree is not lowercase hex": {func(w *ledger.WorkItem, gh *fakeGH, l gateLauncher) {
			upper := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
			gh.trees[head1] = upper
			setSubject(t, w, gh, l, base1, upper)
		}, ledger.Gating, "GATE_VERDICT_INVALID"},
		"verdict for another item": {func(w *ledger.WorkItem, gh *fakeGH, l gateLauncher) {
			other := item(fieldKey, "SDK-bump", ledger.Queued)
			v := verdictFor(t, other, dod.StatusPass, "")
			l.verdicts[w.Claim.LaunchID] = v
			p := gh.prs[7]
			p.Body = "card\n\n" + dod.Trailer(v)
			gh.prs[7] = p
		}, ledger.Escalated, "GATE_VERDICT_FOR_OTHER_ITEM"},
	} {
		t.Run(name, func(t *testing.T) {
			w, gh, l := bumpGating(t, dod.StatusPass, "")
			tc.edit(w, gh, l)
			items := ledgerOf(w)
			assert.Empty(t, gate(context.Background(), items, testEnv(gh, l, true)))
			assert.Equal(t, tc.state, w.State)
			assert.Contains(t, reasons(w), tc.why)
			assert.Empty(t, gh.ready)
			assertLints(t, items)
		})
	}
}

func TestGateRegatesWhenTheHeadMovedDuringTheJob(t *testing.T) {
	w := gating(running(item(fieldKey, "config-only", ledger.Queued), "2026-11-12T08:30:00Z", 1, 3))
	gh := newFakeGH()
	gh.prs[7] = openPR("ack-agent")
	l := gateLauncher{fakeLauncher: &fakeLauncher{statuses: map[string]Status{}}, verdicts: map[string][]byte{}, sigs: map[string][]byte{}}
	e := testEnv(gh, l, true)
	gate(context.Background(), ledgerOf(w), e)
	d1 := w.Claim.LaunchID
	l.statuses[d1] = Status{Phase: PhaseFinished, Job: JobSucceeded}
	l.verdicts[d1] = verdictFor(t, w, dod.StatusPass, "")
	l.sigs[d1] = signFor(t, l.verdicts[d1])
	p := gh.prs[7]
	p.HeadSHA = head2
	gh.prs[7] = p
	gate(context.Background(), ledgerOf(w), e)
	require.Len(t, l.launched, 2)
	assert.Equal(t, head2, l.launched[1].Args["head-sha"])
	assert.Equal(t, 2, w.Gate.Gates)
	assert.Contains(t, last(w).Reason, "GATE_HEAD_MOVED")
}

func TestGateShadowLauncherChangesNothing(t *testing.T) {
	w := gating(running(item(fieldKey, "config-only", ledger.Queued), "2026-11-12T08:30:00Z", 1, 3))
	gh := newFakeGH()
	gh.prs[7] = openPR("ack-agent")
	before := len(w.History)
	gate(context.Background(), ledgerOf(w), testEnv(gh, &ShadowLauncher{}, true))
	assert.Equal(t, ledger.Gating, w.State)
	assert.Len(t, w.History, before)
	assert.Nil(t, w.Gate)
}

// reasons joins every history reason of w.
func reasons(w *ledger.WorkItem) string {
	s := ""
	for _, h := range w.History {
		s += h.Reason + "\n"
	}
	return s
}

func TestGateLaunchesWaitWhilePaused(t *testing.T) {
	for name, edit := range map[string]func(e *prEnv){
		"paused":              func(e *prEnv) { e.sw.Paused = true },
		"abort_inflight":      func(e *prEnv) { e.sw.AbortInflight = true },
		"repo paused":         func(e *prEnv) { e.sw.PausedRepos = []string{"aws-controllers-k8s/s3-controller"} },
		"service not enabled": func(e *prEnv) { e.sw.Services = nil },
	} {
		t.Run(name, func(t *testing.T) {
			w := gating(running(item(fieldKey, "config-only", ledger.Queued), "2026-11-12T08:30:00Z", 1, 3))
			gh := newFakeGH()
			gh.prs[7] = openPR("ack-agent")
			l := gateLauncher{fakeLauncher: &fakeLauncher{statuses: map[string]Status{}}}
			e := testEnv(gh, l, true)
			edit(&e)
			gate(context.Background(), ledgerOf(w), e)
			assert.Empty(t, l.launched)
			assert.Equal(t, ledger.Gating, w.State)
		})
	}
}

// erringVerdicts is a gateLauncher whose verdict read always fails.
type erringVerdicts struct{ gateLauncher }

func (erringVerdicts) Verdict(context.Context, string) ([]byte, []byte, error) {
	return nil, nil, errors.New("s3: throttled")
}

// TestGateStalledEscalates (Plan 9 review F3): an item that has been
// GATING for longer than GateTimeout escalates GATE_STALLED, even while its
// verdict or its PR cannot be read.
func TestGateStalledEscalates(t *testing.T) {
	for _, prDown := range []bool{false, true} {
		w, gh, l := bumpGating(t, dod.StatusPass, "")
		el := erringVerdicts{l}
		e := testEnv(gh, el, true)
		assert.NotEmpty(t, gate(context.Background(), ledgerOf(w), e), "the read error is a problem")
		assert.Equal(t, ledger.Gating, w.State)

		if prDown {
			gh.err = errors.New("github: 502")
		}
		e.now = mustTime("2026-11-13T08:46:00Z")
		e.ts = e.now.Format(time.RFC3339)
		items := ledgerOf(w)
		assert.Empty(t, gate(context.Background(), items, e))
		assert.Equal(t, ledger.Escalated, w.State)
		assert.Equal(t, "GATE_STALLED", last(w).Reason)
		assert.Equal(t, &ledger.Verdict{Outcome: OutcomeFailed, Reason: "GATE_STALLED"}, w.Verdict)
		assert.Nil(t, w.Claim)
		assertLints(t, items)
	}
}

// TestGateMarkReadyRacesAPush (Plan 9 review F6): if the head moved by the
// time MarkReady landed, the PR goes back to draft and is not ready.
func TestGateMarkReadyRacesAPush(t *testing.T) {
	w, gh, l := bumpGating(t, dod.StatusPass, "")
	gh.afterReady = func(f *fakeGH, n int) {
		p := f.prs[n]
		p.HeadSHA = head2
		f.prs[n] = p
	}
	items := ledgerOf(w)
	assert.Empty(t, gate(context.Background(), items, testEnv(gh, l, true)))
	assert.Equal(t, ledger.HumanReview, w.State)
	assert.Equal(t, "READY_PENDING", last(w).Reason)
	assert.Equal(t, []int{7}, gh.drafted)
	assert.Empty(t, w.Review.ReadyAt)
	assertLints(t, items)
}

// TestGateMarkReadyErrorIsAProblem (Plan 9 review F6): the item still
// reaches HUMAN_REVIEW (READY_PENDING), and the error is reported.
func TestGateMarkReadyErrorIsAProblem(t *testing.T) {
	w, gh, l := bumpGating(t, dod.StatusPass, "")
	gh.readyErr = errors.New("graphql: forbidden")
	problems := gate(context.Background(), ledgerOf(w), testEnv(gh, l, true))
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0], "forbidden")
	assert.Equal(t, ledger.HumanReview, w.State)
	assert.Equal(t, "READY_PENDING", last(w).Reason)
	assert.Empty(t, w.Review.ReadyAt)
}

// TestGatePassOnAReadyPRSaysAlreadyReady (Plan 9 review F6): MARKED_READY
// only when the Dispatcher called MarkReady.
func TestGatePassOnAReadyPRSaysAlreadyReady(t *testing.T) {
	w, gh, l := bumpGating(t, dod.StatusPass, "")
	p := gh.prs[7]
	p.Draft = false
	gh.prs[7] = p
	gate(context.Background(), ledgerOf(w), testEnv(gh, l, true))
	assert.Equal(t, ledger.HumanReview, w.State)
	assert.Equal(t, "ALREADY_READY", last(w).Reason)
	assert.Empty(t, gh.ready)
}

// TestGateHeadMovedBeforeTheVerdictChargesNoEnvRetry (Plan 9 review F9): a
// dod job that finished with no verdict (exit 2: the head moved) re-gates
// the new head without charging it an env retry.
func TestGateHeadMovedBeforeTheVerdictChargesNoEnvRetry(t *testing.T) {
	w := gating(running(item(fieldKey, "config-only", ledger.Queued), "2026-11-12T08:30:00Z", 1, 3))
	gh := newFakeGH()
	gh.prs[7] = openPR("ack-agent")
	l := gateLauncher{fakeLauncher: &fakeLauncher{statuses: map[string]Status{}}, verdicts: map[string][]byte{}, sigs: map[string][]byte{}}
	e := testEnv(gh, l, true)
	gate(context.Background(), ledgerOf(w), e)
	d1 := w.Claim.LaunchID
	l.statuses[d1] = Status{Phase: PhaseFinished, Job: JobFailed}
	p := gh.prs[7]
	p.HeadSHA = head2
	gh.prs[7] = p
	items := ledgerOf(w)
	assert.Empty(t, gate(context.Background(), items, e))
	require.Len(t, l.launched, 2)
	assert.Equal(t, head2, l.launched[1].Args["head-sha"])
	assert.Equal(t, ledger.Gating, w.State)
	assert.Equal(t, head2, w.Gate.HeadSHA)
	assert.Zero(t, w.Gate.EnvRetries)
	assert.NotContains(t, states(w), ledger.RetryEnv)
	assertLints(t, items)
}

// dodGating is an add-field item whose dod job finished on head1 with a
// passing verdict and no signature yet.
func dodGating(t *testing.T) (*ledger.WorkItem, *fakeGH, gateLauncher, prEnv, string) {
	t.Helper()
	w := gating(running(item(fieldKey, "config-only", ledger.Queued), "2026-11-12T08:30:00Z", 1, 3))
	gh := newFakeGH()
	gh.prs[7] = openPR("ack-agent")
	l := gateLauncher{fakeLauncher: &fakeLauncher{statuses: map[string]Status{}}, verdicts: map[string][]byte{}, sigs: map[string][]byte{}}
	e := testEnv(gh, l, true)
	gate(context.Background(), ledgerOf(w), e)
	id := w.Claim.LaunchID
	l.statuses[id] = Status{Phase: PhaseFinished, Job: JobSucceeded}
	l.verdicts[id] = verdictFor(t, w, dod.StatusPass, "")
	return w, gh, l, e, id
}

// TestGateDodVerdictMustBeSigned (Plan 9 review): presubmits can write the
// logs bucket, so a dod verdict without a valid signature from the dod key
// is tampering: the item escalates GATE_VERDICT_UNSIGNED, never retried.
func TestGateDodVerdictMustBeSigned(t *testing.T) {
	for name, sig := range map[string]func(t *testing.T, v []byte) []byte{
		"unsigned":                   func(*testing.T, []byte) []byte { return nil },
		"empty":                      func(*testing.T, []byte) []byte { return []byte("\n") },
		"signature over other bytes": func(t *testing.T, v []byte) []byte { return signFor(t, append([]byte(" "), v...)) },
		"not base64":                 func(*testing.T, []byte) []byte { return []byte("not a signature!\n") },
		"another key": func(_ *testing.T, v []byte) []byte {
			other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
			return dod.Sign(other, v)
		},
	} {
		t.Run(name, func(t *testing.T) {
			w, gh, l, e, id := dodGating(t)
			if b := sig(t, l.verdicts[id]); b != nil {
				l.sigs[id] = b
			}
			items := ledgerOf(w)
			assert.Empty(t, gate(context.Background(), items, e))
			assert.Equal(t, ledger.Escalated, w.State)
			assert.Equal(t, "GATE_VERDICT_UNSIGNED", last(w).Reason)
			assert.Equal(t, []string{id}, l.aborted, "abortClaim")
			assert.Nil(t, w.Claim)
			assert.Empty(t, gh.ready)
			assert.Len(t, l.launched, 1, "never retried")
			assertLints(t, items)
		})
	}
}

// TestGateDodWithoutAKeyWaits: with no dod public key no dod verdict can be
// trusted; the item waits (a problem line) until GateTimeout escalates it.
func TestGateDodWithoutAKeyWaits(t *testing.T) {
	w, gh, l, e, id := dodGating(t)
	l.sigs[id] = signFor(t, l.verdicts[id])
	e.dodKey = nil
	n := len(w.History)
	problems := gate(context.Background(), ledgerOf(w), e)
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0], "no dod public key: dod verdicts cannot be trusted")
	assert.Equal(t, ledger.Gating, w.State)
	assert.Len(t, w.History, n)
	assert.Equal(t, id, w.Claim.LaunchID)
	assert.Empty(t, gh.ready)

	e.now = now.Add(GateTimeout + time.Hour)
	gate(context.Background(), ledgerOf(w), e)
	assert.Equal(t, ledger.Escalated, w.State)
	assert.Equal(t, "GATE_STALLED", last(w).Reason)
}

// TestGateSDKBumpNeedsNoSignature: sdk-bump's verdict is bound to the PR
// body's trailer; it passes with no signature and no dod key.
func TestGateSDKBumpNeedsNoSignature(t *testing.T) {
	w, gh, l := bumpGating(t, dod.StatusPass, "")
	e := testEnv(gh, l, true)
	e.dodKey = nil
	assert.Empty(t, gate(context.Background(), ledgerOf(w), e))
	assert.Equal(t, ledger.HumanReview, w.State)
	assert.Equal(t, []int{7}, gh.ready)
}
