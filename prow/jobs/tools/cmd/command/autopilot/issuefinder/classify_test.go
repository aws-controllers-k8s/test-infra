package issuefinder

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/apichanges/findingsblock"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

var (
	s3Header  = findingsblock.Header{Service: "s3", From: "v1.41.5", To: "service/s3/v1.97.3"}
	regenABAC = findingsblock.Line{Class: "spec-field", Kind: "Bucket", Subject: "ABAC", Bucket: findingsblock.BucketRegen}
	modeField = findingsblock.Line{Class: "spec-field", Kind: "Bucket", Subject: "Mode", Bucket: findingsblock.BucketNeedsConfig, Work: "create-only"}
	hookField = findingsblock.Line{Class: "spec-field", Kind: "Bucket", Subject: "Policy", Bucket: findingsblock.BucketNeedsConfig, Work: "dedicated-setter"}
	newTable  = findingsblock.Line{Class: "new-resource", Kind: "Table", Subject: "Table", Bucket: findingsblock.BucketNewResource}
	quoteRes  = findingsblock.Line{Class: "transient-resource", Kind: "Quote", Subject: "Quote", Bucket: findingsblock.BucketManualReview}
)

const issue27 = "https://github.com/aws-controllers-k8s/ack-private/issues/27"

func byKey(items []*ledger.WorkItem) map[string]*ledger.WorkItem {
	m := map[string]*ledger.WorkItem{}
	for _, w := range items {
		m[w.Key] = w
	}
	return m
}

func classify(t *testing.T, lines ...findingsblock.Line) map[string]*ledger.WorkItem {
	t.Helper()
	items, skipped := Classify(findingsblock.Block{Header: s3Header, Lines: lines}, issue27)
	require.Empty(t, skipped)
	got := byKey(items)
	require.Len(t, got, len(items), "duplicate keys")
	return got
}

func TestClassify(t *testing.T) {
	got := classify(t, regenABAC, modeField, hookField, newTable, quoteRes)
	require.Len(t, got, 5)

	bump := got["sdk-bump|s3|*|service/s3/v1.97.3"]
	require.NotNil(t, bump)
	assert.Equal(t, "SDK-bump", bump.ChangeClass)
	assert.Equal(t, "human-merge", bump.AutonomyLevel)
	assert.Equal(t, 3, bump.PriorityBand)
	assert.Equal(t, []string{"Bucket.ABAC"}, bump.Evidence.Findings)
	assert.Equal(t, "aws-controllers-k8s/s3-controller", bump.Repo)
	assert.Equal(t, issue27, bump.Links.ViewIssue)
	assert.Equal(t, ledger.KeyHash(bump.Key), bump.KeyHash)

	mode := got["api-change|s3|Bucket|spec:Mode|needs-config"]
	require.NotNil(t, mode)
	assert.Equal(t, "config-only", mode.ChangeClass)
	assert.Equal(t, "create-only", mode.DetectorWork)
	assert.Equal(t, []string{bump.Key}, mode.DependsOn)
	assert.Equal(t, "Bucket", mode.Resource)
	assert.Equal(t, "Mode", mode.FieldPath)

	require.NotNil(t, got["api-change|s3|Bucket|spec:Policy|needs-config"])
	assert.Equal(t, "hand-written-code", got["api-change|s3|Bucket|spec:Policy|needs-config"].ChangeClass)

	table := got["api-change|s3|Table|new-resource"]
	require.NotNil(t, table)
	assert.Equal(t, "new-resource", table.ChangeClass)
	assert.Equal(t, "draft", table.AutonomyLevel)

	quote := got["api-change|s3|Quote|transient:Quote|manual-review"]
	require.NotNil(t, quote)
	assert.Equal(t, "unclassified", quote.ChangeClass)
	assert.Equal(t, "observe", quote.AutonomyLevel)
	assert.Equal(t, "transient-resource", quote.DetectorWork)
}

func TestClassifyWithoutRegenHasNoBumpDependency(t *testing.T) {
	got, skipped := Classify(findingsblock.Block{Header: s3Header, Lines: []findingsblock.Line{modeField}}, issue27)
	require.Empty(t, skipped)
	require.Len(t, got, 1)
	assert.Empty(t, got[0].DependsOn)
}

func TestClassifyConfigOnlyWork(t *testing.T) {
	for _, work := range []string{"create-only", "from-sibling", "not-generated"} {
		l := modeField
		l.Work = work
		w := classify(t, l)["api-change|s3|Bucket|spec:Mode|needs-config"]
		require.NotNil(t, w, work)
		assert.Equal(t, "config-only", w.ChangeClass, work)
	}
}

// An unknown work name is treated conservatively: it may need code.
func TestClassifyUnknownWorkIsHandWritten(t *testing.T) {
	l := modeField
	l.Work = "brand-new-work-kind"
	w := classify(t, l)["api-change|s3|Bucket|spec:Mode|needs-config"]
	require.NotNil(t, w)
	assert.Equal(t, "hand-written-code", w.ChangeClass)
	assert.Equal(t, "brand-new-work-kind", w.DetectorWork)
}

// The detector can report one member path as both a Spec and a Status
// candidate (a request member and an uncalled read's response member), and
// blockLines keeps both because they differ in Class. They must not share a key.
func TestClassifyClassesWithSameKindAndSubjectDoNotCollide(t *testing.T) {
	spec := findingsblock.Line{Class: "spec-field", Kind: "Bucket", Subject: "Mode", Bucket: findingsblock.BucketNeedsConfig, Work: "create-only"}
	status := findingsblock.Line{Class: "status-field", Kind: "Bucket", Subject: "Mode", Bucket: findingsblock.BucketNeedsConfig, Work: "secondary-read"}
	life := findingsblock.Line{Class: "lifecycle-field", Kind: "Bucket", Subject: "Mode", Bucket: findingsblock.BucketNeedsConfig, Work: "request-only"}
	transient := findingsblock.Line{Class: "transient-resource", Kind: "Quote", Subject: "Quote", Bucket: findingsblock.BucketManualReview}
	possible := findingsblock.Line{Class: "possible-resource", Kind: "Quote", Subject: "Quote", Bucket: findingsblock.BucketManualReview}

	got := classify(t, spec, status, life, transient, possible)
	require.Len(t, got, 5)
	assert.Equal(t, "config-only", got["api-change|s3|Bucket|spec:Mode|needs-config"].ChangeClass)
	assert.Equal(t, "hand-written-code", got["api-change|s3|Bucket|status:Mode|needs-config"].ChangeClass)
	assert.Equal(t, "hand-written-code", got["api-change|s3|Bucket|lifecycle:Mode|needs-config"].ChangeClass)
	assert.NotNil(t, got["api-change|s3|Quote|transient:Quote|manual-review"])
	assert.NotNil(t, got["api-change|s3|Quote|possible:Quote|manual-review"])
}

// A class the issue finder does not know still gets its own key segment.
func TestClassifyUnknownClassKeepsFullName(t *testing.T) {
	l := findingsblock.Line{Class: "future-class", Kind: "Quote", Subject: "Quote", Bucket: findingsblock.BucketManualReview}
	assert.NotNil(t, classify(t, l)["api-change|s3|Quote|future-class:Quote|manual-review"])
}

// Ledger Lint rejects a key that is not valid UTF-8 or holds U+FFFD, and one
// bad item would block every push, so such a line is skipped and reported.
func TestClassifySkipsInvalidText(t *testing.T) {
	bad := []findingsblock.Line{
		{Class: "spec-field", Kind: "Buck\xffet", Subject: "Mode", Bucket: findingsblock.BucketNeedsConfig, Work: "create-only"},
		{Class: "spec-field", Kind: "Bucket", Subject: "Mo�de", Bucket: findingsblock.BucketNeedsConfig, Work: "create-only"},
		{Class: "spec-field", Kind: "Bucket", Subject: "Mode", Bucket: findingsblock.BucketNeedsConfig, Work: "create\xff-only"},
		{Class: "spec\xff-field", Kind: "Bucket", Subject: "Mode", Bucket: findingsblock.BucketNeedsConfig, Work: "create-only"},
		{Class: "spec-field", Kind: "Bucket", Subject: "AB\xffAC", Bucket: findingsblock.BucketRegen},
		{Class: "new-resource", Kind: "Ta�ble", Subject: "Table", Bucket: findingsblock.BucketNewResource},
	}
	items, skipped := Classify(findingsblock.Block{
		Header: s3Header,
		Lines:  append(bad, modeField),
	}, issue27)
	assert.Len(t, skipped, len(bad))
	require.Len(t, items, 1)
	assert.Equal(t, "api-change|s3|Bucket|spec:Mode|needs-config", items[0].Key)
	// The only regen line was skipped, so there is no bump to depend on.
	assert.Empty(t, items[0].DependsOn)
}

// A bad header value would poison every key built from it.
func TestClassifySkipsInvalidHeader(t *testing.T) {
	items, skipped := Classify(findingsblock.Block{
		Header: findingsblock.Header{Service: "s3", From: "v1", To: "v\xff2"},
		Lines:  []findingsblock.Line{regenABAC, modeField},
	}, issue27)
	assert.Len(t, skipped, 1, "the SDK-bump item")
	require.Len(t, items, 1)
	assert.Empty(t, items[0].DependsOn)

	items, skipped = Classify(findingsblock.Block{
		Header: findingsblock.Header{Service: "s�3", From: "v1", To: "v2"},
		Lines:  []findingsblock.Line{regenABAC, modeField},
	}, issue27)
	assert.Empty(t, items)
	assert.Len(t, skipped, 2)
}

func TestClassifyIsOrderIndependent(t *testing.T) {
	a, _ := Classify(findingsblock.Block{Header: s3Header, Lines: []findingsblock.Line{
		regenABAC, modeField, {Class: "spec-field", Kind: "Bucket", Subject: "Z", Bucket: findingsblock.BucketRegen},
	}}, issue27)
	b, _ := Classify(findingsblock.Block{Header: s3Header, Lines: []findingsblock.Line{
		{Class: "spec-field", Kind: "Bucket", Subject: "Z", Bucket: findingsblock.BucketRegen}, modeField, regenABAC,
	}}, issue27)
	assert.Equal(t, byKey(a), byKey(b))
	assert.Equal(t, []string{"Bucket.ABAC", "Bucket.Z"}, byKey(a)["sdk-bump|s3|*|service/s3/v1.97.3"].Evidence.Findings)
}
