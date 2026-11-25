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
	"crypto/ed25519"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/policy"
)

const (
	head1 = "1111111111111111111111111111111111111111"
	head2 = "3333333333333333333333333333333333333333"
	base1 = "2222222222222222222222222222222222222222"
	tree1 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tree2 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	s3PR  = "https://github.com/aws-controllers-k8s/s3-controller/pull/7"
)

// fakeGH is a PRClient and PRWriter over one repo's PRs.
type fakeGH struct {
	prs     map[int]PR
	commits map[int][]PRCommit
	reviews map[int][]PRReview
	trees   map[string]string
	open    map[string][]PR
	err     error
	ready   []int
	drafted []int
	// readyErr fails MarkReady; afterReady runs after a successful one (a
	// push racing the write).
	readyErr   error
	afterReady func(f *fakeGH, n int)
	draftErr   error
}

func newFakeGH() *fakeGH {
	return &fakeGH{prs: map[int]PR{}, commits: map[int][]PRCommit{}, reviews: map[int][]PRReview{},
		trees: map[string]string{head1: tree1, head2: tree2}, open: map[string][]PR{}}
}

func (f *fakeGH) PR(_ context.Context, _ string, n int) (PR, error) {
	if f.err != nil {
		return PR{}, f.err
	}
	p, ok := f.prs[n]
	if !ok {
		return PR{}, fmt.Errorf("no PR %d", n)
	}
	return p, nil
}

func (f *fakeGH) Commits(_ context.Context, _ string, n int) ([]PRCommit, error) {
	return f.commits[n], nil
}

func (f *fakeGH) Reviews(_ context.Context, _ string, n int) ([]PRReview, error) {
	return f.reviews[n], nil
}

func (f *fakeGH) TreeSHA(_ context.Context, _, c string) (string, error) { return f.trees[c], nil }

func (f *fakeGH) IsAncestor(_ context.Context, _, base, _ string) (bool, error) {
	return base == base1, nil
}

func (f *fakeGH) OpenPRs(_ context.Context, repo string) ([]PR, error) { return f.open[repo], nil }

func (f *fakeGH) MarkReady(_ context.Context, _ string, n int) error {
	if f.readyErr != nil {
		return f.readyErr
	}
	f.ready = append(f.ready, n)
	p := f.prs[n]
	p.Draft = false
	f.prs[n] = p
	if f.afterReady != nil {
		f.afterReady(f, n)
	}
	return nil
}

func (f *fakeGH) MarkDraft(_ context.Context, _ string, n int) error {
	if f.draftErr != nil {
		return f.draftErr
	}
	f.drafted = append(f.drafted, n)
	p := f.prs[n]
	p.Draft = true
	f.prs[n] = p
	return nil
}

// gateLauncher is a fakeLauncher that reads verdicts and sees feedback jobs.
type gateLauncher struct {
	*fakeLauncher
	verdicts map[string][]byte
	// sigs are the dod jobs' verdict.sig files; a missing one is nil.
	sigs     map[string][]byte
	feedback bool
}

func (g gateLauncher) Verdict(_ context.Context, id string) ([]byte, []byte, error) {
	if b, ok := g.verdicts[id]; ok {
		return b, g.sigs[id], nil
	}
	return nil, nil, ErrNoVerdict
}

func (g gateLauncher) FeedbackActive(context.Context, string, int) (bool, error) {
	return g.feedback, nil
}

// openPR is PR 7 of s3-controller at head1, a draft by author.
func openPR(author string) PR {
	return PR{Number: 7, State: "open", Draft: true, Author: author, HeadSHA: head1, BaseSHA: base1,
		CreatedAt: "2026-11-12T08:40:00Z"}
}

// verdictFor is a canonical verdict for w at tree1.
func verdictFor(t *testing.T, w *ledger.WorkItem, status dod.Status, reclassify string, reasons ...string) []byte {
	t.Helper()
	b, err := dod.Marshal(&dod.Verdict{
		Schema: dod.Schema, Tool: dod.Tool{Name: dod.ToolName, Revision: "test"},
		Subject: dod.Subject{Repo: w.Repo, Service: w.Service, BaseSHA: base1, HeadTree: tree1},
		Inputs:  dod.Inputs{ChangeClass: w.ChangeClass, WorkItem: w.KeyHash},
		Verdict: status, Reasons: reasons, ReclassifyTo: reclassify, NotCovered: []string{"real-aws-e2e"},
	})
	require.NoError(t, err)
	return b
}

// gating puts w in GATING on PR 7, holding its run's claim (as reconcile
// leaves it after pr_opened).
func gating(w *ledger.WorkItem) *ledger.WorkItem {
	w.Links.PR = s3PR
	w.State = ledger.Gating
	w.History = append(w.History, ledger.Transition{TS: "2026-11-12T08:45:00Z", To: ledger.Gating, By: actor, Reason: "PR_OPENED"})
	return w
}

// testDodKey signs the dod verdicts in these tests (Plan 9 review); testEnv
// trusts its public half.
var testDodKey = ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))

func testDodPub() ed25519.PublicKey { return testDodKey.Public().(ed25519.PublicKey) }

// signFor is the verdict.sig a dod job leaves for verdict v.
func signFor(t *testing.T, v []byte) []byte {
	t.Helper()
	require.NotEmpty(t, v)
	return dod.Sign(testDodKey, v)
}

func testEnv(gh *fakeGH, l Launcher, writes bool) prEnv {
	e := prEnv{gh: gh, l: l, team: map[string]bool{"alice": true}, dodKey: testDodPub(),
		authors: map[string]string{"sdk-bump": "ack-bot", "add-field": "ack-agent", "add-resource": "ack-agent"},
		bots:    []string{"ack-bot", "ack-agent"}, sw: policy.Switches{Launcher: policy.LauncherProw, Services: []string{"s3", "iam"}},
		grace: 30 * time.Minute, now: now, ts: now.UTC().Format(time.RFC3339)}
	if writes {
		e.pw = gh
	}
	return e
}
