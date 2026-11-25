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
	"cmp"
	"slices"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

// TriageStat counts one (mode, detector work, decision): the proposals made
// and how maintainers answered them. Accepted/(Accepted+Rejected) is
// TriageAcceptRate (design 10), which earns auto-accept per (mode, decision)
// in a later phase; Invalid counts answers the firewall turned into
// undecided (UntrustedInputRejected). Plan 10 publishes them. The detector
// work is in the key so README open decision 1 (not-generated) gets its own
// numbers.
type TriageStat struct {
	Mode, Work, Decision                              string
	Proposed, Invalid, Accepted, Rejected, Overridden int
}

// TriageStats counts every item's triage log. Each proposal's last reply
// counts once: a bare reject followed by a deciding reject is one rejection.
func TriageStats(items map[string]*ledger.WorkItem) []TriageStat {
	by := map[[3]string]*TriageStat{}
	get := func(mode, work, decision string) *TriageStat {
		k := [3]string{mode, work, decision}
		if by[k] == nil {
			by[k] = &TriageStat{Mode: mode, Work: work, Decision: decision}
		}
		return by[k]
	}
	for _, w := range items {
		t := w.Triage
		if t == nil {
			continue
		}
		final := map[string]ledger.TriageEvent{}
		for _, e := range t.Log {
			switch e.Event {
			case ledger.TriageProposed:
				s := get(t.Mode, w.DetectorWork, e.Decision)
				s.Proposed++
				if e.Invalid {
					s.Invalid++
				}
			case ledger.ReviewAccepted, ledger.ReviewRejected:
				final[e.LaunchID] = e
			}
		}
		for _, e := range final {
			s := get(t.Mode, w.DetectorWork, e.Decision)
			if e.Event == ledger.ReviewAccepted {
				s.Accepted++
				continue
			}
			s.Rejected++
			if e.Override != "" {
				s.Overridden++
			}
		}
	}
	out := make([]TriageStat, 0, len(by))
	for _, s := range by {
		out = append(out, *s)
	}
	slices.SortFunc(out, func(a, b TriageStat) int {
		return cmp.Or(cmp.Compare(a.Mode, b.Mode), cmp.Compare(a.Work, b.Work), cmp.Compare(a.Decision, b.Decision))
	})
	return out
}
