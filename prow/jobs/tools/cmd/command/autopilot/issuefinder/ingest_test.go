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

func TestIngestIsIdempotent(t *testing.T) {
	items := map[string]*ledger.WorkItem{}
	issues := []Issue{issueWith(t, 27, true, s3Header, regenABAC, modeField)}
	Ingest(cfg, issues, items)
	later := cfg
	later.Now = "2026-11-11T20:30:00Z"
	res := Ingest(later, issues, items)
	assert.Empty(t, res.Changed)
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
	res := Ingest(cfg, []Issue{stranger, unlabelled, twoServices, noBlock, wrongService}, items)
	assert.Empty(t, items)
	assert.Len(t, res.Skipped, 5)
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
	items[modeKey].State = ledger.Running

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
