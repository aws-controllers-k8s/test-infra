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
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/policy"
)

// triageWorkflow is the agent-workflows.yaml name of the triage workflow
// (prow/agent-workflows/templates/triage.tpl).
const triageWorkflow = "triage"

// serverDefaultWork is Plan 4's detector work for a read-back Spec field.
const serverDefaultWork = "server-default"

// Hold reasons for needs-config items (design 5.1).
const (
	// HoldTriageRunning: a triage run is in flight.
	HoldTriageRunning = "triage-running"
	// HoldTriageFailed: no triage run is left for the item's current
	// classification; a person decides (/autopilot wontfix, or a fix).
	HoldTriageFailed = "triage-failed"
	// HoldTriageIneligible: a field the triage workflow cannot take (an
	// unknown finding class, a malformed field path, a core SDK target).
	HoldTriageIneligible = "triage-ineligible"
	// HoldAwaitingTriageReview: a proposal waits for a maintainer's reply.
	HoldAwaitingTriageReview = "awaiting-triage-review"
	// HoldTriageRejected: a maintainer rejected the proposal without a
	// decision of their own.
	HoldTriageRejected = "triage-rejected"
)

// Caps on triage launches (policy/triage.yaml).
const (
	CapTriageConcurrent = "max_concurrent_triage_runs"
	CapTriageDaily      = "max_triage_launches_per_day"
)

// findingClassOf maps a key's class token (issuefinder classToken) to the
// detector FindingClass the workflow takes.
var findingClassOf = map[string]string{"spec": "spec-field", "status": "status-field", "lifecycle": "lifecycle-field"}

// The checks of workflows/validation.py validate_triage_inputs and of Plan
// 7's prowlaunch validators (service, resource, field, sdk-to; triage's
// detector-work), whichever is stricter, so an item the job or the launcher
// would refuse is held triage-ineligible instead of failing its launch every
// tick. Keep them in step with prowlaunch/args.go.
var (
	triageResourceRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,127}$`)
	triageFieldRE    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*(?:\.[A-Za-z][A-Za-z0-9]*)*$`)
	triageWorkRE     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	triageSDKToRE    = regexp.MustCompile(`^service/[a-z0-9]{1,63}/v[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}$`)
	triageServiceRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
)

// maxTriageFieldLen is prowlaunch's maxArgLen: the longest arg value the
// launcher passes.
const maxTriageFieldLen = 256

// isTriageItem reports whether w is the issue finder's needs-config item,
// which mode api triages. Its class may already be a triage decision's.
func isTriageItem(w *ledger.WorkItem) bool {
	return w.Source == "api-change" && w.GapClass == "needs-config"
}

// triageArgs is the triage workflow's arguments for w (triage.tpl
// required_args), or the name of the first value that rules w out.
func triageArgs(w *ledger.WorkItem) (map[string]string, string) {
	seg := strings.Split(w.Key, "|")
	if len(seg) != 5 {
		return nil, "key"
	}
	token, _, _ := strings.Cut(seg[3], ":")
	fc, ok := findingClassOf[token]
	switch {
	case !ok:
		return nil, "finding-class"
	case w.ChangeClass != "config-only" && w.ChangeClass != "hand-written-code":
		return nil, "change-class"
	case w.AutonomyLevel != "human-merge":
		return nil, "autonomy-level"
	case !triageServiceRE.MatchString(w.Service):
		return nil, "service"
	case !triageResourceRE.MatchString(w.Resource):
		return nil, "resource"
	case len(w.FieldPath) > maxTriageFieldLen || !triageFieldRE.MatchString(w.FieldPath):
		return nil, "field"
	case !triageWorkRE.MatchString(w.DetectorWork):
		return nil, "detector-work"
	case !triageSDKToRE.MatchString(w.Evidence.SDKTo):
		return nil, "sdk-to"
	}
	return map[string]string{
		"service": w.Service, "resource": w.Resource, "field": w.FieldPath, "finding-class": fc,
		"detector-work": w.DetectorWork, "change-class": w.ChangeClass, "sdk-to": w.Evidence.SDKTo,
		"work-item": w.KeyHash,
	}, ""
}

// triageEnabled reports whether the policy has a triage lane for mode api.
func triageEnabled(p policy.Policy) bool {
	return p.Triage != nil && p.Triage.ModeEnabled(ledger.TriageModeAPI)
}

// triageCandidate reports whether w waits for a triage run: a finder-owned
// DETECTED triage item with no run in flight, no proposal and runs left.
func triageCandidate(w *ledger.WorkItem) bool {
	if w.State != ledger.Detected || !w.FinderOwned() || !isTriageItem(w) {
		return false
	}
	t := w.Triage
	return t == nil || (t.Claim == nil && t.Proposal == nil && t.Failed == "")
}

// triagePick is an item scheduleTriage picked, with its arguments.
type triagePick struct {
	item *ledger.WorkItem
	args map[string]string
}

// scheduleTriage picks the items to triage this tick, by band, then oldest
// first, and says why each other candidate waits. Triage runs open no PR and
// change no repo, so they skip the per-repo, LLM and review caps; they honour
// the kill switches (paused, abort_inflight), paused repos, the service
// allowlist and their own caps. It changes no item.
func scheduleTriage(items map[string]*ledger.WorkItem, p policy.Policy, now time.Time) ([]triagePick, []capped) {
	return scheduleTriageWith(items, p, now, 0, nil)
}

// scheduleTriageWith is scheduleTriage with the launcher's view of what is
// in flight (Plan 7's inventory): orphans is the number of triage runs no
// item claims, and they fill triage slots; adopt holds launch ids already in
// flight that are a candidate's next run (a launch whose ledger push was
// lost). Such a candidate is weighed first and skips the triage caps: Launch
// adopts the running job and starts nothing.
func scheduleTriageWith(items map[string]*ledger.WorkItem, p policy.Policy, now time.Time, orphans int, adopt map[string]bool) ([]triagePick, []capped) {
	if !triageEnabled(p) {
		return nil, nil
	}
	sw, tp := p.Switches, *p.Triage
	dayAgo := now.Add(-24 * time.Hour)
	running, today := orphans, 0
	var queue []*ledger.WorkItem
	for _, w := range items {
		if t := w.Triage; t != nil {
			if t.Claim != nil {
				running++
			}
			for _, e := range t.Log {
				ts, err := time.Parse(time.RFC3339, e.TS)
				if e.Event == ledger.TriageStarted && err == nil && ts.After(dayAgo) {
					today++
				}
			}
		}
		if !triageCandidate(w) {
			continue
		}
		if _, why := triageArgs(w); why == "" {
			queue = append(queue, w)
		}
	}
	sort.Slice(queue, func(i, j int) bool {
		a, c := queue[i], queue[j]
		if a.PriorityBand != c.PriorityBand {
			return a.PriorityBand < c.PriorityBand
		}
		if fa, fc := firstSeen(a), firstSeen(c); fa != fc {
			return fa < fc
		}
		return a.Key < c.Key
	})
	sort.SliceStable(queue, func(i, j int) bool {
		return adopt[nextTriageLaunchID(queue[i])] && !adopt[nextTriageLaunchID(queue[j])]
	})
	var picks []triagePick
	var waits []capped
	for _, w := range queue {
		cap := ""
		switch {
		case sw.Paused:
			cap = CapPaused
		case sw.AbortInflight:
			// Plan 7's kill switch: nothing launches, triage included.
			cap = CapAborting
		case sw.RepoPaused(w.Repo):
			cap = CapRepoPaused
		case !sw.ServiceEnabled(w.Service):
			cap = CapServiceNotEnabled
		case adopt[nextTriageLaunchID(w)]:
			// Already running: no triage cap applies.
		case running >= tp.MaxConcurrentTriageRuns:
			cap = CapTriageConcurrent
		case today >= tp.MaxTriageLaunchesPerDay:
			cap = CapTriageDaily
		}
		if cap != "" {
			waits = append(waits, capped{w, cap})
			continue
		}
		args, _ := triageArgs(w)
		picks = append(picks, triagePick{w, args})
		running++
		today++
	}
	return picks, waits
}

// firstSeen is when the item was first recorded.
func firstSeen(w *ledger.WorkItem) string {
	if len(w.History) == 0 {
		return ""
	}
	return w.History[0].TS
}

// nextTriageLaunchID names w's next triage run.
func nextTriageLaunchID(w *ledger.WorkItem) string {
	n := 1
	if w.Triage != nil {
		n = w.Triage.Launches + 1
	}
	return ledger.TriageLaunchID(w, n)
}

// startTriage records a started triage run on w. The item stays DETECTED and
// finder-owned: triage is not a dispatch (the item's Claim and Attempts are
// for workflow runs).
func startTriage(w *ledger.WorkItem, req LaunchRequest, now time.Time) {
	ts := now.UTC().Format(time.RFC3339)
	if w.Triage == nil {
		w.Triage = &ledger.Triage{Mode: ledger.TriageModeAPI, Input: ledger.TriageInput(w)}
	}
	t := w.Triage
	t.Runs++
	t.Launches++
	t.Claim = &ledger.Claim{LaunchID: req.LaunchID, Workflow: req.Workflow, TS: ts, Deadline: now.Add(req.Timeout).UTC().Format(time.RFC3339)}
	t.AppendLog(ledger.TriageEvent{TS: ts, Event: ledger.TriageStarted, LaunchID: req.LaunchID})
	w.Links.ProwJobs = append(w.Links.ProwJobs, req.LaunchID)
	transition(w, ledger.Detected, ts, "TRIAGE_STARTED "+req.LaunchID)
}

// triageHold says why route keeps a triage item where it is, or "" when its
// decision lets it move on. A triage item never runs on the detector's guess
// alone (design 5.1): a needs-config item past TRIAGED without a decision
// (only a hand edit can do that) goes back to TRIAGED when it has a proposal
// a reply can still decide, and to DETECTED, to be triaged again, when it has
// none. So does an item in TRIAGED without a proposal: no reply can decide
// it, and DETECTED only ever launches a triage run.
func triageHold(w *ledger.WorkItem, ts string) string {
	t := w.Triage
	switch w.State {
	case ledger.Detected:
		switch {
		case t != nil && t.Claim != nil:
			return HoldTriageRunning
		case t != nil && t.Failed != "":
			return HoldTriageFailed
		}
		if _, why := triageArgs(w); why != "" {
			return HoldTriageIneligible
		}
		return HoldAwaitingTriage
	case ledger.Triaged:
		switch {
		case t == nil || t.Proposal == nil:
			transition(w, ledger.Detected, ts, "TRIAGE_REQUIRED")
			return triageHold(w, ts)
		case t.Review == nil:
			return HoldAwaitingTriageReview
		case t.Decided() == "":
			return HoldTriageRejected
		}
		return ""
	}
	if t.Decided() != "" {
		return ""
	}
	if t == nil || t.Proposal == nil {
		transition(w, ledger.Detected, ts, "TRIAGE_REQUIRED")
	} else {
		transition(w, ledger.Triaged, ts, "TRIAGE_REQUIRED")
	}
	return triageHold(w, ts)
}
