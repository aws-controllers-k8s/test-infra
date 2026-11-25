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
	"strings"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/issuefinder"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

// applyTriageCommands applies maintainers' replies to pending proposals,
// oldest comment first (design 5.1: "a maintainer accepts or rejects them").
// A reply must name the launch id of the item's current proposal and be on
// the item's view issue. The first deciding reply (accept, or reject with a
// decision) wins; a bare reject is recorded and leaves the item for a later
// deciding reply. There is no other way for a proposal to apply: Phase 1 has
// no auto-accept. It returns one line per reply ignored for a reason a
// maintainer should see; replies already applied, replies to a past
// proposal and replies on finished items are ignored silently.
func applyTriageCommands(items map[string]*ledger.WorkItem, cmds []issuefinder.TriageCommand, ts string) []string {
	current := map[string]*ledger.WorkItem{}
	past := map[string]bool{}
	for _, k := range sortedKeys(items) {
		t := items[k].Triage
		if t == nil {
			continue
		}
		if t.Proposal != nil {
			current[t.Proposal.LaunchID] = items[k]
		}
		for _, e := range t.Log {
			if e.Event == ledger.TriageProposed {
				past[e.LaunchID] = true
			}
		}
	}
	var skipped []string
	for _, c := range cmds {
		skip := func(why string) {
			skipped = append(skipped, fmt.Sprintf("#%d: /autopilot triage %s %s by %s: %s", c.Issue, c.Verb, c.LaunchID, c.Author, why))
		}
		w, ok := current[c.LaunchID]
		if !ok {
			if !past[c.LaunchID] {
				skip("matches no triage proposal")
			}
			continue
		}
		if w.Links.ViewIssue != c.IssueURL {
			skip("the proposal belongs to another issue")
			continue
		}
		t := w.Triage
		deciding := c.Verb == "accept" || c.Decision != ""
		if r := t.Review; r != nil && (r.Decision != "" || !deciding || c.CommentID <= r.CommentID) {
			continue
		}
		if w.State.Terminal() {
			continue
		}
		if w.State != ledger.Triaged {
			skip("the item is " + string(w.State))
			continue
		}
		decision := c.Decision
		switch c.Verb {
		case "accept":
			if c.Decision != "" {
				skip("accept takes no decision; reply reject <launch id> <decision>")
				continue
			}
			if t.Proposal.Decision == ledger.DecisionUndecided {
				skip("an undecided proposal cannot be accepted; reply reject <launch id> <decision>")
				continue
			}
			decision = t.Proposal.Decision
		case "reject":
		default:
			skip("unknown verb")
			continue
		}
		if decision == ledger.DecisionNotWorthModelling && w.DetectorWork == serverDefaultWork {
			skip("a server-default item takes field-config, or /autopilot wontfix when there is no server default")
			continue
		}
		review(w, c, decision, ts)
	}
	return skipped
}

// review records a maintainer's reply and applies its decision: a class
// (accept: the proposal's, which already applies the stricter-only rule;
// reject: the maintainer's own), WONT_FIX for not-worth-modelling, or
// nothing for a bare reject.
func review(w *ledger.WorkItem, c issuefinder.TriageCommand, decision, ts string) {
	t, p := w.Triage, w.Triage.Proposal
	outcome := ledger.ReviewAccepted
	if c.Verb == "reject" {
		outcome = ledger.ReviewRejected
	}
	t.Review = &ledger.TriageReview{LaunchID: p.LaunchID, Outcome: outcome, Decision: decision, By: c.Author, CommentID: c.CommentID, TS: ts}
	e := ledger.TriageEvent{TS: ts, Event: outcome, LaunchID: p.LaunchID, Decision: p.Decision, By: c.Author}
	if outcome == ledger.ReviewRejected {
		e.Override = decision
	}
	t.AppendLog(e)
	by := " by " + c.Author
	switch decision {
	case "":
		transition(w, ledger.Triaged, ts, "TRIAGE_REJECTED"+by)
	case ledger.DecisionNotWorthModelling:
		w.Verdict = &ledger.Verdict{Outcome: "wont_fix:product_decision", Reason: "TRIAGE_NOT_WORTH_MODELLING"}
		transition(w, ledger.WontFix, ts, "TRIAGE_NOT_WORTH_MODELLING"+by)
	default:
		class := ledger.DecisionClass[decision]
		if outcome == ledger.ReviewAccepted {
			class = p.Class
		}
		setTriagedClass(w, class)
		transition(w, ledger.Triaged, ts, fmt.Sprintf("TRIAGE_%s %s%s", strings.ToUpper(outcome), decision, by))
	}
}

// setTriagedClass gives w a decided class, with the level and band
// issuefinder.Classify gives that class.
func setTriagedClass(w *ledger.WorkItem, class string) {
	w.ChangeClass = class
	if class == "new-resource" {
		w.AutonomyLevel, w.PriorityBand = "draft", 6
		return
	}
	w.AutonomyLevel, w.PriorityBand = "human-merge", 4
}
