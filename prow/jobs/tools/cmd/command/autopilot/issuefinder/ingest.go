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
	// Skipped explains each issue that was not read, and each finding Classify
	// could not turn into an item, prefixed with "#<issue number>: ".
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
func Ingest(cfg Config, issues []Issue, items map[string]*ledger.WorkItem) Result {
	before := snapshot(items)
	var res Result
	for _, is := range issues {
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
			if !cur.FinderOwned() {
				continue
			}
			ev := w.Evidence
			if !complete {
				ev.Findings = union(cur.Evidence.Findings, ev.Findings)
			}
			cur.Evidence, cur.Links.ViewIssue, cur.DependsOn, cur.DetectorWork = ev, w.Links.ViewIssue, w.DependsOn, w.DetectorWork
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
			for _, m := range wontfixRE.FindAllStringSubmatch(c.Body, -1) {
				for _, w := range fromIssue(items, is.URL) {
					if w.KeyHash == m[1] {
						move(w, ledger.WontFix, cfg.Now, "wont_fix:product_decision", "MAINTAINER_WONTFIX by "+c.Author)
					}
				}
			}
		}
	}
	res.Changed = changed(before, items)
	return res
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
