package apichanges

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/apichanges/findingsblock"
)

func TestFindingBucket(t *testing.T) {
	for _, tc := range []struct {
		f    Finding
		want string
	}{
		{Finding{Class: ClassNewResource}, findingsblock.BucketNewResource},
		{Finding{Class: ClassTransientResource}, findingsblock.BucketManualReview},
		{Finding{Class: ClassPossibleResource}, findingsblock.BucketManualReview},
		{Finding{Class: ClassSpecField, Work: workCreateOnly, Detail: "set only on Create"}, findingsblock.BucketNeedsConfig},
		{Finding{Class: ClassLifecycleField}, findingsblock.BucketNeedsConfig},
		{Finding{Class: ClassSpecField}, findingsblock.BucketRegen},
		{Finding{Class: ClassStatusField}, findingsblock.BucketRegen},
		// A class that is not a field candidate, with no bucket of its own, goes
		// to manual review rather than being claimed by a regeneration.
		{Finding{Class: ClassNewOperation}, findingsblock.BucketManualReview},
		{Finding{Class: FindingClass(99), Work: workCustomUpdate}, findingsblock.BucketManualReview},
	} {
		assert.Equal(t, tc.want, findingBucket(tc.f), tc.f.Class.String())
	}
}

func TestBlockLines(t *testing.T) {
	findings := append(sampleFindings(),
		// Same identity as sampleFindings' Description: listed once.
		Finding{Kind: "Widget", Class: ClassSpecField, Subject: "Description", NewSincePin: true, Evidence: "UpdateWidget"},
		Finding{Kind: "Widget", Class: ClassLifecycleField, Subject: "QuoteId", NewSincePin: true},
		// Not new since the pin: not actionable.
		Finding{Kind: "Widget", Class: ClassSpecField, Subject: "Old"},
	)
	assert.Equal(t, []findingsblock.Line{
		{Class: "new-resource", Kind: "Gizmo", Subject: "Gizmo", Bucket: "new-resource"},
		{Class: "spec-field", Kind: "Widget", Subject: "Description", Bucket: "regen"},
		{Class: "lifecycle-field", Kind: "Widget", Subject: "QuoteId", Bucket: "needs-config", Work: "request-only"},
	}, blockLines(findings))
}

func TestBlockLinesDuplicateTieBreakIsOrderIndependent(t *testing.T) {
	regen := Finding{Kind: "Widget", Class: ClassSpecField, Subject: "Mode", NewSincePin: true}
	cfg := Finding{Kind: "Widget", Class: ClassSpecField, Subject: "Mode", NewSincePin: true,
		Work: workCreateOnly, Detail: "set only on Create"}
	other := Finding{Kind: "Widget", Class: ClassSpecField, Subject: "Other", NewSincePin: true}

	a := blockLines([]Finding{other, regen, cfg})
	b := blockLines([]Finding{other, cfg, regen})
	assert.Equal(t, a, b)
	assert.Contains(t, a, findingsblock.Line{
		Class: "spec-field", Kind: "Widget", Subject: "Mode",
		Bucket: findingsblock.BucketNeedsConfig, Work: "create-only"})
	assert.NotContains(t, a, findingsblock.Line{
		Class: "spec-field", Kind: "Widget", Subject: "Mode", Bucket: findingsblock.BucketRegen})
}

func TestBlockLinesDuplicateTieBreakBothNeedsConfig(t *testing.T) {
	custom := Finding{Kind: "Widget", Class: ClassSpecField, Subject: "Mode", NewSincePin: true, Work: workCustomUpdate}
	create := Finding{Kind: "Widget", Class: ClassSpecField, Subject: "Mode", NewSincePin: true, Work: workCreateOnly}

	for _, in := range [][]Finding{{custom, create}, {create, custom}} {
		got := blockLines(in)
		assert.Len(t, got, 1)
		// "create-only" < "custom-update", so it wins in either order.
		assert.Equal(t, []findingsblock.Line{{
			Class: "spec-field", Kind: "Widget", Subject: "Mode",
			Bucket: findingsblock.BucketNeedsConfig, Work: "create-only"}}, got)
	}
}

func TestFieldWorkNames(t *testing.T) {
	want := map[fieldWork]string{
		workDedicatedSetter: "dedicated-setter",
		workListDiff:        "list-diff",
		workCustomUpdate:    "custom-update",
		workFromSibling:     "from-sibling",
		workChangeList:      "change-list",
		workSecondaryRead:   "secondary-read",
		workCreateOnly:      "create-only",
		workNotGenerated:    "not-generated",
	}
	assert.Equal(t, "", workNone.name())
	seen := map[string]fieldWork{}
	for w := workNone + 1; w <= workNotGenerated; w++ {
		name := w.name()
		assert.NotEmpty(t, name, "fieldWork %d", int(w))
		if prev, dup := seen[name]; dup {
			t.Errorf("fieldWork %d and %d share the name %q", int(prev), int(w), name)
		}
		seen[name] = w
		assert.Equal(t, want[w], name, "fieldWork %d", int(w))
	}
	assert.Len(t, seen, len(want))
	// A value past the last constant has no name, so the loop bound above must
	// move when a constant is added.
	assert.Equal(t, "", (workNotGenerated + 1).name())
}
