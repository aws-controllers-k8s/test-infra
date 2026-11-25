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
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

// Counts is what one tick did (Plan 10 metrics), read from the history
// entries the tick appended: Tick only ever appends to History.
type Counts struct {
	// GateFirstPass and GateFirstFail count items whose first valid ack-dod
	// verdict (FirstGateVerdict) was recorded this tick.
	GateFirstPass, GateFirstFail int
	// RunsCompleted and RunsFailed count workflow runs reconciled this tick
	// (RunEnd); triage and dod runs are not workflow runs.
	RunsCompleted, RunsFailed int
	// ItemsEscalated counts moves into ESCALATED this tick.
	ItemsEscalated int
}

// historyLens records each item's history length before a tick.
func historyLens(items map[string]*ledger.WorkItem) map[string]int {
	out := make(map[string]int, len(items))
	for k, w := range items {
		out[k] = len(w.History)
	}
	return out
}

// countTick counts the entries appended since before (an item missing from
// before is new: all its entries count).
func countTick(before map[string]int, items map[string]*ledger.WorkItem) Counts {
	var c Counts
	for k, w := range items {
		n := before[k]
		if n > len(w.History) {
			// History shrank: not something a tick does. Count nothing.
			continue
		}
		for i := n; i < len(w.History); i++ {
			switch RunEnd(w.History, i) {
			case RunCompleted:
				c.RunsCompleted++
			case RunFailed:
				c.RunsFailed++
			}
			if w.History[i].To == ledger.Escalated {
				c.ItemsEscalated++
			}
		}
		if f := FirstGateVerdict(w.History); f >= n {
			if pass, _ := GateVerdictAt(w.History, f); pass {
				c.GateFirstPass++
			} else {
				c.GateFirstFail++
			}
		}
	}
	return c
}
