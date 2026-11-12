package issuefinder

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/apichanges/findingsblock"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/apichanges/issueregion"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

const (
	apiChangeLabel = "ack/api-change-detected"
	actor          = "issue-finder"
)

var wontfixRE = regexp.MustCompile(`(?m)^/autopilot wontfix (sha256:[0-9a-f]{16})[ \t\r]*$`)

type Comment struct {
	Author, Body string
}

// Issue is the part of a GitHub issue the finder reads. Comments are fetched
// for open issues only.
type Issue struct {
	Number   int
	URL      string
	Open     bool
	Author   string
	Labels   []string
	Body     string
	Comments []Comment
}

type Config struct {
	// BotLogin is the account detect-api-changes files issues as.
	BotLogin string
	// Team holds the logins whose /autopilot commands count.
	Team map[string]bool
	// Now stamps new history entries (RFC 3339, UTC).
	Now string
}

type Result struct {
	// Changed lists the keys of items created or modified, sorted.
	Changed []string
	// Skipped explains each issue that was not read, each finding Classify
	// could not turn into an item, each finding that names an item in a terminal
	// state linked to a different issue (once per key per run), and each team
	// /autopilot wontfix that matches neither a finder-owned item nor an
	// already WONT_FIX item on this issue, prefixed with "#<issue number>: ".
	// Ingest changes no state for any of them.
	Skipped []string
}

// Ingest reconciles items with issues in place and reports what changed.
//
// The finder only touches items it still owns (ledger.WorkItem.FinderOwned):
// pre-dispatch and never dispatched. An item a workflow has picked up, even one
// since returned to BLOCKED or QUEUED, is left exactly as it is.
//
// Only the bot's generated region of the body is parsed, so a maintainer's note
// quoting a findings block cannot be read as the bot's. A body without exactly
// one well-formed region is skipped.
//
// A block is incomplete when it omitted lines or Classify skipped some. For an
// incomplete block, items missing from it are not superseded, and an existing
// item's Evidence.Findings is the union of what it had and what the block
// lists, so truncation (which drops regen lines first) never shrinks the bump.
// A complete block replaces Evidence.Findings outright.
//
// Refreshing an existing item also rewrites its classification (ChangeClass,
// GapClass, AutonomyLevel, PriorityBand, Resource, FieldPath): a finder-owned
// item is untriaged, so the latest block is the only source for them.
//
// The result does not depend on the order of issues; see byPrecedence.
func Ingest(cfg Config, issues []Issue, items map[string]*ledger.WorkItem) Result {
	before := snapshot(items)
	var res Result
	// Keys already reported as terminal this run.
	reported := map[string]bool{}
	for _, is := range byPrecedence(issues) {
		skip := func(format string, args ...any) {
			res.Skipped = append(res.Skipped, fmt.Sprintf("#%d: ", is.Number)+fmt.Sprintf(format, args...))
		}
		svc, why := trusted(cfg, is)
		if why != "" {
			skip("%s", why)
			continue
		}
		if !is.Open {
			for _, w := range fromIssue(items, is.URL) {
				move(w, ledger.WontFix, cfg.Now, "wont_fix:product_decision", "ISSUE_CLOSED")
			}
			continue
		}
		region, ok := issueregion.Region(is.Body)
		if !ok {
			skip("no single well-formed generated region (%s ... %s)", issueregion.Begin, issueregion.End)
			continue
		}
		b, found, err := findingsblock.Parse(region)
		switch {
		case err != nil:
			skip("%v", err)
			continue
		case !found:
			skip("no findings block")
			continue
		case b.Service != svc:
			skip("block service %q != label service %q", b.Service, svc)
			continue
		}

		desired, skipped := Classify(b, is.URL)
		for _, s := range skipped {
			skip("%s", s)
		}
		complete := b.Omitted == 0 && len(skipped) == 0

		want := map[string]bool{}
		for _, w := range desired {
			want[w.Key] = true
			cur, ok := items[w.Key]
			if !ok {
				w.State = ledger.Detected
				w.History = []ledger.Transition{{TS: cfg.Now, To: ledger.Detected, By: actor, Reason: fmt.Sprintf("issue #%d", is.Number)}}
				items[w.Key] = w
				continue
			}
			if cur.State.Terminal() {
				// The same issue still listing a dismissed finding is the
				// steady state; only a different issue is a reappearance.
				if cur.Links.ViewIssue != is.URL && !reported[w.Key] {
					reported[w.Key] = true
					skip("%s: finding reappeared but item is %s; not reopened", w.Key, cur.State)
				}
				continue
			}
			if !cur.FinderOwned() {
				continue
			}
			ev := w.Evidence
			if !complete {
				ev.Findings = union(cur.Evidence.Findings, ev.Findings)
			}
			cur.Evidence, cur.Links.ViewIssue, cur.DependsOn, cur.DetectorWork = ev, w.Links.ViewIssue, w.DependsOn, w.DetectorWork
			cur.ChangeClass, cur.GapClass, cur.AutonomyLevel, cur.PriorityBand = w.ChangeClass, w.GapClass, w.AutonomyLevel, w.PriorityBand
			cur.Resource, cur.FieldPath = w.Resource, w.FieldPath
		}
		if complete {
			for _, w := range fromIssue(items, is.URL) {
				if !want[w.Key] {
					move(w, ledger.Superseded, cfg.Now, "superseded", "FINDING_GONE")
				}
			}
		}
		for _, c := range is.Comments {
			if !cfg.Team[c.Author] {
				continue
			}
			for _, m := range wontfixRE.FindAllStringSubmatch(outsideFences(c.Body), -1) {
				handled := false
				for _, w := range items {
					if w.Source != source || w.Links.ViewIssue != is.URL || w.KeyHash != m[1] {
						continue
					}
					switch {
					case w.FinderOwned():
						handled = true
						move(w, ledger.WontFix, cfg.Now, "wont_fix:product_decision", "MAINTAINER_WONTFIX by "+c.Author)
					case w.State == ledger.WontFix:
						// Already dismissed: the comment stays on the issue.
						handled = true
					}
				}
				if !handled {
					skip("/autopilot wontfix %s by %s matches no finder-owned item on this issue", m[1], c.Author)
				}
			}
		}
	}
	res.Changed = changed(before, items)
	return res
}

// byPrecedence returns issues in the order Ingest applies them: closed issues
// first, then open ones, each group by ascending Number.
//
// Closed first: a maintainer closing an issue dismisses its items (WONT_FIX is
// terminal) before a newer open issue re-listing the same findings can move
// them to itself by rewriting Links.ViewIssue, which the close path matches on.
// Ascending Number among open issues: when two issues for one service list the
// same key, the newest is applied last and ends up as the item's ViewIssue.
func byPrecedence(issues []Issue) []Issue {
	out := slices.Clone(issues)
	slices.SortStableFunc(out, func(a, b Issue) int {
		if a.Open != b.Open {
			if !a.Open {
				return -1
			}
			return 1
		}
		return a.Number - b.Number
	})
	return out
}

// outsideFences returns body without the lines of fenced code blocks (``` or
// ~~~, CommonMark rules: up to three spaces of indent, closed by a fence of
// the same character at least as long, unclosed runs to the end), so a
// command quoted as an example is not run.
func outsideFences(body string) string {
	var out []string
	var fence string // the open fence's run of ` or ~, or "" outside one
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		run := ""
		if len(line)-len(trimmed) <= 3 && len(trimmed) > 0 && (trimmed[0] == '`' || trimmed[0] == '~') {
			n := len(trimmed) - len(strings.TrimLeft(trimmed, trimmed[:1]))
			if n >= 3 {
				run = trimmed[:n]
			}
		}
		switch {
		case fence == "" && run != "":
			// An info string after a backtick fence may not hold a backtick.
			if run[0] == '~' || !strings.Contains(trimmed[len(run):], "`") {
				fence = run
				continue
			}
		case fence != "":
			if run != "" && run[0] == fence[0] && len(run) >= len(fence) && strings.TrimSpace(trimmed[len(run):]) == "" {
				fence = ""
			}
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// union returns the sorted, de-duplicated union of a and b, or nil when both
// are empty.
func union(a, b []string) []string {
	out := append(slices.Clone(a), b...)
	if len(out) == 0 {
		return nil
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// trusted returns the issue's service, or why the issue must not be read.
func trusted(cfg Config, is Issue) (string, string) {
	if is.Author != cfg.BotLogin {
		return "", fmt.Sprintf("author %q is not %q", is.Author, cfg.BotLogin)
	}
	labelled := false
	var services []string
	for _, l := range is.Labels {
		if l == apiChangeLabel {
			labelled = true
		}
		if s, ok := strings.CutPrefix(l, "service/"); ok {
			services = append(services, s)
		}
	}
	if !labelled {
		return "", "missing label " + apiChangeLabel
	}
	if len(services) != 1 {
		return "", fmt.Sprintf("want exactly one service/ label, got %d", len(services))
	}
	return services[0], ""
}

// fromIssue lists this finder's items for one issue that it still owns, sorted
// by key so changes apply in a fixed order.
func fromIssue(items map[string]*ledger.WorkItem, url string) []*ledger.WorkItem {
	var out []*ledger.WorkItem
	for _, w := range items {
		if w.Source == source && w.Links.ViewIssue == url && w.FinderOwned() {
			out = append(out, w)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func move(w *ledger.WorkItem, to ledger.State, now, outcome, reason string) {
	w.State = to
	w.Verdict = &ledger.Verdict{Outcome: outcome, Reason: reason}
	w.History = append(w.History, ledger.Transition{TS: now, To: to, By: actor, Reason: reason})
}

func snapshot(items map[string]*ledger.WorkItem) map[string]string {
	out := make(map[string]string, len(items))
	for k, w := range items {
		b, _ := ledger.Marshal(w)
		out[k] = string(b)
	}
	return out
}

func changed(before map[string]string, items map[string]*ledger.WorkItem) []string {
	var out []string
	for k, v := range snapshot(items) {
		if before[k] != v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
