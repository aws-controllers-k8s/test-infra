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

package issuefinder

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

// TriageMarker starts every triage proposal comment the Dispatcher posts
// (package viewstatus), followed by the proposal's launch id. No comment
// that starts with it is read as a command.
const TriageMarker = "<!-- ack-autopilot-triage v1 "

// triageRE is one reply line: accept or reject a proposal by its launch id,
// a reject optionally naming the maintainer's own decision.
var triageRE = regexp.MustCompile(`(?m)^/autopilot triage (accept|reject) (autopilot-[0-9a-f]{16}-g[1-9][0-9]{0,5}-t[1-9][0-9]{0,5})(?: ([a-z-]{1,32}))?[ \t\r]*$`)

// replyDecisions are the decisions a maintainer can name. undecided is not
// one: it is a model's answer, never a person's.
var replyDecisions = map[string]bool{
	ledger.DecisionResource: true, ledger.DecisionFieldConfig: true,
	ledger.DecisionFieldHook: true, ledger.DecisionNotWorthModelling: true,
}

// TriageCommand is one maintainer reply to a triage proposal.
type TriageCommand struct {
	Issue     int
	IssueURL  string
	CommentID int64
	Author    string
	// Verb is "accept" or "reject".
	Verb     string
	LaunchID string
	// Decision is the maintainer's own decision on a reject, or "".
	Decision string
}

// TriageCommands returns the /autopilot triage replies team members left on
// trusted, open api-change issues, in issue-number order and then comment
// order (oldest first). The same rules as /autopilot wontfix apply: lines
// inside fenced code blocks, comments starting with the status or triage
// marker, and authors outside cfg.Team never count. A reply naming an
// unknown decision is reported in skipped. It reads nothing but issues.
func TriageCommands(cfg Config, issues []Issue) (cmds []TriageCommand, skipped []string) {
	for _, is := range byPrecedence(issues) {
		if !is.Open {
			continue
		}
		if _, why := trusted(cfg, is); why != "" {
			continue
		}
		for _, c := range is.Comments {
			if c.Author == "" || !cfg.Team[c.Author] ||
				strings.HasPrefix(c.Body, StatusMarker) || strings.HasPrefix(c.Body, TriageMarker) {
				continue
			}
			for _, m := range triageRE.FindAllStringSubmatch(outsideFences(c.Body), -1) {
				if m[3] != "" && !replyDecisions[m[3]] {
					skipped = append(skipped, fmt.Sprintf("#%d: /autopilot triage %s %s by %s: unknown decision %q", is.Number, m[1], m[2], c.Author, m[3]))
					continue
				}
				cmds = append(cmds, TriageCommand{
					Issue: is.Number, IssueURL: is.URL, CommentID: c.ID, Author: c.Author,
					Verb: m[1], LaunchID: m[2], Decision: m[3],
				})
			}
		}
	}
	return cmds, skipped
}
