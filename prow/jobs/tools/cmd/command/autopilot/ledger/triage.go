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

package ledger

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// TriageModeAPI is the triage workflow's mode for api-change items (design
// 5.1). It is the only mode in Phase 1.
const TriageModeAPI = "api"

// The mode-api decisions (design 5.1), a closed enum.
const (
	DecisionResource          = "resource"
	DecisionFieldConfig       = "field-config"
	DecisionFieldHook         = "field-hook"
	DecisionNotWorthModelling = "not-worth-modelling"
	DecisionUndecided         = "undecided"
)

// APIDecisions lists the mode-api decisions.
var APIDecisions = []string{DecisionResource, DecisionFieldConfig, DecisionFieldHook, DecisionNotWorthModelling, DecisionUndecided}

// DecisionClass is the change class a decision asks for. not-worth-modelling
// and undecided ask for none.
var DecisionClass = map[string]string{
	DecisionResource:    "new-resource",
	DecisionFieldConfig: "config-only",
	DecisionFieldHook:   "hand-written-code",
}

// Review outcomes; they are also log events.
const (
	ReviewAccepted = "accepted"
	ReviewRejected = "rejected"
)

// Triage log events.
const (
	TriageStarted   = "started"
	TriageProposed  = "proposed"
	TriageFailed    = "failed"
	TriageDiscarded = "discarded"
	TriageStale     = "stale"
)

var triageEvents = map[string]bool{
	TriageStarted: true, TriageProposed: true, TriageFailed: true, TriageDiscarded: true, TriageStale: true,
	ReviewAccepted: true, ReviewRejected: true,
}

const (
	// MaxTriageLog bounds Triage.Log; the oldest events are dropped first.
	MaxTriageLog = 50
	// MaxTriageReason bounds a proposal's reason, in bytes (result.py
	// TRIAGE_MAX_REASON).
	MaxTriageReason = 600
	// MaxEvidenceRefs bounds a proposal's evidence references and invalid
	// codes (result.py TRIAGE_MAX_REFS).
	MaxEvidenceRefs = 16
	maxTriageRuns   = 1000
)

var (
	evidenceRefRE = regexp.MustCompile(`^(?:(?:op|shape|field):[A-Za-z0-9_][A-Za-z0-9_.]{0,199}|crd:(?:spec|status)\.[A-Za-z0-9_][A-Za-z0-9_.]{0,199})$`)
	invalidCodeRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}(?: [A-Za-z0-9_.:-]{1,210})?$`)
	triageIDRE    = regexp.MustCompile(`^autopilot-[0-9a-f]{16}-g[1-9][0-9]{0,5}-t[1-9][0-9]{0,5}$`)
	keyHashRE     = regexp.MustCompile(`^sha256:[0-9a-f]{16}$`)
	loginRE       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	reasonCodeRE  = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
)

// Triage is the triage workflow's record on a needs-config item (design 5.1;
// Plan 8). Only the Dispatcher writes it. The item stays DETECTED, owned by
// the issue finder, while a run is in flight; it moves to TRIAGED when the
// run's proposal is in, and a maintainer's reply decides what applies.
type Triage struct {
	Mode string `json:"mode"`
	// Input is TriageInput of the item when this record started. A
	// different value means the detector reclassified the item: the record
	// is stale and starts over.
	Input string `json:"input"`
	// Runs counts the runs started for Input. Launches counts every run in
	// this generation and numbers their launch ids, so ids never repeat.
	Runs     int `json:"runs"`
	Launches int `json:"launches"`
	// Claim is the triage run in flight.
	Claim *Claim `json:"claim,omitempty"`
	// Failed is set when no run is left for Input: the last failure's reason.
	Failed   string          `json:"failed,omitempty"`
	Proposal *TriageProposal `json:"proposal,omitempty"`
	Review   *TriageReview   `json:"review,omitempty"`
	// Log is every triage event, oldest first: the TriageAcceptRate record.
	Log []TriageEvent `json:"log,omitempty"`
}

// TriageProposal is one run's checked verdict.
type TriageProposal struct {
	LaunchID string `json:"launch_id"`
	Decision string `json:"decision"`
	// Class is the change class accepting the proposal gives: the
	// decision's class, or the detector's when that is stricter (Clamped).
	// Empty for not-worth-modelling and undecided.
	Class   string `json:"class,omitempty"`
	Clamped bool   `json:"clamped,omitempty"`
	// Invalid says why the run's answer became undecided.
	Invalid []string `json:"invalid,omitempty"`
	// Reason is the model's text, cleaned; shown, never parsed.
	Reason       string   `json:"reason,omitempty"`
	EvidenceRefs []string `json:"evidence_refs,omitempty"`
	TS           string   `json:"ts"`
}

// TriageReview is a maintainer's reply to a proposal.
type TriageReview struct {
	LaunchID string `json:"launch_id"`
	Outcome  string `json:"outcome"`
	// Decision is what applies: the proposal's on accept, the maintainer's
	// own on a reject that names one, "" on a bare reject.
	Decision  string `json:"decision,omitempty"`
	By        string `json:"by"`
	CommentID int64  `json:"comment_id"`
	TS        string `json:"ts"`
}

// TriageEvent is one entry of Triage.Log.
type TriageEvent struct {
	TS       string `json:"ts"`
	Event    string `json:"event"`
	LaunchID string `json:"launch_id,omitempty"`
	// Decision is the proposal's decision on proposed, accepted and
	// rejected events.
	Decision string `json:"decision,omitempty"`
	// Override is the maintainer's own decision on a rejected event.
	Override string `json:"override,omitempty"`
	// Invalid marks a proposed event whose answer failed validation.
	Invalid bool   `json:"invalid,omitempty"`
	By      string `json:"by,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// TriageInput is the digest of what a triage of w is made from. The SDK
// version is not in it: a newer SDK target alone does not re-triage.
func TriageInput(w *WorkItem) string {
	return KeyHash(strings.Join([]string{TriageModeAPI, w.Key, w.Resource, w.FieldPath, w.DetectorWork}, "\x1f"))
}

// TriageLaunchID names the n-th triage run of w's generation. Like launchID
// in the Dispatcher it is stable and fits a Kubernetes name.
func TriageLaunchID(w *WorkItem, n int) string {
	return fmt.Sprintf("autopilot-%s-g%d-t%d", strings.TrimPrefix(KeyHash(w.Key), "sha256:"), w.Generation, n)
}

// AppendLog adds e and keeps the last MaxTriageLog events.
func (t *Triage) AppendLog(e TriageEvent) {
	t.Log = append(t.Log, e)
	if n := len(t.Log); n > MaxTriageLog {
		t.Log = slices.Clone(t.Log[n-MaxTriageLog:])
	}
}

// Pending reports whether t holds a proposal no maintainer has replied to.
func (t *Triage) Pending() bool { return t != nil && t.Proposal != nil && t.Review == nil }

// Decided is the decision that applies to the item, or "" while there is
// none (no reply yet, or a bare reject).
func (t *Triage) Decided() string {
	if t == nil || t.Review == nil {
		return ""
	}
	return t.Review.Decision
}

// ValidEvidenceRef reports whether s has the triage/v1 reference form.
func ValidEvidenceRef(s string) bool { return evidenceRefRE.MatchString(s) }

// ValidInvalidCode reports whether s has the triage/v1 invalid-code form.
func ValidInvalidCode(s string) bool { return invalidCodeRE.MatchString(s) }

// CleanTriageText makes untrusted text safe to keep and show: valid UTF-8
// without U+FFFD, every control character a space, runs of white space one
// space, trimmed, and at most max bytes (cut at a rune boundary).
func CleanTriageText(s string, max int) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == utf8.RuneError:
			return -1
		case unicode.IsControl(r):
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	for len(s) > max {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return strings.TrimSpace(s)
}

// lintTriage returns one message per problem with w's triage record.
func lintTriage(w *WorkItem) []string {
	t := w.Triage
	if t == nil {
		return nil
	}
	var out []string
	bad := func(format string, args ...any) { out = append(out, "triage: "+fmt.Sprintf(format, args...)) }
	ours := func(id string) bool {
		prefix := fmt.Sprintf("autopilot-%s-g%d-t", strings.TrimPrefix(KeyHash(w.Key), "sha256:"), w.Generation)
		return triageIDRE.MatchString(id) && strings.HasPrefix(id, prefix)
	}
	if w.GapClass != "needs-config" {
		bad("only needs-config items are triaged, not gap_class %q", w.GapClass)
	}
	if t.Mode != TriageModeAPI {
		bad("mode %q, want %q", t.Mode, TriageModeAPI)
	}
	if !keyHashRE.MatchString(t.Input) {
		bad("input %q is not a key hash", t.Input)
	}
	if t.Runs < 0 || t.Launches < t.Runs || t.Launches > maxTriageRuns {
		bad("runs=%d launches=%d out of range", t.Runs, t.Launches)
	}
	if t.Failed != "" && !reasonCodeRE.MatchString(t.Failed) {
		bad("failed %q is not a SCREAMING_SNAKE reason", t.Failed)
	}
	if c := t.Claim; c != nil {
		if !ours(c.LaunchID) || c.Workflow != "triage" {
			bad("claim %q (workflow %q) is not a triage run of this item", c.LaunchID, c.Workflow)
		}
		if !utcRFC3339(c.TS) || !utcRFC3339(c.Deadline) || c.Deadline <= c.TS {
			bad("claim ts %q and deadline %q must be RFC 3339 UTC, the deadline after the ts", c.TS, c.Deadline)
		}
		if w.State != Detected && !w.State.Terminal() {
			bad("a triage run is in flight, so the item must be DETECTED or finished, not %s", w.State)
		}
		if t.Proposal != nil {
			bad("a triage run is in flight beside a proposal")
		}
	}
	if p := t.Proposal; p != nil {
		if !ours(p.LaunchID) {
			bad("proposal launch_id %q is not a triage run of this item", p.LaunchID)
		}
		want, hasClass := DecisionClass[p.Decision]
		switch {
		case !slices.Contains(APIDecisions, p.Decision):
			bad("proposal decision %q is not a mode-api decision", p.Decision)
		case !hasClass && (p.Class != "" || p.Clamped):
			bad("decision %s gives no class", p.Decision)
		case hasClass && p.Clamped == (p.Class == want):
			bad("class %q and clamped=%t do not fit decision %s", p.Class, p.Clamped, p.Decision)
		}
		if len(p.Invalid) > MaxEvidenceRefs {
			bad("%d invalid entries, at most %d", len(p.Invalid), MaxEvidenceRefs)
		}
		for _, c := range p.Invalid {
			if !ValidInvalidCode(c) {
				bad("invalid entry %q is malformed", c)
				break
			}
		}
		if len(p.Invalid) > 0 && p.Decision != DecisionUndecided {
			bad("a proposal with invalid entries must be undecided")
		}
		if CleanTriageText(p.Reason, MaxTriageReason) != p.Reason {
			bad("proposal reason is not clean one-line text of at most %d bytes", MaxTriageReason)
		}
		if len(p.EvidenceRefs) > MaxEvidenceRefs {
			bad("%d evidence refs, at most %d", len(p.EvidenceRefs), MaxEvidenceRefs)
		}
		for _, r := range p.EvidenceRefs {
			if !ValidEvidenceRef(r) {
				bad("evidence ref %q is malformed", r)
				break
			}
		}
		if !utcRFC3339(p.TS) {
			bad("proposal ts %q is not RFC 3339 UTC", p.TS)
		}
		if t.Review == nil && w.State != Triaged && w.State != Escalated && !w.State.Terminal() {
			bad("a pending proposal needs the item TRIAGED, not %s", w.State)
		}
	}
	if r := t.Review; r != nil {
		switch {
		case t.Proposal == nil:
			bad("a review needs a proposal")
		case r.LaunchID != t.Proposal.LaunchID:
			bad("review is for %q, the proposal is %q", r.LaunchID, t.Proposal.LaunchID)
		case r.Outcome == ReviewAccepted && r.Decision != t.Proposal.Decision:
			bad("an accepted review applies the proposal's decision %q, not %q", t.Proposal.Decision, r.Decision)
		}
		if r.Outcome != ReviewAccepted && r.Outcome != ReviewRejected {
			bad("review outcome %q is not accepted or rejected", r.Outcome)
		}
		if r.Decision == DecisionUndecided || (r.Decision != "" && !slices.Contains(APIDecisions, r.Decision)) {
			bad("review decision %q is not a decision a maintainer can apply", r.Decision)
		}
		if !loginRE.MatchString(r.By) || r.CommentID <= 0 || !utcRFC3339(r.TS) {
			bad("review by %q, comment %d, ts %q: need a login, a comment id and an RFC 3339 UTC ts", r.By, r.CommentID, r.TS)
		}
	}
	if len(t.Log) > MaxTriageLog {
		bad("%d log events, at most %d", len(t.Log), MaxTriageLog)
	}
	for _, e := range t.Log {
		if !triageEvents[e.Event] || !utcRFC3339(e.TS) {
			bad("log event %q at %q is unknown or not RFC 3339 UTC", e.Event, e.TS)
			break
		}
	}
	return out
}
