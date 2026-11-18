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

package sdkbump

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/issuefinder"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

const maxStatusDescription = 140

var errMarkersMissing = errors.New("the PR body's autopilot section markers are missing or repeated")

// pickPR returns the open bump PR (nil when there is none), or the report the
// run must stop with.
func pickPR(prs []PR, o Options) (*PR, *Report) {
	var open []PR
	for _, p := range prs {
		if p.State == "open" {
			open = append(open, p)
		}
	}
	halt := func(outcome, reason, url string) (*PR, *Report) {
		r := stop(outcome, reason, url)
		return nil, &r
	}
	switch {
	case len(open) > 1:
		return halt(OutcomeNeedsHuman, "DUPLICATE_PRS", open[0].URL)
	case len(open) == 1 && !strings.EqualFold(open[0].Author, o.BotLogin):
		return halt(OutcomeNeedsHuman, "IDENTITY_MISMATCH", open[0].URL)
	case len(open) == 1 && open[0].BaseRef != "main":
		return halt(OutcomeNeedsHuman, "BRANCH_MISMATCH", open[0].URL)
	case len(open) == 1:
		return &open[0], nil
	}
	// No open PR. A closed one for this very item is a human's answer.
	for _, p := range prs {
		if v, ok := trailerValue(p.Body, "Work-Item"); ok && o.WorkItem != "" && v == o.WorkItem {
			if p.Merged {
				return halt(OutcomeNoChange, "ALREADY_MERGED", p.URL)
			}
			return halt(OutcomeNeedsHuman, "PR_CLOSED_BY_HUMAN", p.URL)
		}
	}
	return nil, nil
}

func normalizeBody(s string) string {
	return strings.TrimRight(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
}

// upsertPR creates the draft PR, or edits the open one when its title or body
// changed.
func upsertPR(ctx context.Context, o Options, open *PR, title, body string) (PR, error) {
	if open == nil {
		return o.Forge.CreatePR(ctx, o.repo(), NewPR{Title: title, Body: body, Head: Branch(o.Service), Base: "main", Draft: true})
	}
	if open.Title != title || normalizeBody(open.Body) != normalizeBody(body) {
		if err := o.Forge.EditPR(ctx, o.repo(), open.Number, title, body); err != nil {
			return *open, err
		}
		open.Title, open.Body = title, body
	}
	return *open, nil
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

// setGateStatus records dod's verdict on the head commit.
func setGateStatus(ctx context.Context, o Options, head string, v *dod.Verdict, digest string) error {
	s := CommitStatus{Context: ContextGate, State: "failure", TargetURL: o.statusURL()}
	if v.Verdict == dod.StatusPass {
		s.State, s.Description = "success", "ack-dod pass "+short(strings.TrimPrefix(digest, "sha256:"))
	} else {
		s.Description = "ack-dod fail: " + strings.Join(v.Reasons, " ")
	}
	s.Description = clip(s.Description, maxStatusDescription)
	return o.Forge.SetStatus(ctx, o.repo(), head, s)
}

// applyMergeBlock sets autopilot/server-default on head, the labels, and the
// draft state. It never marks the PR ready: since Plan 9 the Dispatcher does,
// after its own GATING pass checks this run's verdict against the PR head. It
// only turns a ready PR back into a draft when the gate failed or a
// server-default field blocks it. It adds only the labels it owns (plus
// autopilot/sensitive-path from the verdict) and removes only the block
// label.
func applyMergeBlock(ctx context.Context, o Options, pr PR, head string, gatePass bool, deps Dependents, verdictLabels []string) error {
	s := CommitStatus{Context: ContextServerDefault, TargetURL: o.statusURL()}
	n, u := len(deps.Items), deps.Unresolved()
	switch {
	case !deps.Known:
		s.State, s.Description = "failure", "server-default dependents unknown: the ledger was not read"
	case u > 0:
		s.State, s.Description = "failure", fmt.Sprintf("%d of %d server-default field(s) need a decision", u, n)
	case n > 0:
		s.State, s.Description = "success", fmt.Sprintf("%d server-default field(s), all decided", n)
	default:
		s.State, s.Description = "success", "no server-default fields"
	}
	if err := o.Forge.SetStatus(ctx, o.repo(), head, s); err != nil {
		return err
	}

	want := []string{LabelAutoGen, LabelClass, LabelLevel}
	if slices.Contains(verdictLabels, dod.LabelSensitivePath) {
		want = append(want, dod.LabelSensitivePath)
	}
	if deps.Blocked() {
		want = append(want, LabelBlockedServerDefault)
	}
	var missing []string
	for _, l := range want {
		if !slices.Contains(pr.Labels, l) {
			missing = append(missing, l)
		}
	}
	if len(missing) > 0 {
		if err := o.Forge.AddLabels(ctx, o.repo(), pr.Number, missing); err != nil {
			return err
		}
	}
	if !deps.Blocked() && slices.Contains(pr.Labels, LabelBlockedServerDefault) {
		if err := o.Forge.RemoveLabel(ctx, o.repo(), pr.Number, LabelBlockedServerDefault); err != nil {
			return err
		}
	}

	if ready := gatePass && !deps.Blocked(); !ready && !pr.Draft {
		return o.Forge.SetDraft(ctx, o.repo(), pr.Number, true)
	}
	return nil
}

// loadDependents reads the bump's dependents from the ledger; unknown (and so
// blocking) whenever the ledger or the bump item cannot be found.
func loadDependents(o Options, generatorYAML []byte) Dependents {
	if o.LedgerDir == "" {
		o.logf("no ledger directory: server-default dependents unknown")
		return Dependents{}
	}
	items, err := ledger.Load(o.LedgerDir)
	if err != nil {
		o.logf("ledger: %v", err)
		return Dependents{}
	}
	key := issuefinder.BumpKey(o.Service, o.SDKTo)
	b, ok := items[key]
	if !ok || (o.WorkItem != "" && b.KeyHash != o.WorkItem) {
		o.logf("the ledger has no item %s", ledger.KeyHash(key))
		return Dependents{}
	}
	return FindDependents(items, key, generatorYAML)
}

// refreshGate re-evaluates the merge block of pr at head: the checklist
// section of its body, autopilot/server-default, the block label and the
// draft state. It never touches autopilot/gate-verdict; it reads it, so a head
// dod never saw stays draft.
func refreshGate(ctx context.Context, o Options, g gitOps, pr PR, head string) (Dependents, error) {
	gen, err := g.showIfExists(ctx, head, "generator.yaml")
	if err != nil {
		return Dependents{}, err
	}
	deps := loadDependents(o, gen)
	state, err := o.Forge.GetStatus(ctx, o.repo(), head, ContextGate)
	if err != nil {
		return deps, err
	}
	body, ok := ReplaceSection(pr.Body, sectionServerDefault, RenderDependents(deps))
	if !ok {
		return deps, errMarkersMissing
	}
	updated, err := upsertPR(ctx, o, &pr, pr.Title, body)
	if err != nil {
		return deps, err
	}
	return deps, applyMergeBlock(ctx, o, updated, head, state == "success", deps, nil)
}
