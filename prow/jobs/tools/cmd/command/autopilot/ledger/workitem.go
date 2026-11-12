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

// Package ledger is the ACK Autopilot work-item ledger: one JSON file per work
// item under <root>/workitems/<dir>/<hash>.json, in the ack-private repo. Only
// autopilot jobs write it; people change policy files by pull request.
package ledger

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

const Schema = "ack.autopilot.workitem/v1"

type State string

const (
	Detected    State = "DETECTED"
	Triaged     State = "TRIAGED"
	Queued      State = "QUEUED"
	Blocked     State = "BLOCKED"
	PlanPending State = "PLAN_PENDING"
	Running     State = "RUNNING"
	Gating      State = "GATING"
	RetryEnv    State = "RETRY_ENV"
	PRReady     State = "PR_READY"
	HumanReview State = "HUMAN_REVIEW"
	Feedback    State = "FEEDBACK"
	MergeSoak   State = "MERGE_SOAK"
	Stale       State = "STALE"
	Merged      State = "MERGED"
	Released    State = "RELEASED"
	Verified    State = "VERIFIED"
	Done        State = "DONE"
	Superseded  State = "SUPERSEDED"
	WontFix     State = "WONT_FIX"
	Escalated   State = "ESCALATED"
)

var stateDir = map[State]string{
	Detected: "open", Triaged: "open", Queued: "open", Blocked: "open", PlanPending: "open",
	Running: "inflight", Gating: "inflight", RetryEnv: "inflight", PRReady: "inflight",
	HumanReview: "inflight", Feedback: "inflight", MergeSoak: "inflight", Stale: "inflight",
	Merged: "inflight", Released: "inflight", Verified: "inflight",
	Done: "done", Superseded: "done", WontFix: "wontfix", Escalated: "escalated",
}

// Dir is the workitems/ subdirectory an item in this state lives in.
func (s State) Dir() (string, bool) {
	d, ok := stateDir[s]
	return d, ok
}

// PreDispatch reports whether no workflow has picked the item up yet, so a
// finder may still change or close it.
func (s State) PreDispatch() bool {
	switch s {
	case Detected, Triaged, Queued, Blocked, PlanPending:
		return true
	}
	return false
}

// Terminal reports whether the item is finished; finders never reopen it.
func (s State) Terminal() bool {
	return s == Done || s == Superseded || s == WontFix
}

// EverDispatched reports whether any history entry moved the item into an
// in-flight state (one whose Dir is "inflight"). Under design 5.4 the only edge
// into in-flight is QUEUED->RUNNING, so this means a workflow picked the item
// up. An item can later return to BLOCKED or QUEUED (env retry, requeue), so
// the current state alone cannot tell whether a workflow has ever touched it.
// Going by directory rather than by RUNNING alone is robust to edited history
// and to future in-flight states. ESCALATED, DONE, SUPERSEDED and WONT_FIX do
// not count: they can be reached directly from pre-dispatch.
func (w WorkItem) EverDispatched() bool {
	for _, h := range w.History {
		if dir, _ := h.To.Dir(); dir == "inflight" {
			return true
		}
	}
	return false
}

// FinderOwned reports whether the issue finder may still change or close the
// item: it is in a pre-dispatch state and was never dispatched.
func (w WorkItem) FinderOwned() bool { return w.State.PreDispatch() && !w.EverDispatched() }

type Evidence struct {
	SDKFrom string `json:"sdk_from,omitempty"`
	SDKTo   string `json:"sdk_to,omitempty"`
	// Findings lists Kind.Subject for a batched SDK-bump item.
	Findings []string `json:"findings,omitempty"`
}

type Links struct {
	ViewIssue string `json:"view_issue,omitempty"`
}

type Verdict struct {
	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`
}

type Transition struct {
	TS     string `json:"ts"`
	To     State  `json:"to"`
	By     string `json:"by"`
	Reason string `json:"reason,omitempty"`
}

type WorkItem struct {
	Schema        string `json:"schema"`
	Key           string `json:"key"`
	KeyHash       string `json:"key_hash"`
	Generation    int    `json:"generation"`
	Source        string `json:"source"`
	Service       string `json:"service"`
	Repo          string `json:"repo"`
	Resource      string `json:"resource,omitempty"`
	FieldPath     string `json:"field_path,omitempty"`
	GapClass      string `json:"gap_class"`
	ChangeClass   string `json:"change_class"`
	AutonomyLevel string `json:"autonomy_level"`
	PriorityBand  int    `json:"priority_band"`
	// DetectorWork is the detector's Work name for a needs-config item. For a
	// manual-review item, which has no Work, it holds the detector Class.
	DetectorWork string       `json:"detector_work,omitempty"`
	Evidence     Evidence     `json:"evidence"`
	DependsOn    []string     `json:"depends_on,omitempty"`
	State        State        `json:"state"`
	Links        Links        `json:"links"`
	Verdict      *Verdict     `json:"verdict"`
	History      []Transition `json:"history"`
}

// KeyHash is the short, stable id of a key: "sha256:" and 16 hex digits.
func KeyHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "sha256:" + hex.EncodeToString(sum[:])[:16]
}

// FileName is the item's file name inside its state directory.
func (w *WorkItem) FileName() string {
	// Derived from Key, never the stored KeyHash, so a hand-edited hash can
	// not become a path.
	return strings.TrimPrefix(KeyHash(w.Key), "sha256:") + ".json"
}

// Marshal is the one encoding the ledger uses: two-space indent, no HTML
// escaping (keys contain "->"), trailing newline. It is deterministic, so a
// file changes only when the item does.
func Marshal(w *WorkItem) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(w); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
