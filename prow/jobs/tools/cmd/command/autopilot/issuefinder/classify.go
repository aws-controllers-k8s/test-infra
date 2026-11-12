// Package issuefinder turns the findings block in detect-api-changes issues
// into ledger work items. It reads GitHub and never writes to it.
package issuefinder

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/apichanges/findingsblock"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

const source = "api-change"

// configOnlyWork is detector work a generator.yaml change alone can do. Any
// other work, including a name this package does not know yet, may need
// hand-written code.
var configOnlyWork = map[string]bool{
	"create-only":   true, // is_immutable
	"from-sibling":  true, // a field beside an existing from:
	"not-generated": true, // a from: field codegen does not add itself
}

// classToken shortens a detector FindingClass name for use in a key. A class
// not listed keeps its full name, so it still gets a distinct key.
var classToken = map[string]string{
	"spec-field":         "spec",
	"status-field":       "status",
	"lifecycle-field":    "lifecycle",
	"transient-resource": "transient",
	"possible-resource":  "possible",
}

// subjectKey is the Subject segment of a needs-config or manual-review key.
//
// A block line's identity is (Class, Kind, Subject), and Subject alone does not
// tell classes apart: for fields it is the bare member path. The detector can
// report one path as both a spec-field (a request member) and a status-field
// (a member of an uncalled read's response, readStatusCandidates), and
// blockLines keeps both because their Class differs. The class token keeps
// their keys apart.
func subjectKey(l findingsblock.Line) string {
	tok, ok := classToken[l.Class]
	if !ok {
		tok = l.Class
	}
	return tok + ":" + l.Subject
}

// validText reports whether s can go into a key. ledger.Lint rejects keys that
// are not valid UTF-8 or hold U+FFFD (what encoding/json writes for bad bytes),
// and one bad item would block every push.
func validText(s ...string) bool {
	for _, v := range s {
		if !utf8.ValidString(v) || strings.ContainsRune(v, utf8.RuneError) {
			return false
		}
	}
	return true
}

// Classify returns the work items one issue's block asks for, and a reason for
// each line (or batched SDK-bump item) it skipped because its text cannot go
// into a ledger key. It sets no state or history; Ingest does.
func Classify(b findingsblock.Block, issueURL string) (items []*ledger.WorkItem, skipped []string) {
	svc := b.Service
	newItem := func(key, bucket string) *ledger.WorkItem {
		return &ledger.WorkItem{
			Schema: ledger.Schema, Key: key, KeyHash: ledger.KeyHash(key), Generation: 1,
			Source: source, Service: svc, Repo: "aws-controllers-k8s/" + svc + "-controller",
			GapClass: bucket,
			Evidence: ledger.Evidence{SDKFrom: b.From, SDKTo: b.To},
			Links:    ledger.Links{ViewIssue: issueURL},
		}
	}
	skip := func(what string) {
		skipped = append(skipped, what+": text is not valid UTF-8")
	}

	var needsConfig []*ledger.WorkItem
	var regen []string
	for _, l := range b.Lines {
		if !validText(l.Class, l.Kind, l.Subject, l.Work) {
			skip(fmt.Sprintf("finding %q|%q|%q|%q", l.Class, l.Kind, l.Subject, l.Bucket))
			continue
		}
		var w *ledger.WorkItem
		switch l.Bucket {
		case findingsblock.BucketRegen:
			regen = append(regen, l.Kind+"."+l.Subject)
			continue
		case findingsblock.BucketNeedsConfig:
			w = newItem(fmt.Sprintf("api-change|%s|%s|%s|needs-config", svc, l.Kind, subjectKey(l)), l.Bucket)
			w.Resource, w.FieldPath, w.DetectorWork = l.Kind, l.Subject, l.Work
			w.ChangeClass = "hand-written-code"
			if configOnlyWork[l.Work] {
				w.ChangeClass = "config-only"
			}
			w.AutonomyLevel, w.PriorityBand = "human-merge", 4
		case findingsblock.BucketNewResource:
			// The detector sets Kind and Subject to the resource name.
			w = newItem(fmt.Sprintf("api-change|%s|%s|new-resource", svc, l.Kind), l.Bucket)
			w.Resource = l.Kind
			w.ChangeClass, w.AutonomyLevel, w.PriorityBand = "new-resource", "draft", 6
		case findingsblock.BucketManualReview:
			w = newItem(fmt.Sprintf("api-change|%s|%s|%s|manual-review", svc, l.Kind, subjectKey(l)), l.Bucket)
			w.Resource, w.FieldPath, w.DetectorWork = l.Kind, l.Subject, l.Class
			w.ChangeClass, w.AutonomyLevel, w.PriorityBand = "unclassified", "observe", 6
		default:
			// Parse rejects unknown buckets.
			continue
		}
		// The header (service) is part of every key.
		if !validText(w.Key) {
			skip(fmt.Sprintf("item %q", w.Key))
			continue
		}
		if l.Bucket == findingsblock.BucketNeedsConfig {
			needsConfig = append(needsConfig, w)
		}
		items = append(items, w)
	}
	if len(regen) > 0 {
		slices.Sort(regen)
		bump := newItem(fmt.Sprintf("sdk-bump|%s|*|%s", svc, b.To), findingsblock.BucketRegen)
		if !validText(bump.Key) {
			skip(fmt.Sprintf("item %q", bump.Key))
			return items, skipped
		}
		bump.ChangeClass, bump.AutonomyLevel, bump.PriorityBand = "SDK-bump", "human-merge", 3
		// Two classes of one path both needing only regen list it once.
		bump.Evidence.Findings = slices.Compact(regen)
		for _, w := range needsConfig {
			w.DependsOn = []string{bump.Key}
		}
		items = append(items, bump)
	}
	return items, skipped
}
