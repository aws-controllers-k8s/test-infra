package issuefinder

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/apichanges/findingsblock"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

var cfg = Config{BotLogin: "ack-bot", Team: map[string]bool{"maintainer": true}, Now: "2026-11-11T08:30:00Z"}

func issueWith(t *testing.T, n int, open bool, h findingsblock.Header, lines ...findingsblock.Line) Issue {
	block, omitted := findingsblock.Format(h, lines, 1<<16)
	require.Zero(t, omitted)
	return Issue{
		Number: n, URL: fmt.Sprintf("https://github.com/aws-controllers-k8s/ack-private/issues/%d", n),
		Open: open, Author: "ack-bot",
		Labels: []string{"ack/api-change-detected", "kind/api-change", "service/s3"},
		Body:   "<!-- ack-api-change-begin -->\nprose\n" + block + "<!-- ack-api-change-end -->\n",
	}
}

// truncate marks an issue's block as having dropped one line.
func truncate(t *testing.T, is Issue) Issue {
	t.Helper()
	require.Contains(t, is.Body, "omitted=0")
	is.Body = strings.Replace(is.Body, "omitted=0", "omitted=1", 1)
	return is
}

// bumpKey is declared in classify_test.go.
const modeKey = "api-change|s3|Bucket|spec:Mode|needs-config"

var regenZeta = findingsblock.Line{Class: "spec-field", Kind: "Bucket", Subject: "Zeta", Bucket: findingsblock.BucketRegen}

func TestIngestCreatesItems(t *testing.T) {
	items := map[string]*ledger.WorkItem{}
	res := Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, regenABAC, modeField, newTable, quoteRes)}, items)

	assert.ElementsMatch(t, []string{
		bumpKey, modeKey, "api-change|s3|Table|new-resource", "api-change|s3|Quote|transient:Quote|manual-review",
	}, res.Changed)
	assert.Empty(t, res.Skipped)
	for _, w := range items {
		assert.Equal(t, ledger.Detected, w.State)
		assert.Equal(t, []ledger.Transition{{TS: cfg.Now, To: ledger.Detected, By: "issue-finder", Reason: "issue #27"}}, w.History)
	}
	assert.Equal(t, []string{bumpKey}, items[modeKey].DependsOn)
}

// historyLen counts every history entry across items.
func historyLen(items map[string]*ledger.WorkItem) int {
	n := 0
	for _, w := range items {
		n += len(w.History)
	}
	return n
}

func TestIngestIsIdempotent(t *testing.T) {
	later := cfg
	later.Now = "2026-11-11T20:30:00Z"
	wontfix := issueWith(t, 27, true, s3Header, regenABAC, modeField)
	wontfix.Comments = []Comment{{Author: "maintainer", Body: "/autopilot wontfix " + ledger.KeyHash(modeKey)}}
	for _, tc := range []struct {
		name   string
		issues []Issue
	}{
		{"refresh", []Issue{issueWith(t, 27, true, s3Header, regenABAC, modeField)}},
		{"supersede", []Issue{issueWith(t, 27, true, s3Header, regenABAC)}},
		{"close", []Issue{issueWith(t, 27, false, s3Header, regenABAC, modeField)}},
		{"wontfix", []Issue{wontfix}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := map[string]*ledger.WorkItem{}
			Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, regenABAC, modeField)}, items)
			Ingest(cfg, tc.issues, items)
			n := historyLen(items)
			res := Ingest(later, tc.issues, items)
			assert.Empty(t, res.Changed)
			assert.Equal(t, n, historyLen(items), "new history entries")
		})
	}
}

func TestIngestIsOrderIndependent(t *testing.T) {
	// The maintainer closed #27; the detector then filed #40 re-listing the
	// same findings.
	seed := func() map[string]*ledger.WorkItem {
		items := map[string]*ledger.WorkItem{}
		Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, regenABAC, modeField)}, items)
		return items
	}
	closed := issueWith(t, 27, false, s3Header, regenABAC, modeField)
	open := issueWith(t, 40, true, s3Header, regenABAC, modeField)

	a, b := seed(), seed()
	resA := Ingest(cfg, []Issue{closed, open}, a)
	resB := Ingest(cfg, []Issue{open, closed}, b)
	assert.Equal(t, resA, resB)
	for k := range a {
		wa, err := ledger.Marshal(a[k])
		require.NoError(t, err)
		wb, err := ledger.Marshal(b[k])
		require.NoError(t, err)
		assert.Equal(t, string(wa), string(wb), k)
	}
	assert.Len(t, b, len(a))
}

func TestIngestClosedIssueWinsOverNewerOpenIssue(t *testing.T) {
	items := map[string]*ledger.WorkItem{}
	Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, regenABAC, modeField)}, items)

	res := Ingest(cfg, []Issue{issueWith(t, 40, true, s3Header, regenABAC, modeField), issueWith(t, 27, false, s3Header)}, items)
	for _, k := range []string{bumpKey, modeKey} {
		assert.Equal(t, ledger.WontFix, items[k].State, k)
		assert.Equal(t, &ledger.Verdict{Outcome: "wont_fix:product_decision", Reason: "ISSUE_CLOSED"}, items[k].Verdict, k)
	}
	// WONT_FIX is a deliberate dismissal: its reappearance is not reported.
	assert.Empty(t, res.Skipped)
}

func TestIngestNewestOpenIssueOwnsSharedKeys(t *testing.T) {
	older := issueWith(t, 30, true, s3Header, regenABAC, modeField)
	newer := issueWith(t, 40, true, s3Header, regenABAC, modeField)
	for _, order := range [][]Issue{{older, newer}, {newer, older}} {
		items := map[string]*ledger.WorkItem{}
		Ingest(cfg, order, items)
		assert.Equal(t, newer.URL, items[modeKey].Links.ViewIssue)
		assert.Equal(t, newer.URL, items[bumpKey].Links.ViewIssue)

		// Swapping the input order on the next run changes nothing.
		res := Ingest(cfg, []Issue{order[1], order[0]}, items)
		assert.Empty(t, res.Changed)
	}
}

func TestIngestRefreshUpdatesClassification(t *testing.T) {
	items := map[string]*ledger.WorkItem{}
	Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, modeField)}, items)
	require.Equal(t, "config-only", items[modeKey].ChangeClass)

	hook := modeField
	hook.Work = "custom-hook"
	res := Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, hook)}, items)
	assert.Equal(t, []string{modeKey}, res.Changed)
	w := items[modeKey]
	assert.Equal(t, "hand-written-code", w.ChangeClass)
	assert.Equal(t, "custom-hook", w.DetectorWork)
	assert.Equal(t, findingsblock.BucketNeedsConfig, w.GapClass)
	assert.Equal(t, "human-merge", w.AutonomyLevel)
	assert.Equal(t, 4, w.PriorityBand)
	assert.Equal(t, "Bucket", w.Resource)
	assert.Equal(t, "Mode", w.FieldPath)
	assert.Equal(t, ledger.Detected, w.State)
}

func TestIngestRefreshKeepsTriagedClassification(t *testing.T) {
	is := issueWith(t, 27, true, s3Header, regenABAC, modeField)
	items := map[string]*ledger.WorkItem{}
	Ingest(cfg, []Issue{is}, items)
	// The Dispatcher's verdict may only make the class or level stricter.
	w := items[modeKey]
	require.Equal(t, "config-only", w.ChangeClass)
	w.State = ledger.Triaged
	w.ChangeClass, w.GapClass, w.AutonomyLevel, w.PriorityBand = "hand-written-code", "triaged-gap", "human-merge", 1
	w.History = append(w.History, ledger.Transition{TS: cfg.Now, To: ledger.Triaged, By: "dispatcher"})
	want, err := ledger.Marshal(w)
	require.NoError(t, err)

	// The block still says config-only work.
	res := Ingest(cfg, []Issue{is}, items)
	assert.Empty(t, res.Changed)
	got, err := ledger.Marshal(items[modeKey])
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got))

	// Evidence still refreshes on a triaged item; the triage verdict stays.
	rebased := s3Header
	rebased.From = "v1.50.0"
	res = Ingest(cfg, []Issue{issueWith(t, 27, true, rebased, regenABAC, modeField)}, items)
	assert.Contains(t, res.Changed, modeKey)
	w = items[modeKey]
	assert.Equal(t, "v1.50.0", w.Evidence.SDKFrom)
	assert.Equal(t, "hand-written-code", w.ChangeClass)
	assert.Equal(t, "triaged-gap", w.GapClass)
	assert.Equal(t, "human-merge", w.AutonomyLevel)
	assert.Equal(t, 1, w.PriorityBand)
	assert.Equal(t, ledger.Triaged, w.State)
}

// Items ingested before FieldPath was cleared for resource classes still carry
// the Subject there. Refreshing clears it without reclassifying or re-keying.
func TestIngestClearsStaleFieldPathOnResourceManualReview(t *testing.T) {
	const quoteKey = "api-change|s3|Quote|transient:Quote|manual-review"
	items := map[string]*ledger.WorkItem{}
	Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, quoteRes)}, items)
	w := items[quoteKey]
	require.NotNil(t, w)
	w.FieldPath = "Quote"
	w.State = ledger.Triaged
	w.History = append(w.History, ledger.Transition{TS: cfg.Now, To: ledger.Triaged, By: "dispatcher"})

	Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, quoteRes)}, items)
	w = items[quoteKey]
	require.NotNil(t, w, "the key still uses Subject")
	assert.Empty(t, w.FieldPath)
	assert.Equal(t, "transient-resource", w.DetectorWork)
	assert.Equal(t, ledger.Triaged, w.State, "DetectorWork is unchanged, so no reclassification")
}

func TestIngestRetriagesWhenDetectorReclassifies(t *testing.T) {
	items := map[string]*ledger.WorkItem{}
	Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, regenABAC, modeField)}, items)
	w := items[modeKey]
	require.Equal(t, "config-only", w.ChangeClass)
	require.Equal(t, "create-only", w.DetectorWork)
	w.State = ledger.Triaged
	w.History = append(w.History, ledger.Transition{TS: cfg.Now, To: ledger.Triaged, By: "dispatcher"})

	// The detector now says the same key needs a hook.
	hooked := modeField
	hooked.Work = "hook"
	is := issueWith(t, 27, true, s3Header, regenABAC, hooked)
	res := Ingest(cfg, []Issue{is}, items)
	assert.Contains(t, res.Changed, modeKey)
	w = items[modeKey]
	assert.Equal(t, ledger.Detected, w.State)
	assert.Equal(t, "hook", w.DetectorWork)
	assert.Equal(t, "hand-written-code", w.ChangeClass)
	assert.Equal(t, "human-merge", w.AutonomyLevel)
	last := w.History[len(w.History)-1]
	assert.Equal(t, ledger.Detected, last.To)
	assert.Equal(t, "DETECTOR_RECLASSIFIED", last.Reason)
	assert.Equal(t, cfg.Now, last.TS)

	res = Ingest(cfg, []Issue{is}, items)
	assert.Empty(t, res.Changed)
}

func TestIngestDoesNotSupersedeKeyListedByAnotherOpenIssue(t *testing.T) {
	seed := func() map[string]*ledger.WorkItem {
		items := map[string]*ledger.WorkItem{}
		Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, regenABAC, modeField)}, items)
		require.Equal(t, issueWith(t, 27, true, s3Header).URL, items[modeKey].Links.ViewIssue)
		return items
	}
	// #27's complete block drops the key; open #40 still lists it.
	dropped := issueWith(t, 27, true, s3Header, regenABAC)
	lister := issueWith(t, 40, true, s3Header, regenABAC, modeField)

	a, b := seed(), seed()
	resA := Ingest(cfg, []Issue{dropped, lister}, a)
	resB := Ingest(cfg, []Issue{lister, dropped}, b)
	assert.Equal(t, resA, resB)
	for _, items := range []map[string]*ledger.WorkItem{a, b} {
		w := items[modeKey]
		assert.False(t, w.State.Terminal(), w.State)
		assert.Equal(t, ledger.Detected, w.State)
		assert.Nil(t, w.Verdict)
		assert.Equal(t, lister.URL, w.Links.ViewIssue)
	}
	for k := range a {
		wa, err := ledger.Marshal(a[k])
		require.NoError(t, err)
		wb, err := ledger.Marshal(b[k])
		require.NoError(t, err)
		assert.Equal(t, string(wa), string(wb), k)
	}
	assert.Len(t, b, len(a))
}

func TestIngestReportsTerminalReappearanceOncePerRun(t *testing.T) {
	items := map[string]*ledger.WorkItem{}
	Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, regenABAC, modeField)}, items)
	Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, regenABAC)}, items)
	require.Equal(t, ledger.Superseded, items[modeKey].State)
	want, err := ledger.Marshal(items[modeKey])
	require.NoError(t, err)

	// Two open issues bring the finding back in one run.
	res := Ingest(cfg, []Issue{issueWith(t, 30, true, s3Header, regenABAC, modeField), issueWith(t, 40, true, s3Header, regenABAC, modeField)}, items)
	got, err := ledger.Marshal(items[modeKey])
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got))
	assert.NotContains(t, res.Changed, modeKey)
	var hits []string
	for _, s := range res.Skipped {
		if strings.Contains(s, modeKey) {
			hits = append(hits, s)
		}
	}
	assert.Equal(t, []string{"#30: " + modeKey + ": finding reappeared but item is SUPERSEDED; not reopened"}, hits)
}

func TestIngestWontfixReappearanceIsSilent(t *testing.T) {
	items := map[string]*ledger.WorkItem{}
	is := issueWith(t, 27, true, s3Header, regenABAC, modeField)
	is.Comments = []Comment{{Author: "maintainer", Body: "/autopilot wontfix " + ledger.KeyHash(modeKey)}}
	Ingest(cfg, []Issue{is}, items)
	require.Equal(t, ledger.WontFix, items[modeKey].State)

	// A different issue lists the dismissed finding, run after run.
	for range 2 {
		res := Ingest(cfg, []Issue{is, issueWith(t, 40, true, s3Header, regenABAC, modeField)}, items)
		assert.Equal(t, ledger.WontFix, items[modeKey].State)
		assert.NotContains(t, res.Changed, modeKey)
		assert.Empty(t, res.Skipped)
	}
}

func TestIngestReportsUnmatchedWontfix(t *testing.T) {
	items := map[string]*ledger.WorkItem{}
	Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, regenABAC, modeField)}, items)

	is := issueWith(t, 27, true, s3Header, regenABAC, modeField)
	stray := ledger.KeyHash("api-change|s3|Bucket|spec:Nope|needs-config")
	is.Comments = []Comment{
		{Author: "maintainer", Body: "/autopilot wontfix " + stray},
		// Not a team member: ignored, not reported.
		{Author: "someone", Body: "/autopilot wontfix " + stray},
	}
	res := Ingest(cfg, []Issue{is}, items)
	assert.Empty(t, res.Changed)
	require.Len(t, res.Skipped, 1)
	assert.True(t, strings.HasPrefix(res.Skipped[0], "#27: "), res.Skipped[0])
	assert.Contains(t, res.Skipped[0], stray)
	assert.Contains(t, res.Skipped[0], "matches no finder-owned item")
}

func TestIngestAppliedWontfixIsSilentOnLaterRuns(t *testing.T) {
	items := map[string]*ledger.WorkItem{}
	Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, regenABAC, modeField)}, items)

	is := issueWith(t, 27, true, s3Header, regenABAC, modeField)
	cmd := "/autopilot wontfix " + ledger.KeyHash(modeKey)
	// A duplicate command in the same run is also silent once applied.
	is.Comments = []Comment{{Author: "maintainer", Body: cmd}, {Author: "maintainer", Body: cmd}}
	res := Ingest(cfg, []Issue{is}, items)
	assert.Equal(t, []string{modeKey}, res.Changed)
	assert.Empty(t, res.Skipped)
	require.Equal(t, ledger.WontFix, items[modeKey].State)

	// The issue still lists the finding and still carries the old comment.
	res = Ingest(cfg, []Issue{is}, items)
	assert.Empty(t, res.Changed)
	assert.Empty(t, res.Skipped)
}

func TestIngestWontfixOfTerminalItemIsSilent(t *testing.T) {
	for _, st := range []ledger.State{ledger.Done, ledger.Superseded, ledger.WontFix} {
		t.Run(string(st), func(t *testing.T) {
			items := map[string]*ledger.WorkItem{}
			Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, regenABAC, modeField)}, items)
			w := items[modeKey]
			w.State = st
			w.History = append(w.History, ledger.Transition{TS: cfg.Now, To: st, By: "test"})
			want, err := ledger.Marshal(w)
			require.NoError(t, err)

			// The finding is gone, but the old comment is still on the issue.
			is := issueWith(t, 27, true, s3Header, regenABAC)
			is.Comments = []Comment{{Author: "maintainer", Body: "/autopilot wontfix " + ledger.KeyHash(modeKey)}}
			res := Ingest(cfg, []Issue{is}, items)
			assert.NotContains(t, res.Changed, modeKey)
			assert.Empty(t, res.Skipped)
			got, err := ledger.Marshal(items[modeKey])
			require.NoError(t, err)
			assert.Equal(t, string(want), string(got))
		})
	}
}

func TestIngestSameIssueTerminalItemIsSilent(t *testing.T) {
	items := map[string]*ledger.WorkItem{}
	Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, regenABAC, modeField)}, items)
	Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, regenABAC)}, items)
	require.Equal(t, ledger.Superseded, items[modeKey].State)

	// #27 lists the finding again: same issue, so nothing to report.
	res := Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, regenABAC, modeField)}, items)
	assert.Empty(t, res.Changed)
	assert.Empty(t, res.Skipped)
}

func TestIngestReportsWontfixOfDispatchedItem(t *testing.T) {
	items := map[string]*ledger.WorkItem{}
	Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, regenABAC, modeField)}, items)
	w := items[modeKey]
	w.State = ledger.Running
	w.History = append(w.History, ledger.Transition{TS: cfg.Now, To: ledger.Running, By: "workflow"})

	is := issueWith(t, 27, true, s3Header, regenABAC, modeField)
	is.Comments = []Comment{{Author: "maintainer", Body: "/autopilot wontfix " + ledger.KeyHash(modeKey)}}
	res := Ingest(cfg, []Issue{is}, items)
	assert.Equal(t, ledger.Running, items[modeKey].State)
	require.Len(t, res.Skipped, 1)
	assert.Contains(t, res.Skipped[0], "matches no finder-owned item")
}

func TestIngestIgnoresWontfixInFencedCode(t *testing.T) {
	items := map[string]*ledger.WorkItem{}
	Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, regenABAC, modeField)}, items)

	cmd := "/autopilot wontfix " + ledger.KeyHash(modeKey)
	is := issueWith(t, 27, true, s3Header, regenABAC, modeField)
	is.Comments = []Comment{
		{Author: "maintainer", Body: "To dismiss, run:\n```\n" + cmd + "\n```\n"},
		{Author: "maintainer", Body: "Or:\n~~~text\n" + cmd + "\n~~~\n"},
		// A longer opening fence is not closed by a shorter one.
		{Author: "maintainer", Body: "````\n```\n" + cmd + "\n````\n"},
		// An unclosed fence runs to the end of the comment.
		{Author: "maintainer", Body: "```\n" + cmd + "\n"},
	}
	res := Ingest(cfg, []Issue{is}, items)
	assert.Empty(t, res.Changed)
	assert.Equal(t, ledger.Detected, items[modeKey].State)

	// After a closed fence, a command counts again.
	is.Comments = []Comment{{Author: "maintainer", Body: "```\nexample\n```\n" + cmd + "\n"}}
	Ingest(cfg, []Issue{is}, items)
	assert.Equal(t, ledger.WontFix, items[modeKey].State)
}

func TestIngestSkipsUntrustedIssues(t *testing.T) {
	stranger := issueWith(t, 1, true, s3Header, modeField)
	stranger.Author = "someone"
	unlabelled := issueWith(t, 2, true, s3Header, modeField)
	unlabelled.Labels = []string{"service/s3"}
	twoServices := issueWith(t, 3, true, s3Header, modeField)
	twoServices.Labels = append(twoServices.Labels, "service/iam")
	noBlock := issueWith(t, 4, true, s3Header, modeField)
	noBlock.Body = "<!-- ack-api-change-begin -->\nprose only\n<!-- ack-api-change-end -->\n"
	wrongService := issueWith(t, 5, true, findingsblock.Header{Service: "iam", From: "a", To: "b"}, modeField)

	items := map[string]*ledger.WorkItem{}
	res := Ingest(cfg, []Issue{wrongService, stranger, unlabelled, twoServices, noBlock}, items)
	assert.Empty(t, items)
	want := []string{
		`#1: author "someone" is not "ack-bot"`,
		"#2: missing label ack/api-change-detected",
		"#3: want exactly one service/ label, got 2",
		"#4: no findings block",
		`#5: block service "iam" != label service "s3"`,
	}
	require.Len(t, res.Skipped, len(want))
	for i, w := range want {
		assert.Contains(t, res.Skipped[i], w)
	}
}

func TestIngestSkipsIssueWithoutRegion(t *testing.T) {
	is := issueWith(t, 27, true, s3Header, modeField)
	// The block is there, but outside any generated region.
	is.Body = strings.NewReplacer("<!-- ack-api-change-begin -->\n", "", "<!-- ack-api-change-end -->\n", "").Replace(is.Body)
	require.Contains(t, is.Body, findingsblock.Begin)

	items := map[string]*ledger.WorkItem{}
	res := Ingest(cfg, []Issue{is}, items)
	assert.Empty(t, items)
	require.Len(t, res.Skipped, 1)
	assert.Contains(t, res.Skipped[0], "#27: ")
	assert.Contains(t, res.Skipped[0], "generated region")
}

func TestIngestIgnoresBlockQuotedOutsideRegion(t *testing.T) {
	is := issueWith(t, 27, true, s3Header, modeField)
	// A maintainer's note quoting another block, one that would add a finding
	// and, read as a second block, make the issue unparseable.
	quoted, _ := findingsblock.Format(s3Header, []findingsblock.Line{newTable}, 1<<16)
	is.Body += "\n> Maintainer note, pasted from an old issue:\n" + quoted

	items := map[string]*ledger.WorkItem{}
	res := Ingest(cfg, []Issue{is}, items)
	assert.Empty(t, res.Skipped)
	assert.ElementsMatch(t, []string{bumpKey, modeKey}, res.Changed)
	assert.NotContains(t, items, "api-change|s3|Table|new-resource")
}

func TestIngestSupersedesGoneFindings(t *testing.T) {
	items := map[string]*ledger.WorkItem{}
	Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, regenABAC, modeField)}, items)

	res := Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, regenABAC)}, items)
	assert.Equal(t, []string{modeKey}, res.Changed)
	assert.Equal(t, ledger.Superseded, items[modeKey].State)
	assert.Equal(t, &ledger.Verdict{Outcome: "superseded", Reason: "FINDING_GONE"}, items[modeKey].Verdict)
	// The bump no longer has a dependent, but is otherwise unchanged.
	assert.Equal(t, ledger.Detected, items[bumpKey].State)
}

func TestIngestKeepsItemsWhenBlockWasTruncated(t *testing.T) {
	items := map[string]*ledger.WorkItem{}
	Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, regenABAC, modeField)}, items)

	Ingest(cfg, []Issue{truncate(t, issueWith(t, 27, true, s3Header, regenABAC))}, items)
	assert.Equal(t, ledger.Detected, items[modeKey].State)
}

func TestIngestKeepsItemsWhenClassifySkipped(t *testing.T) {
	items := map[string]*ledger.WorkItem{}
	Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, regenABAC, modeField)}, items)

	// Classify cannot key a subject holding "|", so the desired set is
	// incomplete and nothing may be superseded.
	bad := modeField
	bad.Subject = "Mo|de"
	res := Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, regenABAC, bad)}, items)
	assert.Equal(t, ledger.Detected, items[modeKey].State)
	assert.Empty(t, res.Changed)
	require.Len(t, res.Skipped, 1)
	assert.True(t, strings.HasPrefix(res.Skipped[0], "#27: finding "), res.Skipped[0])
}

func TestIngestIncompleteBlockKeepsBumpEvidence(t *testing.T) {
	items := map[string]*ledger.WorkItem{}
	Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, regenABAC, regenZeta, modeField)}, items)
	require.Equal(t, []string{"Bucket.ABAC", "Bucket.Zeta"}, items[bumpKey].Evidence.Findings)

	// Truncation drops regen lines first; the bump must not shrink.
	res := Ingest(cfg, []Issue{truncate(t, issueWith(t, 27, true, s3Header, regenABAC, modeField))}, items)
	assert.Empty(t, res.Changed)
	assert.Equal(t, []string{"Bucket.ABAC", "Bucket.Zeta"}, items[bumpKey].Evidence.Findings)

	// Every regen line dropped: Classify still emits the bump, with no findings.
	res = Ingest(cfg, []Issue{truncate(t, issueWith(t, 27, true, s3Header, modeField))}, items)
	assert.Empty(t, res.Changed)
	assert.Equal(t, []string{"Bucket.ABAC", "Bucket.Zeta"}, items[bumpKey].Evidence.Findings)

	// A new finding in an incomplete block is added (union).
	regenNew := regenZeta
	regenNew.Subject = "New"
	res = Ingest(cfg, []Issue{truncate(t, issueWith(t, 27, true, s3Header, regenNew, modeField))}, items)
	assert.Equal(t, []string{bumpKey}, res.Changed)
	assert.Equal(t, []string{"Bucket.ABAC", "Bucket.New", "Bucket.Zeta"}, items[bumpKey].Evidence.Findings)

	// A complete block is authoritative and replaces the list.
	res = Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, regenABAC, modeField)}, items)
	assert.Equal(t, []string{bumpKey}, res.Changed)
	assert.Equal(t, []string{"Bucket.ABAC"}, items[bumpKey].Evidence.Findings)
}

func TestIngestNewSDKTargetSupersedesOldBump(t *testing.T) {
	items := map[string]*ledger.WorkItem{}
	Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, regenABAC)}, items)

	newer := s3Header
	newer.To = "service/s3/v1.98.0"
	Ingest(cfg, []Issue{issueWith(t, 27, true, newer, regenABAC)}, items)
	assert.Equal(t, ledger.Superseded, items[bumpKey].State)
	assert.Equal(t, ledger.Detected, items["sdk-bump|s3|*|service/s3/v1.98.0"].State)
}

func TestIngestClosedIssueDismissesItsItems(t *testing.T) {
	items := map[string]*ledger.WorkItem{}
	Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, regenABAC, modeField)}, items)

	res := Ingest(cfg, []Issue{issueWith(t, 27, false, s3Header, regenABAC, modeField)}, items)
	assert.ElementsMatch(t, []string{bumpKey, modeKey}, res.Changed)
	for _, w := range items {
		assert.Equal(t, ledger.WontFix, w.State)
		assert.Equal(t, &ledger.Verdict{Outcome: "wont_fix:product_decision", Reason: "ISSUE_CLOSED"}, w.Verdict)
	}
}

func TestIngestWontfixCommandOnlyFromTeam(t *testing.T) {
	items := map[string]*ledger.WorkItem{}
	is := issueWith(t, 27, true, s3Header, modeField)
	Ingest(cfg, []Issue{is}, items)
	hash := ledger.KeyHash(modeKey)

	is.Comments = []Comment{{Author: "someone", Body: "/autopilot wontfix " + hash}}
	Ingest(cfg, []Issue{is}, items)
	assert.Equal(t, ledger.Detected, items[modeKey].State)

	is.Comments = append(is.Comments, Comment{Author: "maintainer", Body: "not needed\n/autopilot wontfix " + hash + "\n"})
	Ingest(cfg, []Issue{is}, items)
	assert.Equal(t, ledger.WontFix, items[modeKey].State)
	assert.Equal(t, "MAINTAINER_WONTFIX by maintainer", items[modeKey].Verdict.Reason)
	assert.Equal(t, ledger.Detected, items[bumpKey].State)
}

func TestIngestLeavesDispatchedItemsAlone(t *testing.T) {
	items := map[string]*ledger.WorkItem{}
	Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, modeField)}, items)
	w := items[modeKey]
	w.State = ledger.Running
	w.History = append(w.History,
		ledger.Transition{TS: cfg.Now, To: ledger.Queued, By: "triage"},
		ledger.Transition{TS: cfg.Now, To: ledger.Running, By: "dispatcher"})

	Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header)}, items)
	assert.Equal(t, ledger.Running, items[modeKey].State)
	Ingest(cfg, []Issue{issueWith(t, 27, false, s3Header)}, items)
	assert.Equal(t, ledger.Running, items[modeKey].State)
}

func TestIngestLeavesOnceDispatchedItemsAlone(t *testing.T) {
	items := map[string]*ledger.WorkItem{}
	Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, modeField)}, items)
	// A workflow ran the item and requeued it: pre-dispatch state, but it is
	// no longer the finder's.
	w := items[modeKey]
	w.History = append(w.History,
		ledger.Transition{TS: cfg.Now, To: ledger.Queued, By: "triage"},
		ledger.Transition{TS: cfg.Now, To: ledger.Running, By: "dispatcher"},
		ledger.Transition{TS: cfg.Now, To: ledger.Queued, By: "dispatcher", Reason: "requeue"})
	w.State = ledger.Queued
	require.False(t, w.FinderOwned())
	want, err := ledger.Marshal(w)
	require.NoError(t, err)
	unchanged := func(step string) {
		t.Helper()
		got, err := ledger.Marshal(items[modeKey])
		require.NoError(t, err)
		assert.Equal(t, string(want), string(got), step)
	}

	// Refresh: a new baseline would rewrite Evidence.SDKFrom on an owned item.
	rebased := s3Header
	rebased.From = "v1.50.0"
	res := Ingest(cfg, []Issue{issueWith(t, 27, true, rebased, modeField)}, items)
	unchanged("refresh")
	assert.NotContains(t, res.Changed, modeKey)

	// Wontfix from a team member.
	is := issueWith(t, 27, true, s3Header, modeField)
	is.Comments = []Comment{{Author: "maintainer", Body: "/autopilot wontfix " + ledger.KeyHash(modeKey)}}
	Ingest(cfg, []Issue{is}, items)
	unchanged("wontfix")

	// Finding gone from a complete block.
	Ingest(cfg, []Issue{issueWith(t, 27, true, s3Header, regenABAC)}, items)
	unchanged("finding gone")

	// Issue closed.
	Ingest(cfg, []Issue{issueWith(t, 27, false, s3Header, regenABAC)}, items)
	unchanged("close")
}

func TestIngestEmptyCommentAuthorIsNeverTeam(t *testing.T) {
	// Even a Team map that somehow holds "" must not let an authorless
	// comment (a deleted account, say) act.
	loose := cfg
	loose.Team = map[string]bool{"": true}
	items := map[string]*ledger.WorkItem{}
	is := issueWith(t, 27, true, s3Header, modeField)
	Ingest(loose, []Issue{is}, items)

	is.Comments = []Comment{{Author: "", Body: "/autopilot wontfix " + ledger.KeyHash(modeKey)}}
	Ingest(loose, []Issue{is}, items)
	assert.Equal(t, ledger.Detected, items[modeKey].State)
}

func TestIngestEmptyBotLoginTrustsNothing(t *testing.T) {
	noBot := cfg
	noBot.BotLogin = ""
	is := issueWith(t, 27, true, s3Header, modeField)
	is.Author = ""

	items := map[string]*ledger.WorkItem{}
	res := Ingest(noBot, []Issue{is}, items)
	assert.Empty(t, items)
	assert.Empty(t, res.Changed)
	require.Len(t, res.Skipped, 1)
	assert.Contains(t, res.Skipped[0], "#27: no bot login configured")
}
