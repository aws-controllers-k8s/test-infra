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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

func queued(key, class string, band int, since string) *ledger.WorkItem {
	w := item(key, class, ledger.Queued)
	w.PriorityBand = band
	w.History = []ledger.Transition{{TS: since, To: ledger.Queued, By: actor}}
	return w
}

func pickKeys(c []candidate) []string {
	var out []string
	for _, x := range c {
		out = append(out, x.item.Key)
	}
	return out
}

func waitCaps(ws []capped) map[string]string {
	out := map[string]string{}
	for _, w := range ws {
		out[w.item.Key] = w.cap
	}
	return out
}

func TestScheduleOrder(t *testing.T) {
	a := queued("sdk-bump|s3|*|v1", "SDK-bump", 3, "2026-11-12T08:10:00Z")
	b := queued("sdk-bump|iam|*|v1", "SDK-bump", 3, "2026-11-12T08:00:00Z")
	c := queued("api-change|kms|Key|spec:X|needs-config", "config-only", 2, "2026-11-12T08:20:00Z")
	p := testPolicy(t)
	p.Switches.Services = append(p.Switches.Services, "kms")
	picks, waits := schedule(ledgerOf(a, b, c), p, now)
	assert.Empty(t, waits)
	assert.Equal(t, []string{c.Key, b.Key, a.Key}, pickKeys(picks))
}

func TestScheduleLeastRecentlyVisitedRepoFirst(t *testing.T) {
	a := queued("sdk-bump|s3|*|v1", "SDK-bump", 3, t0)
	b := queued("sdk-bump|iam|*|v1", "SDK-bump", 3, t0)
	// s3 was visited (a done item ran there); iam never was.
	old := running(item("sdk-bump|s3|*|v0", "SDK-bump", ledger.Queued), "2026-11-01T08:00:00Z", 1, 3)
	old.State, old.Claim = ledger.Done, nil
	picks, _ := schedule(ledgerOf(a, b, old), testPolicy(t), now)
	assert.Equal(t, []string{b.Key, a.Key}, pickKeys(picks))
}

func TestScheduleKillSwitches(t *testing.T) {
	a := queued("sdk-bump|s3|*|v1", "SDK-bump", 3, t0)
	b := queued("sdk-bump|sqs|*|v1", "SDK-bump", 3, t0)
	c := queued("sdk-bump|iam|*|v1", "SDK-bump", 3, t0)
	p := testPolicy(t)
	p.Switches.PausedRepos = []string{"aws-controllers-k8s/iam-controller"}
	picks, waits := schedule(ledgerOf(a, b, c), p, now)
	assert.Equal(t, []string{a.Key}, pickKeys(picks))
	assert.Equal(t, map[string]string{b.Key: CapServiceNotEnabled, c.Key: CapRepoPaused}, waitCaps(waits))

	p.Switches.Paused = true
	picks, waits = schedule(ledgerOf(a), p, now)
	assert.Empty(t, picks)
	assert.Equal(t, map[string]string{a.Key: CapPaused}, waitCaps(waits))
}

func TestSchedulePerRepoAndWithinTick(t *testing.T) {
	busy := running(item("sdk-bump|iam|*|v0", "SDK-bump", ledger.Queued), "2026-11-12T08:30:00Z", 1, 3)
	a := queued("sdk-bump|iam|*|v1", "SDK-bump", 3, t0)
	b := queued("api-change|s3|Bucket|spec:A|needs-config", "config-only", 4, t0)
	c := queued("api-change|s3|Bucket|spec:B|needs-config", "config-only", 4, t0)
	picks, waits := schedule(ledgerOf(busy, a, b, c), testPolicy(t), now)
	assert.Equal(t, []string{b.Key}, pickKeys(picks))
	assert.Equal(t, map[string]string{a.Key: CapRepoBusy, c.Key: CapRepoBusy}, waitCaps(waits))
}

func TestScheduleLLMCap(t *testing.T) {
	r1 := running(item("api-change|iam|Role|spec:A|needs-config", "config-only", ledger.Queued), "2026-11-12T08:30:00Z", 1, 3)
	r2 := running(item("api-change|rds|DBInstance|spec:A|needs-config", "config-only", ledger.Queued), "2026-11-12T08:30:00Z", 1, 3)
	q := queued("api-change|s3|Bucket|spec:A|needs-config", "config-only", 4, t0)
	bump := queued("sdk-bump|eks|*|v1", "SDK-bump", 3, t0)
	picks, waits := schedule(ledgerOf(r1, r2, q, bump), testPolicy(t), now)
	assert.Equal(t, []string{bump.Key}, pickKeys(picks), "sdk-bump is not an LLM run")
	assert.Equal(t, map[string]string{q.Key: CapLLM}, waitCaps(waits))
}

func TestScheduleDailyCaps(t *testing.T) {
	var items []*ledger.WorkItem
	// Five SDK-bump launches in the last 24h (repos other than s3).
	for i, svc := range []string{"a", "b", "c", "d", "e"} {
		w := running(item(fmt.Sprintf("sdk-bump|%s|*|v0", svc), "SDK-bump", ledger.Queued),
			now.Add(-time.Duration(i+1)*time.Hour).Format(time.RFC3339), 1, 3)
		w.State, w.Claim = ledger.Done, nil
		items = append(items, w)
	}
	// An ec2 launch 2h ago (expensive), and one 30h ago that no longer counts.
	ec2 := running(item("sdk-bump|ec2|*|v0", "SDK-bump", ledger.Queued), now.Add(-30*time.Hour).Format(time.RFC3339), 1, 3)
	ec2.History = append(ec2.History, ledger.Transition{TS: now.Add(-2 * time.Hour).Format(time.RFC3339), To: ledger.Running, By: actor})
	ec2.State, ec2.Claim = ledger.Done, nil
	items = append(items, ec2)

	s3 := queued("sdk-bump|s3|*|v1", "SDK-bump", 3, t0)
	urgent := queued("sdk-bump|iam|*|v1", "SDK-bump", 2, t0)
	ec2q := queued("api-change|ec2|Instance|spec:A|needs-config", "config-only", 4, t0)
	items = append(items, s3, urgent, ec2q)

	picks, waits := schedule(ledgerOf(items...), testPolicy(t), now)
	assert.Equal(t, []string{urgent.Key}, pickKeys(picks), "band 2 skips the per-class daily cap")
	assert.Equal(t, map[string]string{s3.Key: CapSDKBumpDaily, ec2q.Key: CapExpensive}, waitCaps(waits))
}

func TestScheduleReviewBackpressureAndOpenPRs(t *testing.T) {
	var items []*ledger.WorkItem
	for _, svc := range []string{"a", "b", "c", "d", "e"} {
		items = append(items, item(fmt.Sprintf("sdk-bump|%s|*|v0", svc), "SDK-bump", ledger.HumanReview))
	}
	s3open := item("sdk-bump|s3|*|v0", "SDK-bump", ledger.HumanReview)
	s3next := queued("sdk-bump|s3|*|v1", "SDK-bump", 3, t0)
	iam := queued("sdk-bump|iam|*|v1", "SDK-bump", 3, t0)
	eks := queued("sdk-bump|eks|*|v1", "SDK-bump", 3, "2026-11-12T08:30:00Z")
	items = append(items, s3open, s3next, iam, eks)

	picks, waits := schedule(ledgerOf(items...), testPolicy(t), now)
	// Six PRs await review (the cap), so nothing more starts.
	assert.Empty(t, picks)
	assert.Equal(t, CapReview, waitCaps(waits)[iam.Key])

	// Drop one: iam fits; s3 already has an open SDK-bump PR; eks then hits review again.
	items = items[1:]
	picks, waits = schedule(ledgerOf(items...), testPolicy(t), now)
	assert.Equal(t, []string{iam.Key}, pickKeys(picks))
	assert.Equal(t, map[string]string{s3next.Key: CapOpenPRs, eks.Key: CapReview}, waitCaps(waits))
}

func TestScheduleRunsExhausted(t *testing.T) {
	w := queued("sdk-bump|s3|*|v1", "SDK-bump", 3, t0)
	w.Attempts = &ledger.Attempts{Runs: 3, MaxRuns: 3}
	picks, waits := schedule(ledgerOf(w), testPolicy(t), now)
	assert.Empty(t, picks)
	assert.Equal(t, map[string]string{w.Key: CapRuns}, waitCaps(waits))
}
