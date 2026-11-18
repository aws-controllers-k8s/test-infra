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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/issuefinder"
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
	// DryRun makes the tick launch and abort through a ShadowLauncher whatever
	// Launcher is, so a dry run can never start or stop anything. Reconcile
	// still reads run status, and the inventory, through Launcher.
	DryRun bool
	// TriageCommands are maintainers' /autopilot triage replies
	// (issuefinder.TriageCommands), oldest first.
	TriageCommands []issuefinder.TriageCommand
	// GitHub reads the PRs items link to (Plan 9). Nil skips PR tracking,
	// gating and the runtime-release back-off; the dispatch command always
	// sets it.
	GitHub PRClient
	// PRWriter marks gated drafts ready; nil leaves them draft (PR writes
	// off). A dry run never uses it.
	PRWriter PRWriter
	// Team are the logins whose PR reviews count as feedback.
	Team map[string]bool
	// PRAuthors maps a lane's workflow (sdk-bump, add-field, add-resource)
	// to the login its PRs must be opened by.
	PRAuthors map[string]string
	// BotLogins are the logins whose commits are not human.
	BotLogins []string
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
	// Skipped explains each triage reply the tick ignored.
	Skipped []string
	// TriageOff: the policy has no triage lane for mode api.
	TriageOff bool
	// TriageStats are the TriageAcceptRate inputs (design 10), for Plan 10.
	TriageStats []TriageStat
	// Reaped is ReapedToday after the tick (design 10 WorkItemsReaped).
	Reaped int
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
	launcher := cfg.Launcher
	if cfg.DryRun {
		launcher = &dryRun{real: cfg.Launcher}
	}
	res.Problems = reconcile(ctx, items, launcher, p.Budgets, cfg.Now)
	var runtimeRelease map[string]bool
	if cfg.GitHub != nil {
		e := prEnv{gh: cfg.GitHub, l: launcher, team: cfg.Team, authors: cfg.PRAuthors, bots: cfg.BotLogins,
			sw: p.Switches, grace: p.Budgets.ReaperGrace(), now: cfg.Now, ts: ts, launched: &res.Launched}
		if !cfg.DryRun {
			e.pw = cfg.PRWriter
		}
		res.Problems = append(res.Problems, trackPRs(ctx, items, e)...)
		res.Problems = append(res.Problems, gate(ctx, items, e)...)
	}
	res.Problems = append(res.Problems, reconcileTriage(ctx, items, launcher, p, cfg.Now)...)
	if p.Switches.AbortInflight {
		res.Problems = append(res.Problems, abortInflight(ctx, items, launcher, ts)...)
		res.Problems = append(res.Problems, abortInflightTriage(ctx, items, launcher, p, ts)...)
	}
	res.Skipped = applyTriageCommands(items, cfg.TriageCommands, ts)
	res.Held = route(items, ts)
	if cfg.GitHub != nil {
		var problems []string
		runtimeRelease, problems = runtimeReleaseRepos(ctx, cfg.GitHub, items, cfg.Now)
		res.Problems = append(res.Problems, problems...)
	}
	res.Reaped = ReapedToday(items, cfg.Now)
	// Set before the inventory read, whose failure returns early.
	res.TriageOff = !triageEnabled(p)
	res.TriageStats = TriageStats(items)
	orphans, adopt, err := findOrphans(ctx, cfg.Launcher, items, p.Switches.AbortInflight)
	if err != nil {
		// Without the inventory the caps cannot be trusted: launch nothing.
		res.Problems = append(res.Problems, fmt.Sprintf("inventory: %v; nothing launched", err))
		return res
	}
	for _, o := range orphans {
		res.Problems = append(res.Problems, fmt.Sprintf("orphan run %s (%s) is in flight and no item claims it; it counts against the caps", o.LaunchID, o.Workflow))
		if p.Switches.AbortInflight && !launcher.Shadow() {
			if err := launcher.Abort(ctx, o.LaunchID); err != nil {
				res.Problems = append(res.Problems, fmt.Sprintf("abort orphan %s: %v", o.LaunchID, err))
			}
		}
	}
	// Triage runs in flight that no item claims fill triage slots, not the
	// workflow caps: triage opens no PR and holds no repo (decision 10).
	var wfOrphans []ActiveJob
	triageOrphans := 0
	for _, o := range orphans {
		if o.Workflow == triageWorkflow {
			triageOrphans++
			continue
		}
		wfOrphans = append(wfOrphans, o)
	}
	picks, waits := scheduleHeld(items, p, cfg.Now, wfOrphans, adopt, runtimeRelease)
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
		if err := launcher.Launch(ctx, req); err != nil {
			res.Problems = append(res.Problems, fmt.Sprintf("launch %s for %s: %v", req.LaunchID, w.Key, err))
			continue
		}
		res.Launched = append(res.Launched, Launch{Key: w.Key, KeyHash: w.KeyHash, Workflow: req.Workflow, LaunchID: req.LaunchID, Shadow: launcher.Shadow()})
		if !launcher.Shadow() {
			claim(w, req, p.Budgets, cfg.Now)
		}
	}
	res.Problems = append(res.Problems, refreshBumps(ctx, items, launcher, p.Switches, &res)...)
	// The triage lane (design 5.1): tool-less runs that only propose.
	tpicks, twaits := scheduleTriageWith(items, p, cfg.Now, triageOrphans, adopt)
	for _, c := range twaits {
		res.Waiting = append(res.Waiting, Wait{Key: c.item.Key, KeyHash: c.item.KeyHash, Cap: c.cap})
	}
	for _, c := range tpicks {
		w := c.item
		req := LaunchRequest{LaunchID: nextTriageLaunchID(w), Workflow: triageWorkflow, Args: c.args, Timeout: p.Triage.JobTimeout()}
		if err := launcher.Launch(ctx, req); err != nil {
			res.Problems = append(res.Problems, fmt.Sprintf("launch %s for %s: %v", req.LaunchID, w.Key, err))
			continue
		}
		res.Launched = append(res.Launched, Launch{Key: w.Key, KeyHash: w.KeyHash, Workflow: req.Workflow, LaunchID: req.LaunchID, Shadow: launcher.Shadow()})
		if !launcher.Shadow() {
			startTriage(w, req, cfg.Now)
		}
	}
	return res
}

// NextLaunch is the request Tick would make for a QUEUED item of a class with
// a lane, ignoring caps. autopilot launch-preview prints the job it builds.
func NextLaunch(w *ledger.WorkItem, b policy.Budgets) (LaunchRequest, bool) {
	ln, ok := lanes[w.ChangeClass]
	if !ok || w.State != ledger.Queued {
		return LaunchRequest{}, false
	}
	return LaunchRequest{LaunchID: nextLaunchID(w), Workflow: ln.Workflow, Args: ln.args(w), Timeout: b.JobTimeout()}, true
}

// findOrphans sorts the runs the launcher sees in flight: a run that is the
// next run of a QUEUED item, or of a triage candidate, goes to adopt (that
// item's next Launch adopts it); a run no item claims is an orphan. Refresh
// runs are neither. With aborting set (policy abort_inflight) nothing is
// adopted, since scheduling launches nothing: such a run is an orphan, so Tick
// aborts it instead of leaving it to run on. A shadow launcher, or one without
// an inventory, sees none.
func findOrphans(ctx context.Context, l Launcher, items map[string]*ledger.WorkItem, aborting bool) ([]ActiveJob, map[string]bool, error) {
	inv, ok := l.(Inventory)
	if !ok || l.Shadow() {
		return nil, nil, nil
	}
	active, err := inv.Active(ctx)
	if err != nil {
		return nil, nil, err
	}
	claimed, next := map[string]bool{}, map[string]bool{}
	for _, w := range items {
		if w.Claim != nil {
			claimed[w.Claim.LaunchID] = true
		}
		// A triage run lives in w.Triage.Claim, not w.Claim (Plan 8).
		if w.Triage != nil && w.Triage.Claim != nil {
			claimed[w.Triage.Claim.LaunchID] = true
		}
		if w.State == ledger.Queued {
			next[nextLaunchID(w)] = true
		}
		// A triage candidate's next run in flight is a lost ledger push:
		// scheduleTriageWith adopts it.
		if triageCandidate(w) {
			next[nextTriageLaunchID(w)] = true
		}
	}
	var orphans []ActiveJob
	adopt := map[string]bool{}
	for _, a := range active {
		switch {
		case a.Refresh, claimed[a.LaunchID]:
		case next[a.LaunchID] && !aborting:
			adopt[a.LaunchID] = true
		default:
			orphans = append(orphans, a)
		}
	}
	sort.Slice(orphans, func(i, j int) bool { return orphans[i].LaunchID < orphans[j].LaunchID })
	return orphans, adopt, nil
}

// abortInflight stops every run in flight (policy abort_inflight) and
// escalates its item. A shadow or dry-run launcher stops nothing, so the items
// stay as they are and one problem line says how many would be stopped.
func abortInflight(ctx context.Context, items map[string]*ledger.WorkItem, l Launcher, ts string) []string {
	var active []*ledger.WorkItem
	for _, k := range sortedKeys(items) {
		if w := items[k]; w.State.ActiveRun() && w.Claim != nil {
			active = append(active, w)
		}
	}
	if len(active) == 0 {
		return nil
	}
	if l.Shadow() {
		for _, w := range active {
			_ = l.Abort(ctx, w.Claim.LaunchID)
		}
		return []string{fmt.Sprintf("abort_inflight: %d run(s) in flight; this launcher aborts nothing", len(active))}
	}
	var problems []string
	for _, w := range active {
		if err := l.Abort(ctx, w.Claim.LaunchID); err != nil {
			problems = append(problems, fmt.Sprintf("abort %s for %s: %v", w.Claim.LaunchID, w.Key, err))
			continue
		}
		finish(w, ledger.Escalated, ts, "ABORTED_BY_POLICY", &ledger.Verdict{Outcome: OutcomeNeedsHuman, Reason: "ABORTED_BY_POLICY"})
	}
	return problems
}

// refreshTimeout bounds an sdk-bump --mode refresh run (no regeneration).
const refreshTimeout = 30 * time.Minute

// refreshBumps starts an sdk-bump --mode refresh run (Plan 6) for every
// SDK-bump item with an open PR whose server-default dependents (items that
// depend on it with detector_work server-default) are in states no refresh
// has seen yet. The launch id is derived from those states, so each set of
// states gets one refresh, recorded in the bump's links.prowjobs. Refresh
// runs take no claim, charge no attempt, and are not reconciled. The first
// refresh after the PR opens repeats what the bump run already did; that is
// harmless.
func refreshBumps(ctx context.Context, items map[string]*ledger.WorkItem, l Launcher, sw policy.Switches, res *Result) []string {
	deps := map[string][]*ledger.WorkItem{}
	for _, w := range items {
		if w.DetectorWork != "server-default" {
			continue
		}
		for _, d := range w.DependsOn {
			deps[d] = append(deps[d], w)
		}
	}
	var problems []string
	for _, k := range sortedKeys(items) {
		b := items[k]
		if b.ChangeClass != "SDK-bump" || !b.State.OpenPR() || b.Links.PR == "" || len(deps[b.Key]) == 0 {
			continue
		}
		if sw.Paused || sw.AbortInflight || sw.RepoPaused(b.Repo) || !sw.ServiceEnabled(b.Service) {
			continue
		}
		id := refreshID(b, deps[b.Key])
		if slices.Contains(b.Links.ProwJobs, id) {
			continue
		}
		args := lanes["SDK-bump"].args(b)
		args["mode"] = "refresh"
		req := LaunchRequest{LaunchID: id, Workflow: "sdk-bump", Args: args, Timeout: refreshTimeout}
		if err := l.Launch(ctx, req); err != nil {
			problems = append(problems, fmt.Sprintf("launch refresh %s for %s: %v", id, b.Key, err))
			continue
		}
		res.Launched = append(res.Launched, Launch{Key: b.Key, KeyHash: b.KeyHash, Workflow: "sdk-bump", LaunchID: id, Shadow: l.Shadow()})
		if !l.Shadow() {
			b.Links.ProwJobs = append(b.Links.ProwJobs, id)
		}
	}
	return problems
}

// refreshID names the refresh of bump b for its dependents' current states:
// autopilot-<key hash>-g<generation>-f<8 hex of the states>.
func refreshID(b *ledger.WorkItem, deps []*ledger.WorkItem) string {
	lines := make([]string, 0, len(deps))
	for _, d := range deps {
		lines = append(lines, ledger.KeyHash(d.Key)+"="+string(d.State))
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return fmt.Sprintf("autopilot-%s-g%d-f%s", strings.TrimPrefix(ledger.KeyHash(b.Key), "sha256:"), b.Generation, hex.EncodeToString(sum[:])[:8])
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
