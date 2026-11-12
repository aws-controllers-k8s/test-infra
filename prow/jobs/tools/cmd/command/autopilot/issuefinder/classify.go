// Copyright Amazon.com Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//     http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

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
// hand-written code. "from-sibling" is not listed: the detector sets it only
// for a field sent beside one that custom code reconciles, so adding it means
// updating that hook.
var configOnlyWork = map[string]bool{
	"create-only":   true, // is_immutable
	"not-generated": true, // a from: field codegen does not add itself
}

// classToken shortens a detector FindingClass name for use in a key.
var classToken = map[string]string{
	"spec-field":         "spec",
	"status-field":       "status",
	"lifecycle-field":    "lifecycle",
	"transient-resource": "transient",
	"possible-resource":  "possible",
}

// unknownClassPrefix marks the class segment of a class not in classToken, so
// an unknown class literally named "spec" ("class=spec") cannot collide with
// the known spec-field ("spec").
const unknownClassPrefix = "class="

// subjectKey is the Subject segment of a needs-config or manual-review key:
// "<class token>:<subject>", where the token is the short name from classToken
// or "class=<full name>" for a class not listed.
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
		tok = unknownClassPrefix + l.Class
	}
	return tok + ":" + l.Subject
}

// validText reports whether s can go into a key or the ledger. ledger.Lint
// rejects keys that are not valid UTF-8 or hold U+FFFD (what encoding/json
// writes for bad bytes), and one bad item would block every push.
func validText(s ...string) bool {
	for _, v := range s {
		if !utf8.ValidString(v) || strings.ContainsRune(v, utf8.RuneError) {
			return false
		}
	}
	return true
}

// validSegment reports whether s can be one segment of a key: valid text with
// no "|", the key separator.
func validSegment(s ...string) bool {
	for _, v := range s {
		if !validText(v) || strings.Contains(v, "|") {
			return false
		}
	}
	return true
}

const (
	reasonText = "text is not valid UTF-8"
	reasonSep  = `key segment contains "|"`
)

// Classify returns the work items one issue's block asks for, and a reason for
// each line (or item) it skipped because its key cannot be built.
//
// Callers MUST treat a non-empty skipped like Header.Omitted > 0: the issue's
// desired set is incomplete, so they must not supersede ledger items for that
// issue that are missing from items.
//
// Regen lines are batched into one "sdk-bump|<svc>|*|<to>" item. needs-config
// lines are new SDK members, so every one depends on that bump, and the bump is
// emitted whenever there are regen lines, needs-config lines, or Omitted > 0
// (a truncated block drops regen lines first). Without regen lines its
// Evidence.Findings is nil. If the bump key is invalid, the bump and every
// needs-config item are skipped, since they cannot be done without it.
//
// A line is skipped only when a field that goes into its key is bad: Class,
// Kind and Subject (Kind alone for new-resource), which must be valid UTF-8 and
// hold no "|". Work is not part of any key; a needs-config line whose Work is
// not valid text is classified hand-written-code with an empty DetectorWork.
// Lines with the same key collapse to one item; the line that sorts first wins,
// so the result does not depend on line order. Classify sets no state or
// history; Ingest does.
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
	skip := func(what, why string) {
		skipped = append(skipped, what+": "+why)
	}

	// Sort a copy so duplicate keys resolve the same way for any input order.
	lines := slices.Clone(b.Lines)
	slices.SortStableFunc(lines, func(x, y findingsblock.Line) int {
		return slices.Compare(
			[]string{x.Class, x.Kind, x.Subject, x.Bucket, x.Work},
			[]string{y.Class, y.Kind, y.Subject, y.Bucket, y.Work})
	})

	// The bump key comes from the header alone.
	bumpKey := fmt.Sprintf("sdk-bump|%s|*|%s", svc, b.To)
	bumpOK := validSegment(svc, b.To)

	seen := map[string]bool{}
	needsBump := b.Omitted > 0
	var regen []string
	for _, l := range lines {
		what := fmt.Sprintf("finding %q|%q|%q|%q", l.Class, l.Kind, l.Subject, l.Bucket)
		if !validText(l.Class, l.Kind, l.Subject) {
			skip(what, reasonText)
			continue
		}
		if l.Bucket == findingsblock.BucketRegen {
			// Kind.Subject goes into the bump's evidence, not into a key.
			regen = append(regen, l.Kind+"."+l.Subject)
			continue
		}
		var w *ledger.WorkItem
		switch l.Bucket {
		case findingsblock.BucketNeedsConfig:
			if !validSegment(l.Class, l.Kind, l.Subject) || strings.Contains(l.Class, ":") {
				skip(what, reasonSep)
				continue
			}
			needsBump = true
			if !bumpOK {
				skip(what, "SDK-bump key is invalid")
				continue
			}
			w = newItem(fmt.Sprintf("api-change|%s|%s|%s|needs-config", svc, l.Kind, subjectKey(l)), l.Bucket)
			w.Resource, w.FieldPath = l.Kind, l.Subject
			w.ChangeClass = "hand-written-code"
			if validText(l.Work) {
				w.DetectorWork = l.Work
				if configOnlyWork[l.Work] {
					w.ChangeClass = "config-only"
				}
			}
			w.AutonomyLevel, w.PriorityBand = "human-merge", 4
			w.DependsOn = []string{bumpKey}
		case findingsblock.BucketNewResource:
			// The detector sets Kind and Subject to the resource name.
			if !validSegment(l.Kind) {
				skip(what, reasonSep)
				continue
			}
			w = newItem(fmt.Sprintf("api-change|%s|%s|new-resource", svc, l.Kind), l.Bucket)
			w.Resource = l.Kind
			w.ChangeClass, w.AutonomyLevel, w.PriorityBand = "new-resource", "draft", 6
		case findingsblock.BucketManualReview:
			if !validSegment(l.Class, l.Kind, l.Subject) || strings.Contains(l.Class, ":") {
				skip(what, reasonSep)
				continue
			}
			w = newItem(fmt.Sprintf("api-change|%s|%s|%s|manual-review", svc, l.Kind, subjectKey(l)), l.Bucket)
			w.Resource, w.FieldPath, w.DetectorWork = l.Kind, l.Subject, l.Class
			w.ChangeClass, w.AutonomyLevel, w.PriorityBand = "unclassified", "observe", 6
		default:
			// Parse rejects unknown buckets.
			continue
		}
		// The header (service) is part of every key.
		if !validSegment(svc) {
			skip(fmt.Sprintf("item %q", w.Key), reasonText+` or contains "|"`)
			continue
		}
		if seen[w.Key] {
			continue
		}
		seen[w.Key] = true
		items = append(items, w)
	}

	if len(regen) == 0 && !needsBump {
		return items, skipped
	}
	if !bumpOK {
		skip(fmt.Sprintf("item %q", bumpKey), reasonText+` or contains "|"`)
		return items, skipped
	}
	bump := newItem(bumpKey, findingsblock.BucketRegen)
	bump.ChangeClass, bump.AutonomyLevel, bump.PriorityBand = "SDK-bump", "human-merge", 3
	if len(regen) > 0 {
		slices.Sort(regen)
		// Two classes of one path both needing only regen list it once.
		bump.Evidence.Findings = slices.Compact(regen)
	}
	return append(items, bump), skipped
}
