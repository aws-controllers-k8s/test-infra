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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

const proposalID = "autopilot-0123456789abcdef-g1-t1"

func TestTriageCommands(t *testing.T) {
	is := issueWith(t, 27, true, s3Header, modeField)
	is.Comments = []Comment{
		{ID: 1, Author: "maintainer", Body: "/autopilot triage accept " + proposalID},
		{ID: 2, Author: "stranger", Body: "/autopilot triage accept " + proposalID},
		{ID: 3, Author: "maintainer", Body: "looks wrong\n/autopilot triage reject " + proposalID + " field-hook\n"},
		{ID: 4, Author: "maintainer", Body: "```\n/autopilot triage accept " + proposalID + "\n```"},
		{ID: 5, Author: "maintainer", Body: TriageMarker + proposalID + " -->\n/autopilot triage accept " + proposalID},
		{ID: 6, Author: "maintainer", Body: StatusMarker + "\n/autopilot triage accept " + proposalID},
		{ID: 7, Author: "maintainer", Body: "/autopilot triage reject " + proposalID + " merge-it"},
		{ID: 8, Author: "maintainer", Body: "/autopilot triage accept autopilot-XYZ-g1-t1"},
		{ID: 9, Author: "", Body: "/autopilot triage accept " + proposalID},
		{ID: 10, Author: "maintainer", Body: "please /autopilot triage accept " + proposalID},
	}
	untrusted := issueWith(t, 28, true, s3Header, modeField)
	untrusted.Author = "someone"
	untrusted.Comments = []Comment{{ID: 11, Author: "maintainer", Body: "/autopilot triage accept " + proposalID}}

	cmds, skipped := TriageCommands(cfg, []Issue{untrusted, is})
	assert.Equal(t, []TriageCommand{
		{Issue: 27, IssueURL: is.URL, CommentID: 1, Author: "maintainer", Verb: "accept", LaunchID: proposalID},
		{Issue: 27, IssueURL: is.URL, CommentID: 3, Author: "maintainer", Verb: "reject", LaunchID: proposalID, Decision: ledger.DecisionFieldHook},
	}, cmds)
	assert.Equal(t, []string{`#27: /autopilot triage reject ` + proposalID + ` by maintainer: unknown decision "merge-it"`}, skipped)
}

func TestIngestIgnoresTriageComments(t *testing.T) {
	items := map[string]*ledger.WorkItem{}
	is := issueWith(t, 27, true, s3Header, modeField)
	Ingest(cfg, []Issue{is}, items)
	is.Comments = []Comment{{ID: 1, Author: "maintainer",
		Body: TriageMarker + proposalID + " -->\n/autopilot wontfix " + items[modeKey].KeyHash}}
	Ingest(cfg, []Issue{is}, items)
	assert.Equal(t, ledger.Detected, items[modeKey].State, "a triage comment is never a command")
}
