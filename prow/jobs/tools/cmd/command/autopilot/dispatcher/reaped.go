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
	"time"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

// ReapedAlarmThreshold is design 10's WorkItemsReaped alarm: more than 2 a
// day.
const ReapedAlarmThreshold = 2

// ReapedToday counts runs reaped with no verdict (INFRA_NO_VERDICT, design
// 5.4 reaper) in the 24 hours before now: retried ones (history reason
// "RETRY INFRA_NO_VERDICT") and the ones that exhausted the item's attempts.
// The dispatch command prints an alarm line above ReapedAlarmThreshold and
// publishes the count as WorkItemsReapedToday (Plan 10).
func ReapedToday(items map[string]*ledger.WorkItem, now time.Time) int {
	return len(ReapedBetween(items, now.Add(-24*time.Hour), now))
}

// Reap is one reaped run: items[Key].History[Index] recorded it.
type Reap struct {
	Key   string
	Index int
}

// ReapedBetween lists the runs ReapedToday counts, reaped after from and at
// or before to, in no particular order.
func ReapedBetween(items map[string]*ledger.WorkItem, from, to time.Time) []Reap {
	var out []Reap
	for k, w := range items {
		for i, h := range w.History {
			ts, err := time.Parse(time.RFC3339, h.TS)
			if err != nil || !ts.After(from) || ts.After(to) {
				continue
			}
			switch {
			case h.Reason == "RETRY INFRA_NO_VERDICT":
				out = append(out, Reap{Key: k, Index: i})
			case h.Reason == "ATTEMPTS_EXHAUSTED" && i == len(w.History)-1 && w.Verdict != nil && w.Verdict.Reason == "INFRA_NO_VERDICT":
				out = append(out, Reap{Key: k, Index: i})
			}
		}
	}
	return out
}
