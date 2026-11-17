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

package dod

import (
	"slices"
	"strings"
)

func firstValue(vs []string) string {
	if len(vs) == 0 {
		return ""
	}
	return vs[0]
}

func isBot(c commit, botEmails []string) bool {
	return slices.ContainsFunc(botEmails, func(e string) bool { return strings.EqualFold(e, c.AuthorEmail) })
}

// autopilotHead reports whether the PR head claims to be autopilot's: its
// author is a bot email, or it carries a Producer: autopilot/* trailer. Such a
// head makes provenance binding even without --work-item, so a bot head that
// lacks its Work-Item trailer fails instead of going unchecked.
func autopilotHead(head commit, botEmails []string) bool {
	return isBot(head, botEmails) || strings.HasPrefix(firstValue(parseTrailers(head.Message)["Producer"]), "autopilot/")
}

// checkProvenance applies design §4.4: a commit by a bot email must carry the
// Plan 3 trailers for this work item and class (fail closed); any other commit
// is a human commit, allowed and counted. It returns the human commit count.
func checkProvenance(commits []commit, botEmails []string, workItem, class string) (Check, int) {
	if len(botEmails) == 0 {
		return skipCheck(CheckProvenance, "NO_BOT_EMAILS"), 0
	}
	var findings []Finding
	humans, bots := 0, 0
	for _, c := range commits {
		if c.Merge {
			continue
		}
		short := c.SHA
		if len(short) > 12 {
			short = short[:12]
		}
		if !isBot(c, botEmails) {
			humans++
			findings = append(findings, Finding{Signature: "HUMAN_COMMIT", Detail: "commit " + short})
			continue
		}
		bots++
		t := parseTrailers(c.Message)
		sig := ""
		switch {
		case !strings.HasPrefix(firstValue(t["Producer"]), "autopilot/"):
			sig = "BOT_COMMIT_WITHOUT_TRAILERS"
		case firstValue(t["Work-Item"]) == "":
			sig = "BOT_COMMIT_WITHOUT_WORK_ITEM"
		case workItem != "" && firstValue(t["Work-Item"]) != workItem:
			sig = "BOT_COMMIT_WRONG_WORK_ITEM"
		case firstValue(t["Change-Class"]) != class:
			sig = "BOT_COMMIT_WRONG_CLASS"
		}
		if sig != "" {
			findings = append(findings, Finding{Signature: sig, Detail: "commit " + short, Fails: true})
		}
	}
	if bots == 0 {
		findings = append(findings, Finding{Signature: "NO_BOT_COMMIT", Fails: true})
	}
	return newCheck(CheckProvenance, findings), humans
}
