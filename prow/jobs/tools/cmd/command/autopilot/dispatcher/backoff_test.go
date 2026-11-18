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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

func TestRuntimeReleaseHoldsTheRepo(t *testing.T) {
	q := item(bumpKey, "SDK-bump", ledger.Queued)
	other := item("sdk-bump|iam|*|service/iam/v1.2.0", "SDK-bump", ledger.Queued)
	gh := newFakeGH()
	gh.open[q.Repo] = []PR{
		{Author: "ack-bot", HeadRef: "ack-bot/rt-v0.68.0-codegen-v0.68.0", CreatedAt: now.Add(-time.Hour).Format(time.RFC3339)},
	}
	gh.open[other.Repo] = []PR{
		// Older than the back-off: no longer holds the repo.
		{Author: "ack-bot", HeadRef: "ack-bot/rt-v0.67.0-codegen-v0.67.0", CreatedAt: now.Add(-8 * 24 * time.Hour).Format(time.RFC3339)},
		// Not ack-bot's.
		{Author: "mallory", HeadRef: "ack-bot/rt-v0.69.0", CreatedAt: now.Format(time.RFC3339)},
	}
	items := ledgerOf(q, other)
	held, problems := runtimeReleaseRepos(context.Background(), gh, items, now)
	assert.Empty(t, problems)
	assert.Equal(t, map[string]bool{q.Repo: true}, held)

	picks, waits := scheduleHeld(items, testPolicy(t), now, nil, nil, held)
	require.Len(t, picks, 1)
	assert.Equal(t, other.Key, picks[0].item.Key)
	require.Len(t, waits, 1)
	assert.Equal(t, CapRuntimeRelease, waits[0].cap)
}

func TestRuntimeReleaseFailsClosed(t *testing.T) {
	q := item(bumpKey, "SDK-bump", ledger.Queued)
	gh := newFakeGH()
	gh.err = errors.New("rate limited")
	held, problems := runtimeReleaseRepos(context.Background(), errOpen{gh}, ledgerOf(q), now)
	assert.True(t, held[q.Repo])
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0], "rate limited")
}

// errOpen fails OpenPRs with the fake's err.
type errOpen struct{ *fakeGH }

func (e errOpen) OpenPRs(context.Context, string) ([]PR, error) { return nil, e.err }

func TestReapedToday(t *testing.T) {
	a := item(bumpKey, "SDK-bump", ledger.Queued)
	a.History = append(a.History,
		ledger.Transition{TS: "2026-11-12T07:00:00Z", To: ledger.Queued, By: actor, Reason: "RETRY INFRA_NO_VERDICT"},
		ledger.Transition{TS: "2026-11-10T07:00:00Z", To: ledger.Queued, By: actor, Reason: "RETRY INFRA_NO_VERDICT"})
	b := item(fieldKey, "config-only", ledger.Escalated)
	b.Verdict = &ledger.Verdict{Outcome: OutcomeFailed, Reason: "INFRA_NO_VERDICT"}
	b.History = append(b.History, ledger.Transition{TS: "2026-11-12T08:00:00Z", To: ledger.Escalated, By: actor, Reason: "ATTEMPTS_EXHAUSTED"})
	assert.Equal(t, 2, ReapedToday(ledgerOf(a, b), now))
}
