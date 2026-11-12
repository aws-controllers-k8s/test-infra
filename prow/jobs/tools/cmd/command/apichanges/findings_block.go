package apichanges

import "github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/apichanges/findingsblock"

// maxFindingsBlock caps the hidden findings block, so the visible body keeps
// most of GitHub's limit. Format drops "regen" lines first when it binds.
const maxFindingsBlock = 16 << 10

// name is the stable wire name of a fieldWork for the findings block. It is not
// hashed into the fingerprint, so adding a name re-files nothing.
func (w fieldWork) name() string {
	switch w {
	case workDedicatedSetter:
		return "dedicated-setter"
	case workListDiff:
		return "list-diff"
	case workCustomUpdate:
		return "custom-update"
	case workFromSibling:
		return "from-sibling"
	case workChangeList:
		return "change-list"
	case workSecondaryRead:
		return "secondary-read"
	case workCreateOnly:
		return "create-only"
	case workNotGenerated:
		return "not-generated"
	}
	return ""
}

// findingBucket mirrors the issue sections in resourceSections. A class with no
// bucket of its own that is not a field candidate goes to manual review, so a
// new class is never claimed as regeneration-only by default.
func findingBucket(f Finding) string {
	switch {
	case f.Class == ClassNewResource:
		return findingsblock.BucketNewResource
	case f.Class == ClassTransientResource, f.Class == ClassPossibleResource,
		!isFieldCandidate(f.Class):
		return findingsblock.BucketManualReview
	}
	if _, ok := needsWork(f); ok {
		return findingsblock.BucketNeedsConfig
	}
	return findingsblock.BucketRegen
}

func findingWork(f Finding) string {
	if f.Class == ClassLifecycleField && f.Work == workNone {
		return "request-only"
	}
	return f.Work.name()
}

// blockLines lists each actionable finding once by (Class, Kind, Subject), the
// identity fingerprintFindings hashes. When findings collide on that identity,
// the choice must not depend on input order: keep the higher-priority bucket,
// then the lexicographically smaller work name.
func blockLines(findings []Finding) []findingsblock.Line {
	index := map[[3]string]int{}
	var out []findingsblock.Line
	for _, f := range actionable(findings) {
		id := [3]string{f.Class.String(), f.Kind, f.Subject}
		l := findingsblock.Line{
			Class: id[0], Kind: f.Kind, Subject: f.Subject,
			Bucket: findingBucket(f), Work: findingWork(f),
		}
		i, ok := index[id]
		if !ok {
			index[id] = len(out)
			out = append(out, l)
			continue
		}
		cur := out[i]
		rl, rc := findingsblock.Rank(l.Bucket), findingsblock.Rank(cur.Bucket)
		if rl < rc || (rl == rc && l.Work < cur.Work) {
			out[i] = l
		}
	}
	return out
}
