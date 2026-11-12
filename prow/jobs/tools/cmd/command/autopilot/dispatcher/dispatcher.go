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
	"fmt"
	"time"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/policy"
)

type Config struct {
	Now time.Time
	// Policy is nil when the policy files could not be read. The tick then
	// only routes: it reconciles nothing (caps and timeouts are unknown) and
	// launches nothing, and reports PolicyErr. A nil Launcher does the same.
	Policy    *policy.Policy
	PolicyErr error
	Launcher  Launcher
}

// Launch is one launch the tick made (or, with a shadow launcher, would make).
type Launch struct {
	Key, KeyHash, Workflow, LaunchID string
	Shadow                           bool
}

// Wait is a QUEUED item the tick did not launch, and the cap that stopped it.
type Wait struct {
	Key, KeyHash, Cap string
}

type Result struct {
	Launched []Launch
	Waiting  []Wait
	// Held counts the items route left in place, by reason.
	Held map[string]int
	// Problems are failures the next tick retries: unreadable policy, run
	// status or launch errors. The ledger stays consistent.
	Problems []string
}

// Tick runs one Dispatcher pass over items, in place: reconcile, route,
// schedule, launch. The caller has already run the issue finder, and saves
// and lints the ledger afterwards.
func Tick(ctx context.Context, cfg Config, items map[string]*ledger.WorkItem) Result {
	ts := cfg.Now.UTC().Format(time.RFC3339)
	var res Result
	if cfg.Policy == nil || cfg.Launcher == nil {
		why := "policy or launcher unavailable"
		if cfg.PolicyErr != nil {
			why = fmt.Sprintf("policy: %v", cfg.PolicyErr)
		}
		res.Problems = append(res.Problems, why+"; nothing reconciled or launched")
		res.Held = route(items, ts)
		return res
	}
	p := *cfg.Policy
	res.Problems = reconcile(ctx, items, cfg.Launcher, p.Budgets, cfg.Now)
	res.Held = route(items, ts)
	picks, waits := schedule(items, p, cfg.Now)
	for _, c := range waits {
		res.Waiting = append(res.Waiting, Wait{Key: c.item.Key, KeyHash: c.item.KeyHash, Cap: c.cap})
	}
	for _, c := range picks {
		w := c.item
		run := 1
		if w.Attempts != nil {
			run = w.Attempts.Runs + 1
		}
		req := LaunchRequest{LaunchID: launchID(w, run), Workflow: c.lane.Workflow, Args: c.lane.args(w), Timeout: p.Budgets.JobTimeout()}
		if err := cfg.Launcher.Launch(ctx, req); err != nil {
			res.Problems = append(res.Problems, fmt.Sprintf("launch %s for %s: %v", req.LaunchID, w.Key, err))
			continue
		}
		res.Launched = append(res.Launched, Launch{Key: w.Key, KeyHash: w.KeyHash, Workflow: req.Workflow, LaunchID: req.LaunchID, Shadow: cfg.Launcher.Shadow()})
		if !cfg.Launcher.Shadow() {
			claim(w, req, p.Budgets, cfg.Now)
		}
	}
	return res
}

// claim records a started run on the item: QUEUED -> RUNNING.
func claim(w *ledger.WorkItem, req LaunchRequest, b policy.Budgets, now time.Time) {
	if w.Attempts == nil {
		w.Attempts = &ledger.Attempts{MaxRuns: b.MaxRunsPerItem}
	}
	w.Attempts.Runs++
	ts := now.UTC().Format(time.RFC3339)
	w.Claim = &ledger.Claim{LaunchID: req.LaunchID, Workflow: req.Workflow, TS: ts, Deadline: now.Add(req.Timeout).UTC().Format(time.RFC3339)}
	w.Links.ProwJobs = append(w.Links.ProwJobs, req.LaunchID)
	transition(w, ledger.Running, ts, req.LaunchID)
}
