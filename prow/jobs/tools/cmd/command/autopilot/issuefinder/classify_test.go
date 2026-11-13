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
	assert.Equal(t, "regen", bump.GapClass)
	assert.Equal(t, "api-change", bump.Source)
	assert.Equal(t, "s3", bump.Service)
	assert.Equal(t, 1, bump.Generation)
	assert.Equal(t, "v1.41.5", bump.Evidence.SDKFrom)
	assert.Equal(t, "service/s3/v1.97.3", bump.Evidence.SDKTo)

	mode := got["api-change|s3|Bucket|spec:Mode|needs-config"]
	require.NotNil(t, mode)
	assert.Equal(t, "config-only", mode.ChangeClass)
	assert.Equal(t, "create-only", mode.DetectorWork)
	assert.Equal(t, []string{bump.Key}, mode.DependsOn)
	assert.Equal(t, "Bucket", mode.Resource)
	assert.Equal(t, "Mode", mode.FieldPath)
	assert.Equal(t, 4, mode.PriorityBand)
	assert.Equal(t, "human-merge", mode.AutonomyLevel)
	assert.Equal(t, "needs-config", mode.GapClass)
	assert.Equal(t, "api-change", mode.Source)
	assert.Equal(t, "s3", mode.Service)
	assert.Equal(t, 1, mode.Generation)
	assert.Equal(t, "v1.41.5", mode.Evidence.SDKFrom)
	assert.Equal(t, "service/s3/v1.97.3", mode.Evidence.SDKTo)

	require.NotNil(t, got["api-change|s3|Bucket|spec:Policy|needs-config"])
	assert.Equal(t, "hand-written-code", got["api-change|s3|Bucket|spec:Policy|needs-config"].ChangeClass)

	table := got["api-change|s3|Table|new-resource"]
	require.NotNil(t, table)
	assert.Equal(t, "new-resource", table.ChangeClass)
	assert.Equal(t, "draft", table.AutonomyLevel)
	assert.Equal(t, 6, table.PriorityBand)
	assert.Equal(t, "new-resource", table.GapClass)
	assert.Equal(t, "api-change", table.Source)
	assert.Equal(t, "s3", table.Service)
	assert.Equal(t, 1, table.Generation)

	quote := got["api-change|s3|Quote|transient:Quote|manual-review"]
	require.NotNil(t, quote)
	assert.Equal(t, "unclassified", quote.ChangeClass)
	assert.Equal(t, "observe", quote.AutonomyLevel)
	assert.Equal(t, "transient-resource", quote.DetectorWork)
	assert.Empty(t, quote.FieldPath, "a resource class has no field")
	assert.Equal(t, "Quote", quote.Resource)
	assert.Equal(t, 6, quote.PriorityBand)
	assert.Equal(t, "manual-review", quote.GapClass)
	assert.Equal(t, "api-change", quote.Source)
	assert.Equal(t, "s3", quote.Service)
	assert.Equal(t, 1, quote.Generation)
}

const bumpKey = "sdk-bump|s3|*|service/s3/v1.97.3"

// needs-config lines are new SDK members, so they need the bump even when no
// regen line is present.
func TestClassifyNeedsConfigWithoutRegenStillDependsOnBump(t *testing.T) {
	got := classify(t, modeField)
	require.Len(t, got, 2)
	bump := got[bumpKey]
	require.NotNil(t, bump)
	assert.Empty(t, bump.Evidence.Findings)
	assert.Equal(t, []string{bumpKey}, got["api-change|s3|Bucket|spec:Mode|needs-config"].DependsOn)
}

// A truncated block drops regen lines first, so only needs-config lines may
// remain; the bump must still be emitted.
func TestClassifyTruncatedBlockStillEmitsBump(t *testing.T) {
	items, skipped := Classify(findingsblock.Block{Header: s3Header, Omitted: 3, Count: 1, Lines: []findingsblock.Line{modeField}}, issue27)
	require.Empty(t, skipped)
	got := byKey(items)
	require.Len(t, got, 2)
	require.NotNil(t, got[bumpKey])
	assert.Equal(t, []string{bumpKey}, got["api-change|s3|Bucket|spec:Mode|needs-config"].DependsOn)

	// Omitted alone, with no lines at all, also keeps the bump.
	items, skipped = Classify(findingsblock.Block{Header: s3Header, Omitted: 3}, issue27)
	require.Empty(t, skipped)
	require.Len(t, items, 1)
	assert.Equal(t, bumpKey, items[0].Key)
}

// Neither regen, needs-config nor omission: no bump.
func TestClassifyNoBumpWhenNothingNeedsIt(t *testing.T) {
	got := classify(t, newTable, quoteRes)
	assert.Len(t, got, 2)
	assert.Nil(t, got[bumpKey])
}

func TestClassifyConfigOnlyWork(t *testing.T) {
	for _, work := range []string{"create-only", "not-generated"} {
		l := modeField
		l.Work = work
		w := classify(t, l)["api-change|s3|Bucket|spec:Mode|needs-config"]
		require.NotNil(t, w, work)
		assert.Equal(t, "config-only", w.ChangeClass, work)
	}
}

// from-sibling is sent beside a field custom code reconciles, so adding it means
// updating that hook: it needs hand-written code, not only a generator.yaml change.
func TestClassifyFromSiblingIsHandWritten(t *testing.T) {
	l := modeField
	l.Work = "from-sibling"
	w := classify(t, l)["api-change|s3|Bucket|spec:Mode|needs-config"]
	require.NotNil(t, w)
	assert.Equal(t, "hand-written-code", w.ChangeClass)
	assert.Equal(t, "from-sibling", w.DetectorWork)
}

// A manual-review item stores the detector class in DetectorWork. FieldPath
// holds a field path only for a field class; for a resource class the Subject is
// the resource name, which Resource already holds.
func TestClassifyManualReviewFieldPath(t *testing.T) {
	field := findingsblock.Line{Class: "status-field", Kind: "Bucket", Subject: "Mode", Bucket: findingsblock.BucketManualReview}
	possible := findingsblock.Line{Class: "possible-resource", Kind: "Quote", Subject: "Quote", Bucket: findingsblock.BucketManualReview}
	got := classify(t, field, quoteRes, possible)

	f := got["api-change|s3|Bucket|status:Mode|manual-review"]
	require.NotNil(t, f)
	assert.Equal(t, "Mode", f.FieldPath)
	assert.Equal(t, "status-field", f.DetectorWork)

	for _, key := range []string{
		"api-change|s3|Quote|transient:Quote|manual-review",
		"api-change|s3|Quote|possible:Quote|manual-review",
	} {
		w := got[key]
		require.NotNil(t, w, key)
		assert.Empty(t, w.FieldPath, key)
		assert.Equal(t, "Quote", w.Resource, key)
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
	require.Len(t, got, 6, "five items and the bump")
	assert.Equal(t, "config-only", got["api-change|s3|Bucket|spec:Mode|needs-config"].ChangeClass)
	assert.Equal(t, "hand-written-code", got["api-change|s3|Bucket|status:Mode|needs-config"].ChangeClass)
	assert.Equal(t, "hand-written-code", got["api-change|s3|Bucket|lifecycle:Mode|needs-config"].ChangeClass)
	assert.NotNil(t, got["api-change|s3|Quote|transient:Quote|manual-review"])
	assert.NotNil(t, got["api-change|s3|Quote|possible:Quote|manual-review"])
}

// A class the issue finder does not know still gets its own key segment, with a
// "class=" prefix so it cannot collide with a known token.
func TestClassifyUnknownClassKeepsFullName(t *testing.T) {
	l := findingsblock.Line{Class: "future-class", Kind: "Quote", Subject: "Quote", Bucket: findingsblock.BucketManualReview}
	assert.NotNil(t, classify(t, l)["api-change|s3|Quote|class=future-class:Quote|manual-review"])
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
	assert.Len(t, skipped, len(bad)-1, "invalid Work does not skip the line")
	got := byKey(items)
	require.Len(t, got, 2)
	assert.Equal(t, []string{bumpKey}, got["api-change|s3|Bucket|spec:Mode|needs-config"].DependsOn)
	// The only regen line was skipped, but needs-config still needs the bump.
	assert.Empty(t, got[bumpKey].Evidence.Findings)
}

// Work is not part of any key, so an invalid Work must not skip the line. It is
// classified conservatively and the bad text is not copied.
func TestClassifyInvalidWorkIsHandWrittenNotSkipped(t *testing.T) {
	for _, work := range []string{"create\xff-only", "create�only"} {
		l := modeField
		l.Work = work
		w := classify(t, l)["api-change|s3|Bucket|spec:Mode|needs-config"]
		require.NotNil(t, w)
		assert.Equal(t, "hand-written-code", w.ChangeClass)
		assert.Equal(t, "", w.DetectorWork)
	}
}

// A "|" in a key segment would let two different lines share a key.
func TestClassifySkipsSeparatorInKeySegments(t *testing.T) {
	bad := []findingsblock.Line{
		{Class: "spec-field", Kind: "Buc|ket", Subject: "Mode", Bucket: findingsblock.BucketNeedsConfig, Work: "create-only"},
		{Class: "spec-field", Kind: "Bucket", Subject: "Mo|de", Bucket: findingsblock.BucketNeedsConfig, Work: "create-only"},
		{Class: "fu|ture", Kind: "Quote", Subject: "Quote", Bucket: findingsblock.BucketManualReview},
		{Class: "fu|ture", Kind: "Quote", Subject: "Quote", Bucket: findingsblock.BucketNeedsConfig},
		{Class: "future:x", Kind: "Quote", Subject: "Quote", Bucket: findingsblock.BucketManualReview},
		{Class: "new-resource", Kind: "Ta|ble", Subject: "Table", Bucket: findingsblock.BucketNewResource},
	}
	items, skipped := Classify(findingsblock.Block{Header: s3Header, Lines: append(bad, newTable)}, issue27)
	assert.Len(t, skipped, len(bad))
	require.Len(t, items, 1)
	assert.Equal(t, "api-change|s3|Table|new-resource", items[0].Key)
}

// An unknown class named like a known token must not collide with the known one.
func TestClassifyUnknownClassNamedLikeTokenDoesNotCollide(t *testing.T) {
	known := findingsblock.Line{Class: "spec-field", Kind: "Bucket", Subject: "Mode", Bucket: findingsblock.BucketManualReview}
	unknown := findingsblock.Line{Class: "spec", Kind: "Bucket", Subject: "Mode", Bucket: findingsblock.BucketManualReview}
	got := classify(t, known, unknown)
	require.Len(t, got, 2)
	assert.NotNil(t, got["api-change|s3|Bucket|spec:Mode|manual-review"])
	assert.NotNil(t, got["api-change|s3|Bucket|class=spec:Mode|manual-review"])
}

// A bad header value would poison every key built from it. The bump and every
// needs-config item need the bump, so they are skipped with it.
func TestClassifySkipsInvalidHeader(t *testing.T) {
	items, skipped := Classify(findingsblock.Block{
		Header: findingsblock.Header{Service: "s3", From: "v1", To: "v\xff2"},
		Lines:  []findingsblock.Line{regenABAC, modeField, newTable},
	}, issue27)
	assert.Len(t, skipped, 2, "the SDK-bump item and the needs-config item")
	require.Len(t, items, 1)
	assert.Equal(t, "api-change|s3|Table|new-resource", items[0].Key)

	items, skipped = Classify(findingsblock.Block{
		Header: findingsblock.Header{Service: "s�3", From: "v1", To: "v2"},
		Lines:  []findingsblock.Line{regenABAC, modeField},
	}, issue27)
	assert.Empty(t, items)
	assert.Len(t, skipped, 2)

	// A separator in the header would corrupt every key too.
	items, skipped = Classify(findingsblock.Block{
		Header: findingsblock.Header{Service: "s3", From: "v1", To: "v|2"},
		Lines:  []findingsblock.Line{modeField},
	}, issue27)
	assert.Empty(t, items)
	assert.Len(t, skipped, 2)
}

func TestClassifyIsOrderIndependent(t *testing.T) {
	z := findingsblock.Line{Class: "spec-field", Kind: "Bucket", Subject: "Z", Bucket: findingsblock.BucketRegen}
	// Two needs-config lines share a key but disagree on Work.
	modeCode := modeField
	modeCode.Work = "dedicated-setter"
	fwd := []findingsblock.Line{regenABAC, modeField, z, modeCode, regenABAC, newTable, newTable}
	rev := make([]findingsblock.Line, len(fwd))
	for i, l := range fwd {
		rev[len(fwd)-1-i] = l
	}
	a, _ := Classify(findingsblock.Block{Header: s3Header, Lines: fwd}, issue27)
	b, _ := Classify(findingsblock.Block{Header: s3Header, Lines: rev}, issue27)
	assert.Equal(t, a, b)
	got := byKey(a)
	require.Len(t, got, len(a), "duplicate keys are collapsed")
	assert.Equal(t, []string{"Bucket.ABAC", "Bucket.Z"}, got[bumpKey].Evidence.Findings)
}
