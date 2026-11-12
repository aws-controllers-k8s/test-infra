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
)

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
	openPRs        map[string]int // repo + "|" + class
	awaitingReview int
	launchesToday  map[string]int // class
	expensiveToday map[string]int // service
}

func newLoad(items map[string]*ledger.WorkItem, b policy.Budgets, now time.Time) *load {
	l := &load{runsPerRepo: map[string]int{}, openPRs: map[string]int{}, launchesToday: map[string]int{}, expensiveToday: map[string]int{}}
	dayAgo := now.Add(-24 * time.Hour)
	for _, w := range items {
		if w.State.ActiveRun() {
			l.runsPerRepo[w.Repo]++
			if lanes[w.ChangeClass].LLM {
				l.llmRuns++
			}
		}
		if w.State.OpenPR() {
			l.openPRs[w.Repo+"|"+w.ChangeClass]++
		}
		// A run of a human-merge item will open a PR that needs a human, so
		// active runs count against review backpressure too.
		if w.AutonomyLevel == "human-merge" && (w.State.ActiveRun() || (w.State.OpenPR() && w.State != ledger.MergeSoak)) {
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
	if ln.LLM {
		l.llmRuns++
	}
	l.launchesToday[w.ChangeClass]++
	if b.Expensive(w.Service) {
		l.expensiveToday[w.Service]++
	}
	if w.AutonomyLevel == "human-merge" {
		l.awaitingReview++
	}
}

// schedule picks the QUEUED items to launch this tick, in launch order, and
// says why each other QUEUED item waits. It changes no item.
func schedule(items map[string]*ledger.WorkItem, p policy.Policy, now time.Time) ([]candidate, []capped) {
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
			return qa < qc
		}
		if va, vc := lastVisit[a.Repo], lastVisit[c.Repo]; va != vc {
			return va < vc
		}
		return a.Key < c.Key
	})

	l := newLoad(items, b, now)
	var picks []candidate
	var waits []capped
	for _, w := range queue {
		ln, ok := lanes[w.ChangeClass]
		if !ok {
			// route escalates these; a hand-edited ledger may still hold one.
			continue
		}
		// Bands 0-2 skip the per-class caps (design 8).
		perClass := w.PriorityBand > 2
		cap := ""
		switch {
		case sw.Paused:
			cap = CapPaused
		case sw.RepoPaused(w.Repo):
			cap = CapRepoPaused
		case !sw.ServiceEnabled(w.Service):
			cap = CapServiceNotEnabled
		case w.Attempts != nil && w.Attempts.Runs >= w.Attempts.MaxRuns:
			cap = CapRuns
		case l.runsPerRepo[w.Repo] >= b.MaxConcurrentRunsPerRepo:
			cap = CapRepoBusy
		case ln.LLM && l.llmRuns >= b.MaxConcurrentLLMRuns:
			cap = CapLLM
		case b.Expensive(w.Service) && l.expensiveToday[w.Service] >= b.MaxExpensiveLaunchesPerDay:
			cap = CapExpensive
		case perClass && l.openPRs[w.Repo+"|"+w.ChangeClass] >= b.MaxOpenPRsPerRepoClass:
			cap = CapOpenPRs
		case perClass && w.ChangeClass == "SDK-bump" && l.launchesToday["SDK-bump"] >= b.MaxSDKBumpLaunchesPerDay:
			cap = CapSDKBumpDaily
		case w.AutonomyLevel == "human-merge" && l.awaitingReview >= b.MaxPRsAwaitingReview:
			cap = CapReview
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

// queuedSince is when the item last entered QUEUED.
func queuedSince(w *ledger.WorkItem) string {
	for i := len(w.History) - 1; i >= 0; i-- {
		if w.History[i].To == ledger.Queued {
			return w.History[i].TS
		}
	}
	return ""
}
