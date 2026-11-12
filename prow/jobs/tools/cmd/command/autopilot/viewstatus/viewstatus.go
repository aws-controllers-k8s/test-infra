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

// Package viewstatus renders the Dispatcher's status comment on an api-change
// issue and plans the one edit that keeps it current. The detector stays the
// only writer of the issue body (design 4.5).
package viewstatus

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/google/go-github/v63/github"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/issuefinder"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

// Marker starts every status comment. Only a comment by the posting account
// that starts with it is treated as ours.
const Marker = "<!-- ack-autopilot-status v1 -->"

// maxBody stays under GitHub's 65536-character comment limit.
const maxBody = 60000

const (
	Create    = "create"
	Update    = "update"
	Unchanged = "unchanged"
)

type Action struct {
	Issue     int
	Kind      string
	CommentID int64
	Body      string
}

// Render lists items (those whose view issue this is), sorted by key. It uses
// only fields the ledger builds by code (keys, states, classes, checked PR
// links, verdict tokens). Every line starts with "- ", so the issue finder
// can never read one as an /autopilot command.
func Render(items []*ledger.WorkItem) string {
	sorted := slices.Clone(items)
	slices.SortFunc(sorted, func(a, b *ledger.WorkItem) int { return strings.Compare(a.Key, b.Key) })
	var b strings.Builder
	b.WriteString(Marker + "\n")
	b.WriteString("**ACK Autopilot status.** Machine-written, updated by each Dispatcher run. One line per work item from this issue.\n\n")
	const tail = "\n…and %d more items in the ledger.\n"
	for i, w := range sorted {
		line := fmt.Sprintf("- `%s` **%s** %s: `%s`", w.KeyHash, w.State, w.ChangeClass, code(w.Key))
		if w.Links.PR != "" {
			line += " " + w.Links.PR
		}
		if v := w.Verdict; v != nil {
			line += fmt.Sprintf(" (`%s` `%s`)", code(v.Outcome), code(v.Reason))
		}
		line += "\n"
		if b.Len()+len(line)+len(tail)+10 > maxBody {
			fmt.Fprintf(&b, tail, len(sorted)-i)
			break
		}
		b.WriteString(line)
	}
	return b.String()
}

// code makes s safe inside a single-backtick code span.
func code(s string) string { return strings.ReplaceAll(s, "`", "'") }

// Plan decides the edit for one issue. Ours is a comment by login whose body
// starts with Marker; anyone else's comment, even one copying the marker, is
// never touched or trusted. With several of ours, the first listed (GitHub
// lists oldest first) is updated.
func Plan(is issuefinder.Issue, login, body string) Action {
	a := Action{Issue: is.Number, Body: body, Kind: Create}
	for _, c := range is.Comments {
		if c.Author != login || !strings.HasPrefix(c.Body, Marker) {
			continue
		}
		a.CommentID = c.ID
		a.Kind = Update
		if normalize(c.Body) == normalize(body) {
			a.Kind = Unchanged
		}
		return a
	}
	return a
}

func normalize(s string) string { return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n")) }

// Apply performs a planned edit. Unchanged does nothing.
func Apply(ctx context.Context, c *github.Client, owner, repo string, a Action) error {
	var err error
	switch a.Kind {
	case Create:
		_, _, err = c.Issues.CreateComment(ctx, owner, repo, a.Issue, &github.IssueComment{Body: github.String(a.Body)})
	case Update:
		_, _, err = c.Issues.EditComment(ctx, owner, repo, a.CommentID, &github.IssueComment{Body: github.String(a.Body)})
	}
	if err != nil {
		return fmt.Errorf("%s status comment on #%d: %w", a.Kind, a.Issue, err)
	}
	return nil
}
