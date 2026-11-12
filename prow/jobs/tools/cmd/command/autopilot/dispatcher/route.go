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

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

// Hold reasons: why route left an item where it is.
const (
	HoldObserve              = "observe"
	HoldAwaitingTriage       = "awaiting-triage"
	HoldAwaitingPlanWorkflow = "awaiting-plan-workflow"
	HoldAwaitingPlanApproval = "awaiting-plan-approval"
)

// route moves pre-dispatch items along DETECTED -> TRIAGED -> QUEUED or
// BLOCKED (design 5.4) and escalates triaged items no workflow can do. It
// returns how many items it held, by reason. It writes a history entry only
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
			w.Verdict = &ledger.Verdict{Outcome: "needs_human", Reason: "NO_WORKFLOW"}
			transition(w, ledger.Escalated, ts, "NO_WORKFLOW")
			continue
		}
		if why := unmetDependency(w, items); why != "" {
			if w.State != ledger.Blocked {
				transition(w, ledger.Blocked, ts, "DEPENDS_ON "+why)
			}
			continue
		}
		switch w.State {
		case ledger.Blocked:
			transition(w, ledger.Queued, ts, "DEPENDENCIES_MET")
		case ledger.Triaged:
			transition(w, ledger.Queued, ts, "READY")
		}
	}
	return held
}

// unmetDependency describes the first dependency that has not landed, or
// returns "" when all have.
func unmetDependency(w *ledger.WorkItem, items map[string]*ledger.WorkItem) string {
	for _, d := range w.DependsOn {
		dep, ok := items[d]
		switch {
		case !ok:
			return ledger.KeyHash(d) + " missing"
		case !dep.State.Landed():
			return fmt.Sprintf("%s %s", dep.KeyHash, dep.State)
		}
	}
	return ""
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
