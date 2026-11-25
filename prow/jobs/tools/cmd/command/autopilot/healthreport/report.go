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

// Package healthreport renders ACK Autopilot's daily health report (design
// 10, Plan 10): the deterministic part only, from the ledger and policy. It
// reads history entries, so the same ledger and date always give the same
// report, whenever it runs. The LLM root-cause step is not built.
package healthreport

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dispatcher"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/policy"
)

// DateLayout is the report's date: its file name is <date>.md.
const DateLayout = "2006-01-02"

// Limits on how many items a section cites.
const (
	maxEscalations    = 10
	maxFailureReasons = 3
	maxExamples       = 3
	maxListed         = 10
	maxQueueAge       = 5
)

// stateOrder is the order states are listed in: the lifecycle.
var stateOrder = []ledger.State{
	ledger.Detected, ledger.Triaged, ledger.PlanPending, ledger.Queued, ledger.Blocked,
	ledger.Running, ledger.Gating, ledger.RetryEnv, ledger.PRReady, ledger.HumanReview, ledger.Feedback,
	ledger.MergeSoak, ledger.Stale, ledger.Merged, ledger.Released, ledger.Verified,
	ledger.Done, ledger.Superseded, ledger.WontFix, ledger.Escalated,
}

// Summary is what the command prints in one line.
type Summary struct {
	Date                                    string
	Items, RunsCompleted, RunsFailed        int
	Escalated, GateFirstPass, GateFirstFail int
}

// window is [From, To).
type window struct{ From, To time.Time }

func (w window) has(t time.Time) bool { return !t.Before(w.From) && t.Before(w.To) }

// event is one history entry of one item.
type event struct {
	w *ledger.WorkItem
	i int
	t time.Time
}

func (e event) h() ledger.Transition { return e.w.History[e.i] }

// newestFirst orders events by time, newest first, then by item and entry.
func newestFirst(a, b event) int {
	return cmp.Or(b.t.Compare(a.t), cmp.Compare(a.w.KeyHash, b.w.KeyHash), cmp.Compare(a.i, b.i))
}

type report struct {
	items []*ledger.WorkItem // sorted by key hash, then key
	day   window
	week  window
}

func newReport(items map[string]*ledger.WorkItem, date time.Time) *report {
	r := &report{}
	for _, w := range items {
		r.items = append(r.items, w)
	}
	slices.SortFunc(r.items, func(a, b *ledger.WorkItem) int {
		return cmp.Or(cmp.Compare(a.KeyHash, b.KeyHash), cmp.Compare(a.Key, b.Key))
	})
	from := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	r.day = window{from, from.AddDate(0, 0, 1)}
	r.week = window{from.AddDate(0, 0, -6), r.day.To}
	return r
}

// events lists every history entry in w that keep accepts.
func (r *report) events(w window, keep func(h []ledger.Transition, i int) bool) []event {
	var out []event
	for _, it := range r.items {
		for i, h := range it.History {
			t, err := time.Parse(time.RFC3339, h.TS)
			if err != nil || !w.has(t) || !keep(it.History, i) {
				continue
			}
			out = append(out, event{it, i, t})
		}
	}
	slices.SortFunc(out, newestFirst)
	return out
}

type runCounts struct{ completed, failed, wontFix int }

func (r *report) runs(w window) (runCounts, []event) {
	var c runCounts
	var noChange []event
	for _, e := range r.events(w, func(h []ledger.Transition, i int) bool { return dispatcher.RunEnd(h, i) != dispatcher.RunNone }) {
		switch dispatcher.RunEnd(e.w.History, e.i) {
		case dispatcher.RunCompleted:
			c.completed++
			if e.h().To == ledger.Done {
				noChange = append(noChange, e)
			}
		case dispatcher.RunFailed:
			c.failed++
		case dispatcher.RunWontFix:
			c.wontFix++
		}
	}
	return c, noChange
}

// firstVerdicts counts items whose first valid gate verdict fell in w.
func (r *report) firstVerdicts(w window) (pass, fail int) {
	for _, it := range r.items {
		f := dispatcher.FirstGateVerdict(it.History)
		if f < 0 {
			continue
		}
		t, err := time.Parse(time.RFC3339, it.History[f].TS)
		if err != nil || !w.has(t) {
			continue
		}
		if p, _ := dispatcher.GateVerdictAt(it.History, f); p {
			pass++
		} else {
			fail++
		}
	}
	return pass, fail
}

// reaped lists the runs dispatcher.ReapedToday counts, in w. ReapedBetween's
// range is (from, to]; history times have whole seconds, so (From-1s,
// To-1s] is [From, To).
func (r *report) reaped(w window) []event {
	byKey := map[string]*ledger.WorkItem{}
	for _, it := range r.items {
		byKey[it.Key] = it
	}
	var out []event
	for _, rp := range dispatcher.ReapedBetween(byKey, w.From.Add(-time.Second), w.To.Add(-time.Second)) {
		it := byKey[rp.Key]
		t, _ := time.Parse(time.RFC3339, it.History[rp.Index].TS)
		out = append(out, event{it, rp.Index, t})
	}
	slices.SortFunc(out, newestFirst)
	return out
}

// stateAt is the item's state at t: what its last entry before t moved it
// to, and since when it has been there. ok is false before the item existed.
func stateAt(it *ledger.WorkItem, t time.Time) (s ledger.State, since time.Time, ok bool) {
	for _, h := range it.History {
		ht, err := time.Parse(time.RFC3339, h.TS)
		if err != nil || !ht.Before(t) {
			continue
		}
		if !ok || h.To != s {
			since = ht
		}
		s, ok = h.To, true
	}
	return s, since, ok
}

var (
	tokenRE = regexp.MustCompile(`^[A-Z][A-Z0-9_:]{0,63}$`)
	urlRE   = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/(issues|pull)/[0-9]{1,10}$`)
)

// signature is an entry's failure signature: the reason's first token,
// without RETRY. ATTEMPTS_EXHAUSTED names what failed last when it can.
func signature(it *ledger.WorkItem, i int) string {
	h := it.History
	reason := strings.TrimPrefix(h[i].Reason, "RETRY ")
	if reason == "ATTEMPTS_EXHAUSTED" {
		switch {
		case i == len(h)-1 && it.Verdict != nil && it.Verdict.Reason != "":
			reason = it.Verdict.Reason
		case i > 0 && (h[i-1].To == ledger.Gating || h[i-1].To == ledger.RetryEnv):
			reason = "GATE_FAILED"
		}
	}
	return token(reason)
}

func token(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), " ")
	switch {
	case s == "":
		return "NO_REASON"
	case !tokenRE.MatchString(s):
		return "INVALID_REASON"
	}
	return s
}

// code puts ledger text in a code span: no backticks, no line breaks, at most
// 160 characters. The ledger is written by jobs, but its text came from
// issues and runs.
func code(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '`' || r < 0x20 || r == 0x7f {
			return '\''
		}
		return r
	}, s)
	if len(s) > 160 {
		s = s[:157] + "..."
	}
	return "`" + s + "`"
}

func link(label, url string) string {
	if !urlRE.MatchString(url) {
		return ""
	}
	return fmt.Sprintf(" [%s](%s)", label, url)
}

// cite names an item so a reader can find it: key hash, service, key,
// issue and PR.
func cite(it *ledger.WorkItem) string {
	return code(it.KeyHash) + " " + code(it.Service) + " " + code(it.Key) +
		link("issue", it.Links.ViewIssue) + link("PR", it.Links.PR)
}

func rate(num, den int) string {
	if den == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%% (%d/%d)", 100*float64(num)/float64(den), num, den)
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// age is a duration in whole hours, or days and hours.
func age(d time.Duration) string {
	h := int(d / time.Hour)
	if h < 24 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dd %dh", h/24, h%24)
}

// Render is the report for the UTC day date over items, with the policy (or
// why it could not be read).
func Render(items map[string]*ledger.WorkItem, pol *policy.Policy, polErr error, date time.Time) (string, Summary) {
	r := newReport(items, date)
	var b strings.Builder
	p := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	day := r.day.From.Format(DateLayout)
	sum := Summary{Date: day, Items: len(r.items)}

	p("# ACK Autopilot health report %s\n\n", day)
	p("Window: %s to %s (UTC); 7-day figures from %s.\n", ts(r.day.From), ts(r.day.To), ts(r.week.From))
	p("Generated from the ledger's history alone: the same ledger and date give the same report.\n")
	p("Oncall reads this daily (design 12).\n\n")
	switch {
	case pol != nil:
		sw := pol.Switches
		p("Policy now: launcher `%s`, paused `%t`, abort_inflight `%t`, services `%s`.\n\n",
			sw.Launcher, sw.Paused, sw.AbortInflight, strings.Join(sw.Services, ", "))
	case polErr != nil:
		p("Policy: unreadable (%s).\n\n", code(polErr.Error()))
	}

	dayRuns, noChange := r.runs(r.day)
	weekRuns, _ := r.runs(r.week)
	dayPass, dayFail := r.firstVerdicts(r.day)
	weekPass, weekFail := r.firstVerdicts(r.week)
	isEscalation := func(h []ledger.Transition, i int) bool { return h[i].To == ledger.Escalated }
	dayEsc, weekEsc := r.events(r.day, isEscalation), r.events(r.week, isEscalation)
	dayReaped, weekReaped := r.reaped(r.day), r.reaped(r.week)
	sum.RunsCompleted, sum.RunsFailed, sum.Escalated = dayRuns.completed, dayRuns.failed, len(dayEsc)
	sum.GateFirstPass, sum.GateFirstFail = dayPass, dayFail

	p("## Summary\n\n")
	p("| | 24 h | 7 d |\n|---|---|---|\n")
	p("| Runs completed (pr_opened, pr_updated, completed_no_change) | %d | %d |\n", dayRuns.completed, weekRuns.completed)
	p("| Runs failed (failed, needs_human, reaped) | %d | %d |\n", dayRuns.failed, weekRuns.failed)
	p("| Runs WONT_FIX (excluded below) | %d | %d |\n", dayRuns.wontFix, weekRuns.wontFix)
	p("| Failure rate: failed / (completed + failed) | %s | %s |\n",
		rate(dayRuns.failed, dayRuns.completed+dayRuns.failed), rate(weekRuns.failed, weekRuns.completed+weekRuns.failed))
	p("| Run success: completed / (completed + failed) | %s | %s |\n",
		rate(dayRuns.completed, dayRuns.completed+dayRuns.failed), rate(weekRuns.completed, weekRuns.completed+weekRuns.failed))
	p("| Gate first pass / first fail | %d / %d | %d / %d |\n", dayPass, dayFail, weekPass, weekFail)
	p("| GateFirstPassRate | %s | %s |\n", rate(dayPass, dayPass+dayFail), rate(weekPass, weekPass+weekFail))
	p("| Reaped runs (INFRA_NO_VERDICT) | %d | %d |\n", len(dayReaped), len(weekReaped))
	p("| Moves into ESCALATED | %d | %d |\n\n", len(dayEsc), len(weekEsc))

	p("## Items by state at %s\n\n", ts(r.day.To))
	byState := map[ledger.State]int{}
	total := 0
	for _, it := range r.items {
		if s, _, ok := stateAt(it, r.day.To); ok {
			byState[s]++
			total++
		}
	}
	p("| State | Items |\n|---|---|\n")
	for _, s := range stateOrder {
		if byState[s] > 0 {
			p("| %s | %d |\n", s, byState[s])
		}
	}
	var unknown []ledger.State
	for s := range byState {
		if !slices.Contains(stateOrder, s) {
			unknown = append(unknown, s)
		}
	}
	slices.Sort(unknown)
	for _, s := range unknown {
		// A state this report does not know (hand edit): still counted.
		p("| %s | %d |\n", code(string(s)), byState[s])
	}
	p("| total | %d |\n\n", total)

	p("## Activity in the window\n\n")
	activity := []struct {
		name string
		keep func(h []ledger.Transition, i int) bool
	}{
		{"Detected", func(_ []ledger.Transition, i int) bool { return i == 0 }},
		{"Started (moved to RUNNING)", func(h []ledger.Transition, i int) bool { return h[i].To == ledger.Running }},
		{"Done", func(h []ledger.Transition, i int) bool { return h[i].To == ledger.Done }},
		{"Escalated", isEscalation},
		{"Won't fix", func(h []ledger.Transition, i int) bool { return h[i].To == ledger.WontFix }},
	}
	p("| Event | Moves | Items |\n|---|---|---|\n")
	lists := make([][]event, len(activity))
	for n, a := range activity {
		lists[n] = r.events(r.day, a.keep)
		p("| %s | %d | %d |\n", a.name, len(lists[n]), distinct(lists[n]))
	}
	p("\n")
	for n, a := range activity {
		if len(lists[n]) == 0 || a.name == "Escalated" {
			continue
		}
		p("%s, newest first:\n\n", a.name)
		listEvents(p, lists[n], reasonOf)
	}

	p("## Escalations (24 h, newest first, top %d)\n\n", maxEscalations)
	if len(dayEsc) == 0 {
		p("None.\n\n")
	} else {
		listEvents(p, dayEsc, func(e event) string {
			s := code(token(e.h().Reason))
			if sig := signature(e.w, e.i); sig != token(e.h().Reason) {
				s += " (" + code(sig) + ")"
			}
			return s
		})
	}

	p("## Top %d failure reasons (24 h)\n\n", maxFailureReasons)
	p("Every move into ESCALATED and every RETRY requeue, by signature.\n\n")
	failures := r.events(r.day, func(h []ledger.Transition, i int) bool {
		return h[i].To == ledger.Escalated || (h[i].To == ledger.Queued && strings.HasPrefix(h[i].Reason, "RETRY "))
	})
	bySig := map[string][]event{}
	for _, e := range failures {
		s := signature(e.w, e.i)
		bySig[s] = append(bySig[s], e)
	}
	sigs := make([]string, 0, len(bySig))
	for s := range bySig {
		sigs = append(sigs, s)
	}
	slices.SortFunc(sigs, func(a, b string) int { return cmp.Or(cmp.Compare(len(bySig[b]), len(bySig[a])), cmp.Compare(a, b)) })
	if len(sigs) == 0 {
		p("None.\n\n")
	}
	for n, s := range sigs {
		if n == maxFailureReasons {
			break
		}
		p("%d. %s: %d\n", n+1, code(s), len(bySig[s]))
		for k, e := range bySig[s] {
			if k == maxExamples {
				break
			}
			p("   - %s %s\n", ts(e.t), cite(e.w))
		}
	}
	if len(sigs) > 0 {
		p("\n")
	}

	p("## Trivial successes (24 h)\n\n")
	p("Runs that ended completed_no_change (RUNNING -> DONE).\n\n")
	if len(noChange) == 0 {
		p("None.\n\n")
	} else {
		listEvents(p, noChange, reasonOf)
	}

	p("## Reaped runs (24 h)\n\n")
	if len(dayReaped) == 0 {
		p("None.\n\n")
	} else {
		listEvents(p, dayReaped, reasonOf)
	}

	p("## Queue age at %s\n\n", ts(r.day.To))
	for _, s := range []ledger.State{ledger.Queued, ledger.Blocked} {
		type waiting struct {
			it    *ledger.WorkItem
			since time.Time
		}
		var ws []waiting
		for _, it := range r.items {
			if st, since, ok := stateAt(it, r.day.To); ok && st == s {
				ws = append(ws, waiting{it, since})
			}
		}
		slices.SortStableFunc(ws, func(a, b waiting) int { return a.since.Compare(b.since) })
		p("Oldest %s (%d in total):\n\n", s, len(ws))
		if len(ws) == 0 {
			p("None.\n\n")
			continue
		}
		for n, x := range ws {
			if n == maxQueueAge {
				break
			}
			p("- %s since %s %s\n", age(r.day.To.Sub(x.since)), ts(x.since), cite(x.it))
		}
		p("\n")
	}

	p("## Tokens and AWS e2e runs\n\n")
	var tokDay, e2eDay, tokAll, e2eAll int64
	ended := map[*ledger.WorkItem]bool{}
	for _, e := range r.events(r.day, func(h []ledger.Transition, i int) bool { return dispatcher.RunEnd(h, i) != dispatcher.RunNone }) {
		ended[e.w] = true
	}
	for _, it := range r.items {
		if it.Usage == nil {
			continue
		}
		tokAll, e2eAll = tokAll+it.Usage.Tokens, e2eAll+int64(it.Usage.E2ERuns)
		if ended[it] {
			tokDay, e2eDay = tokDay+it.Usage.Tokens, e2eDay+int64(it.Usage.E2ERuns)
		}
	}
	p("The ledger keeps each item's running totals (usage.tokens, usage.e2e_runs), not per-run times, so usage in the window is not derivable.\n\n")
	p("| | Tokens | AWS e2e runs |\n|---|---|---|\n")
	p("| Totals of the %d item(s) with a run that ended in the window | %d | %d |\n", len(ended), tokDay, e2eDay)
	p("| Totals of all items | %d | %d |\n\n", tokAll, e2eAll)

	p("## Root causes\n\n")
	p("Root causes: not generated (deterministic report only). Design 10's read-only subagents that root-cause failed and escalated runs into signatures are not built yet.\n")
	return b.String(), sum
}

// reasonOf is the entry's reason as written, or nothing.
func reasonOf(e event) string {
	if e.h().Reason == "" {
		return ""
	}
	return code(e.h().Reason)
}

func distinct(es []event) int {
	seen := map[*ledger.WorkItem]bool{}
	for _, e := range es {
		seen[e.w] = true
	}
	return len(seen)
}

// listEvents prints up to maxListed events and how many it left out.
func listEvents(p func(string, ...any), es []event, what func(event) string) {
	for n, e := range es {
		if n == maxListed {
			p("- and %d more\n", len(es)-maxListed)
			break
		}
		if w := what(e); w != "" {
			p("- %s %s %s\n", ts(e.t), w, cite(e.w))
		} else {
			p("- %s %s\n", ts(e.t), cite(e.w))
		}
	}
	p("\n")
}
