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
