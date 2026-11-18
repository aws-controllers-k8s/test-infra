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
	"sort"
	"time"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/policy"
)

// Caps: why a QUEUED item was not launched this tick. Printed in "waiting"
// lines; the budget names match policy/budgets.yaml.
const (
	CapPaused            = "paused"
	CapRepoPaused        = "repo-paused"
	CapServiceNotEnabled = "service-not-enabled"
	CapRuns              = "max_runs_per_item"
	CapRepoBusy          = "max_concurrent_runs_per_repo"
	CapLLM               = "max_concurrent_llm_runs"
	CapExpensive         = "max_expensive_launches_per_service_per_day"
	CapOpenPRs           = "max_open_prs_per_repo_class"
	CapSDKBumpDaily      = "max_sdk_bump_launches_per_day"
	CapReview            = "max_prs_awaiting_review"
	// CapReviewReserved: an SDK bump left a review slot for a triaged item
	// (policy/triage.yaml reserved_review_slots_triaged; README open
	// decision 8).
	CapReviewReserved = "reserved_review_slots_triaged"
	// CapLane: the lane's own MaxRunning (config-only: 2).
	CapLane = "lane-max-running"
	// CapAborting: policy abort_inflight is set, so nothing launches.
	CapAborting = "abort_inflight"
	// CapNoLane: a QUEUED item of a class no workflow handles. A guard for
	// hand-edited ledgers: route returns no-lane items to TRIAGED before
	// schedule runs.
	CapNoLane = "no-lane"
	// CapObserve: a QUEUED item at autonomy level "observe" never launches.
	CapObserve = "observe"
	// CapNewResourceOff: policy/new-resource.yaml is missing or not enabled.
	CapNewResourceOff = "new-resource-off"
	// CapNewResourceDaily and CapNewResourceOpenPR are policy/new-resource.yaml
	// max_launches_per_day (org-wide) and max_open_prs_per_repo.
	CapNewResourceDaily  = "new-resource max_launches_per_day"
	CapNewResourceOpenPR = "new-resource max_open_prs_per_repo"
	// CapRuntimeRelease: an ack-bot runtime-release PR (head ack-bot/rt-*)
	// opened in the last RuntimeReleaseBackoff is open in the repo (design
	// 5.1 conflict avoidance).
	CapRuntimeRelease = "runtime-release-open"
)

// RuntimeReleaseBackoff bounds the runtime-release back-off: a runtime PR
// older than this no longer holds the repo, so a repo whose runtime PR stays
// open for weeks is not starved (design Appendix A [medium]).
const RuntimeReleaseBackoff = 7 * 24 * time.Hour

// candidate is a QUEUED item schedule picked, with its lane.
type candidate struct {
	item *ledger.WorkItem
	lane lane
}

type capped struct {
	item *ledger.WorkItem
	cap  string
}

// load is what is running, open or recently launched, counted from the ledger.
type load struct {
	runsPerRepo    map[string]int
	llmRuns        int
	laneRuns       map[string]int // workflow
	busyServices   map[string]bool
	openPRs        map[string]int // repo + "|" + class
	awaitingReview int
	launchesToday  map[string]int // class
	expensiveToday map[string]int // service
}

// newLoad counts the ledger's load, plus orphans: runs the launcher sees in
// flight that no item claims. An orphan counts against its workflow's caps and
// keeps its service busy.
func newLoad(items map[string]*ledger.WorkItem, b policy.Budgets, now time.Time, orphans []ActiveJob) *load {
	l := &load{runsPerRepo: map[string]int{}, openPRs: map[string]int{}, launchesToday: map[string]int{},
		expensiveToday: map[string]int{}, laneRuns: map[string]int{}, busyServices: map[string]bool{}}
	for _, o := range orphans {
		l.laneRuns[o.Workflow]++
		if llmWorkflow(o.Workflow) {
			l.llmRuns++
		}
		l.busyServices[o.Service] = true
	}
	dayAgo := now.Add(-24 * time.Hour)
	for _, w := range items {
		if w.State.ActiveRun() {
			l.runsPerRepo[w.Repo]++
			if w.Claim != nil {
				l.laneRuns[w.Claim.Workflow]++
				if llmWorkflow(w.Claim.Workflow) {
					l.llmRuns++
				}
			}
			// The run will open a PR.
			l.openPRs[w.Repo+"|"+w.ChangeClass]++
		}
		if w.State.OpenPR() {
			l.openPRs[w.Repo+"|"+w.ChangeClass]++
		}
		// A run of a human-merge or draft item will open a PR that needs a
		// human, so active runs count against review backpressure too.
		if needsReview(w) && (w.State.ActiveRun() || (w.State.OpenPR() && w.State != ledger.MergeSoak)) {
			l.awaitingReview++
		}
		for _, h := range w.History {
			if h.To != ledger.Running {
				continue
			}
			ts, err := time.Parse(time.RFC3339, h.TS)
			if err != nil || !ts.After(dayAgo) {
				continue
			}
			l.launchesToday[w.ChangeClass]++
			if b.Expensive(w.Service) {
				l.expensiveToday[w.Service]++
			}
		}
	}
	return l
}

func (l *load) add(w *ledger.WorkItem, ln lane, b policy.Budgets) {
	l.runsPerRepo[w.Repo]++
	l.laneRuns[ln.Workflow]++
	if ln.LLM {
		l.llmRuns++
	}
	l.openPRs[w.Repo+"|"+w.ChangeClass]++
	l.launchesToday[w.ChangeClass]++
	if b.Expensive(w.Service) {
		l.expensiveToday[w.Service]++
	}
	if needsReview(w) {
		l.awaitingReview++
	}
}

// needsReview reports whether the item's PR waits for a person: human-merge,
// and draft (new-resource PRs get their API review on the draft).
func needsReview(w *ledger.WorkItem) bool {
	return w.AutonomyLevel == "human-merge" || w.AutonomyLevel == "draft"
}

// schedule picks the QUEUED items to launch this tick, in launch order, and
// says why each other QUEUED item waits. It changes no item.
func schedule(items map[string]*ledger.WorkItem, p policy.Policy, now time.Time) ([]candidate, []capped) {
	return scheduleWith(items, p, now, nil, nil)
}

// scheduleWith is schedule with the launcher's view of what is in flight:
// orphans count against the caps, and adopt holds the launch ids of runs
// already in flight for a QUEUED item's next run (a launch whose ledger push
// was lost). Such an item is weighed first and skips the load caps: Launch
// adopts the running job, so picking it starts nothing, and its run must be
// counted before anything else is weighed.
func scheduleWith(items map[string]*ledger.WorkItem, p policy.Policy, now time.Time, orphans []ActiveJob, adopt map[string]bool) ([]candidate, []capped) {
	return scheduleHeld(items, p, now, orphans, adopt, nil)
}

// scheduleHeld is scheduleWith plus the repos where a runtime-release PR is
// open (CapRuntimeRelease).
func scheduleHeld(items map[string]*ledger.WorkItem, p policy.Policy, now time.Time, orphans []ActiveJob, adopt map[string]bool, runtimeRelease map[string]bool) ([]candidate, []capped) {
	sw, b := p.Switches, p.Budgets
	lastVisit := map[string]string{}
	var queue []*ledger.WorkItem
	for _, w := range items {
		for _, h := range w.History {
			if h.To == ledger.Running && h.TS > lastVisit[w.Repo] {
				lastVisit[w.Repo] = h.TS
			}
		}
		if w.State == ledger.Queued {
			queue = append(queue, w)
		}
	}
	sort.Slice(queue, func(i, j int) bool {
		a, c := queue[i], queue[j]
		if a.PriorityBand != c.PriorityBand {
			return a.PriorityBand < c.PriorityBand
		}
		if qa, qc := queuedSince(a), queuedSince(c); qa != qc {
			// No QUEUED history sorts after any timestamp.
			if qa == "" || qc == "" {
				return qc == ""
			}
			return qa < qc
		}
		if va, vc := lastVisit[a.Repo], lastVisit[c.Repo]; va != vc {
			return va < vc
		}
		return a.Key < c.Key
	})
	sort.SliceStable(queue, func(i, j int) bool { return adopt[nextLaunchID(queue[i])] && !adopt[nextLaunchID(queue[j])] })

	l := newLoad(items, b, now, orphans)
	// While a triaged item with a lane waits, SDK bumps (band 3) leave the
	// reserved review slots to it (band 4).
	reserve := 0
	if p.Triage != nil {
		for _, w := range queue {
			if _, ok := lanes[w.ChangeClass]; ok && isTriageItem(w) {
				reserve = p.Triage.ReservedReviewSlotsTriaged
				break
			}
		}
	}
	var picks []candidate
	var waits []capped
	for _, w := range queue {
		ln, ok := lanes[w.ChangeClass]
		if !ok {
			waits = append(waits, capped{w, CapNoLane})
			continue
		}
		if w.AutonomyLevel == "observe" {
			waits = append(waits, capped{w, CapObserve})
			continue
		}
		// Bands 0-2 skip the per-class caps (design 8).
		perClass := w.PriorityBand > 2
		cap := ""
		runs, maxRuns := 0, b.MaxRunsPerItem
		if w.Attempts != nil {
			runs = w.Attempts.Runs
			if w.Attempts.MaxRuns >= 1 {
				maxRuns = w.Attempts.MaxRuns
			}
		}
		switch {
		case sw.Paused:
			cap = CapPaused
		case sw.AbortInflight:
			cap = CapAborting
		case sw.RepoPaused(w.Repo):
			cap = CapRepoPaused
		case !sw.ServiceEnabled(w.Service):
			cap = CapServiceNotEnabled
		case runs >= maxRuns:
			cap = CapRuns
		case w.ChangeClass == "new-resource" && !p.NewResourceEnabled():
			cap = CapNewResourceOff
		case adopt[nextLaunchID(w)]:
			// Already running: no load cap applies.
		case l.runsPerRepo[w.Repo] >= b.MaxConcurrentRunsPerRepo || l.busyServices[w.Service]:
			cap = CapRepoBusy
		case runtimeRelease[w.Repo]:
			cap = CapRuntimeRelease
		// The owner's new-resource caps hold in every band.
		case w.ChangeClass == "new-resource" && l.launchesToday["new-resource"] >= p.NewResource.MaxLaunchesPerDay:
			cap = CapNewResourceDaily
		case w.ChangeClass == "new-resource" && l.openPRs[w.Repo+"|new-resource"] >= p.NewResource.MaxOpenPRsPerRepo:
			cap = CapNewResourceOpenPR
		case ln.LLM && l.llmRuns >= b.MaxConcurrentLLMRuns:
			cap = CapLLM
		case ln.MaxRunning > 0 && l.laneRuns[ln.Workflow] >= ln.MaxRunning:
			cap = CapLane
		case b.Expensive(w.Service) && l.expensiveToday[w.Service] >= b.MaxExpensiveLaunchesPerDay:
			cap = CapExpensive
		case perClass && l.openPRs[w.Repo+"|"+w.ChangeClass] >= b.MaxOpenPRsPerRepoClass:
			cap = CapOpenPRs
		case perClass && w.ChangeClass == "SDK-bump" && l.launchesToday["SDK-bump"] >= b.MaxSDKBumpLaunchesPerDay:
			cap = CapSDKBumpDaily
		case needsReview(w) && l.awaitingReview >= b.MaxPRsAwaitingReview:
			cap = CapReview
		case w.ChangeClass == "SDK-bump" && w.AutonomyLevel == "human-merge" && reserve > 0 &&
			l.awaitingReview >= b.MaxPRsAwaitingReview-reserve:
			cap = CapReviewReserved
		}
		if cap != "" {
			waits = append(waits, capped{w, cap})
			continue
		}
		picks = append(picks, candidate{w, ln})
		l.add(w, ln, b)
	}
	return picks, waits
}

// nextLaunchID is the launch id of the item's next run.
func nextLaunchID(w *ledger.WorkItem) string {
	run := 1
	if w.Attempts != nil {
		run = w.Attempts.Runs + 1
	}
	return launchID(w, run)
}

// queuedSince is when the item last entered QUEUED.
func queuedSince(w *ledger.WorkItem) string {
	for i := len(w.History) - 1; i >= 0; i-- {
		if w.History[i].To == ledger.Queued {
			return w.History[i].TS
		}
	}
	return ""
}

// llmWorkflow reports whether any lane running the named workflow is an LLM
// lane.
func llmWorkflow(name string) bool {
	for _, ln := range lanes {
		if ln.Workflow == name && ln.LLM {
			return true
		}
	}
	return false
}
