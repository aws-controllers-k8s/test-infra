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

// Package viewstatus renders the Dispatcher's status comment on an api-change
// issue and plans the one edit that keeps it current. The detector stays the
// only writer of the issue body (design 4.5).
package viewstatus

import (
	"fmt"
	"slices"
	"strings"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/issuefinder"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

// TriageMarker is the first line of the proposal comment for launchID. Each
// proposal gets exactly one comment, never edited: it records what was
// proposed.
func TriageMarker(launchID string) string { return issuefinder.TriageMarker + launchID + " -->" }

// RenderTriage is the comment asking maintainers to review w's pending
// proposal (design 5.1: "decisions are commented on the view issue"). It uses
// only ledger fields the Dispatcher built and checked; the model's reason is
// shown only in a code span, so it renders no links, mentions or markup. The
// reply lines are in a fenced block, which the issue finder never reads, and
// the comment starts with the triage marker, which it skips.
func RenderTriage(w *ledger.WorkItem) string {
	p := w.Triage.Proposal
	var b strings.Builder
	b.WriteString(TriageMarker(p.LaunchID) + "\n")
	b.WriteString("**ACK Autopilot triage proposal.** Draft only: nothing changes until a team member replies.\n\n")
	fmt.Fprintf(&b, "- Item: `%s` `%s`\n", code(w.KeyHash), code(w.Key))
	fmt.Fprintf(&b, "- Detector: class `%s`, work `%s`\n", code(w.ChangeClass), code(w.DetectorWork))
	switch {
	case p.Class != "" && p.Clamped:
		fmt.Fprintf(&b, "- Proposal: **`%s`** → class `%s` (the detector's class is stricter and is kept)\n", code(p.Decision), code(p.Class))
	case p.Class != "":
		fmt.Fprintf(&b, "- Proposal: **`%s`** → class `%s`\n", code(p.Decision), code(p.Class))
	default:
		fmt.Fprintf(&b, "- Proposal: **`%s`**\n", code(p.Decision))
	}
	if len(p.Invalid) > 0 {
		fmt.Fprintf(&b, "- Why undecided: %s\n", spans(p.Invalid))
	}
	if p.Reason != "" {
		fmt.Fprintf(&b, "- Model's reason (unverified text): `%s`\n", code(p.Reason))
	}
	if len(p.EvidenceRefs) > 0 {
		fmt.Fprintf(&b, "- Evidence (checked against the SDK model and the CRD): %s\n", spans(p.EvidenceRefs))
	}
	switch p.Decision {
	case ledger.DecisionNotWorthModelling:
		b.WriteString("\nAccepting closes the item as `wont_fix:product_decision`. If the SDK bump generates this field, it stays in the CRD unless the bump branch also gets an `ignore.field_paths` entry.\n")
	case ledger.DecisionResource:
		b.WriteString("\nAccepting makes the item a `new-resource` draft; no workflow plans resources in Phase 1.\n")
	}
	b.WriteString("\nTo decide, a team member posts one of these lines as a new comment, outside a code block:\n\n```\n")
	if p.Decision != ledger.DecisionUndecided {
		fmt.Fprintf(&b, "/autopilot triage accept %s\n", p.LaunchID)
	}
	fmt.Fprintf(&b, "/autopilot triage reject %s\n", p.LaunchID)
	for _, d := range []string{ledger.DecisionFieldConfig, ledger.DecisionFieldHook, ledger.DecisionResource, ledger.DecisionNotWorthModelling} {
		serverDefaultOnly := w.DetectorWork == "server-default" && (d == ledger.DecisionResource || d == ledger.DecisionNotWorthModelling)
		if d == p.Decision || serverDefaultOnly {
			continue
		}
		fmt.Fprintf(&b, "/autopilot triage reject %s %s\n", p.LaunchID, d)
	}
	b.WriteString("```\n\nThe first reply that decides wins. `/autopilot wontfix` on this issue still closes the item.\n")
	return b.String()
}

// spans renders each value in its own code span.
func spans(values []string) string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = "`" + code(v) + "`"
	}
	return strings.Join(out, ", ")
}

// PlanTriage returns a create action for each pending proposal among items
// (those whose view issue is is) that has no comment by login yet, sorted by
// key. Anyone else's comment, even one that copies the marker, does not
// count. An empty login plans nothing.
func PlanTriage(is issuefinder.Issue, login string, items []*ledger.WorkItem) []Action {
	if login == "" {
		return nil
	}
	have := map[string]bool{}
	for _, c := range is.Comments {
		if c.Author != login || !strings.HasPrefix(c.Body, issuefinder.TriageMarker) {
			continue
		}
		first, _, _ := strings.Cut(c.Body, "\n")
		have[strings.TrimRight(first, "\r ")] = true
	}
	sorted := slices.Clone(items)
	slices.SortFunc(sorted, func(a, b *ledger.WorkItem) int { return strings.Compare(a.Key, b.Key) })
	var out []Action
	for _, w := range sorted {
		if w.State != ledger.Triaged || !w.Triage.Pending() || have[TriageMarker(w.Triage.Proposal.LaunchID)] {
			continue
		}
		out = append(out, Action{Issue: is.Number, Kind: Create, Body: RenderTriage(w), LaunchID: w.Triage.Proposal.LaunchID})
	}
	return out
}
