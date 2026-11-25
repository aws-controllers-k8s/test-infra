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
	"sort"
	"strings"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

// Hold reasons: why route left an item where it is.
const (
	HoldObserve              = "observe"
	HoldAwaitingTriage       = "awaiting-triage"
	HoldAwaitingPlanWorkflow = "awaiting-plan-workflow"
	HoldAwaitingPlanApproval = "awaiting-plan-approval"
	HoldAwaitingWorkflow     = "awaiting-workflow"
	HoldBlockedOther         = "blocked-other"
	// HoldBlockedMissingDependency: a depends_on key is not in the ledger.
	HoldBlockedMissingDependency = "blocked-missing-dependency"
)

// route moves pre-dispatch items along DETECTED -> TRIAGED -> QUEUED or
// BLOCKED (design 5.4). Needs-config items wait in DETECTED for a triage run
// and in TRIAGED for a maintainer's decision (triageHold). Items of a class no workflow handles are held as
// "awaiting-workflow" in TRIAGED; route escalates only items whose dependency
// is dead. It returns how many items it held, by reason. It writes a history entry only
// when an item's state changes.
func route(items map[string]*ledger.WorkItem, ts string) map[string]int {
	held := map[string]int{}
	for _, k := range sortedKeys(items) {
		w := items[k]
		if !w.State.PreDispatch() {
			continue
		}
		switch {
		case w.AutonomyLevel == "observe":
			held[HoldObserve]++
			continue
		case w.State == ledger.PlanPending:
			held[HoldAwaitingPlanApproval]++
			continue
		case isTriageItem(w):
			// Mode-api triage (Plan 8): held until a maintainer's reply
			// decides; a decided item moves on below.
			if hold := triageHold(w, ts); hold != "" {
				held[hold]++
				continue
			}
		case w.State == ledger.Detected && w.ChangeClass == "new-resource":
			held[HoldAwaitingPlanWorkflow]++
			continue
		case w.State == ledger.Detected && !skipsTriage[w.ChangeClass]:
			held[HoldAwaitingTriage]++
			continue
		case w.State == ledger.Detected:
			transition(w, ledger.Triaged, ts, "NO_TRIAGE_NEEDED")
		}
		// TRIAGED, QUEUED or BLOCKED from here.
		if _, ok := lanes[w.ChangeClass]; !ok {
			// No workflow yet: keep the item with the issue finder, out of
			// schedule's reach.
			held[HoldAwaitingWorkflow]++
			if w.State != ledger.Triaged {
				transition(w, ledger.Triaged, ts, "NO_WORKFLOW")
			}
			continue
		}
		why, dead, missing := unmetDependency(w, items)
		if dead {
			w.Verdict = &ledger.Verdict{Outcome: "needs_human", Reason: "DEPENDENCY_DEAD"}
			transition(w, ledger.Escalated, ts, "DEPENDENCY_DEAD "+why)
			continue
		}
		if why != "" {
			if missing {
				held[HoldBlockedMissingDependency]++
			}
			if w.State != ledger.Blocked {
				transition(w, ledger.Blocked, ts, "DEPENDS_ON "+why)
			}
			continue
		}
		switch w.State {
		case ledger.Blocked:
			if !blockedByDependency(w) {
				held[HoldBlockedOther]++
				continue
			}
			transition(w, ledger.Queued, ts, "DEPENDENCIES_MET")
		case ledger.Triaged:
			transition(w, ledger.Queued, ts, "READY")
		}
	}
	return held
}

// blockedByDependency reports whether the transition that last moved w to
// BLOCKED was route's own dependency block. Other writers (env retry,
// back-off) also use BLOCKED, and route must not release those.
func blockedByDependency(w *ledger.WorkItem) bool {
	for i := len(w.History) - 1; i >= 0; i-- {
		if w.History[i].To == ledger.Blocked {
			return strings.HasPrefix(w.History[i].Reason, "DEPENDS_ON ")
		}
	}
	return false
}

// unmetDependency describes the first dependency that has not landed, or
// returns "" when all have. dead is true when that dependency can never land:
// it is ESCALATED, SUPERSEDED or WONT_FIX. missing is true when the unmet
// dependency is not in the ledger at all.
func unmetDependency(w *ledger.WorkItem, items map[string]*ledger.WorkItem) (why string, dead, missing bool) {
	for _, d := range w.DependsOn {
		dep, ok := items[d]
		switch {
		case !ok:
			return ledger.KeyHash(d) + " missing", false, true
		case dep.State.Landed():
		default:
			dead = dep.State == ledger.Escalated || dep.State.Terminal()
			return fmt.Sprintf("%s %s", dep.KeyHash, dep.State), dead, false
		}
	}
	return "", false, false
}

func transition(w *ledger.WorkItem, to ledger.State, ts, reason string) {
	w.State = to
	w.History = append(w.History, ledger.Transition{TS: ts, To: to, By: actor, Reason: reason})
}

func sortedKeys(items map[string]*ledger.WorkItem) []string {
	keys := make([]string, 0, len(items))
	for k := range items {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
