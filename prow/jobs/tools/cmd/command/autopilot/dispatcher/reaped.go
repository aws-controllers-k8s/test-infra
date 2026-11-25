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
// The dispatch command prints an alarm line above ReapedAlarmThreshold; Plan
// 10 publishes the count as WorkItemsReaped.
func ReapedToday(items map[string]*ledger.WorkItem, now time.Time) int {
	dayAgo := now.Add(-24 * time.Hour)
	n := 0
	for _, w := range items {
		for i, h := range w.History {
			ts, err := time.Parse(time.RFC3339, h.TS)
			if err != nil || !ts.After(dayAgo) || ts.After(now) {
				continue
			}
			switch {
			case h.Reason == "RETRY INFRA_NO_VERDICT":
				n++
			case h.Reason == "ATTEMPTS_EXHAUSTED" && i == len(w.History)-1 && w.Verdict != nil && w.Verdict.Reason == "INFRA_NO_VERDICT":
				n++
			}
		}
	}
	return n
}
