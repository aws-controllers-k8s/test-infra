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
	"testing"

	"github.com/stretchr/testify/assert"
)

const wi = "sha256:0123456789abcdef"

func botCommit(trailers string) commit {
	return commit{SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", AuthorEmail: "Bot@Example.com", Message: "bump\n\n" + trailers}
}

func TestProvenance(t *testing.T) {
	good := botCommit("Producer: autopilot/sdk-bump\nWork-Item: " + wi + "\nChange-Class: SDK-bump")
	human := commit{SHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", AuthorEmail: "someone@example.com", Message: "fix typo"}
	bots := []string{"bot@example.com"}

	c, humans := checkProvenance([]commit{good, human}, bots, wi, ClassSDKBump)
	assert.Equal(t, StatusPass, c.Status)
	assert.Equal(t, 1, humans)
	assert.Equal(t, "HUMAN_COMMIT", c.Findings[0].Signature)

	for name, tc := range map[string]struct {
		c    commit
		want string
	}{
		"no trailers":  {botCommit("Signed-off-by: x"), "BOT_COMMIT_WITHOUT_TRAILERS"},
		"other item":   {botCommit("Producer: autopilot/sdk-bump\nWork-Item: sha256:ffffffffffffffff\nChange-Class: SDK-bump"), "BOT_COMMIT_WRONG_WORK_ITEM"},
		"other class":  {botCommit("Producer: autopilot/sdk-bump\nWork-Item: " + wi + "\nChange-Class: config-only"), "BOT_COMMIT_WRONG_CLASS"},
		"no work item": {botCommit("Producer: autopilot/sdk-bump\nChange-Class: SDK-bump"), "BOT_COMMIT_WITHOUT_WORK_ITEM"},
		"not last":     {commit{SHA: good.SHA, AuthorEmail: "bot@example.com", Message: "bump\n\nProducer: autopilot/sdk-bump\n\nsome prose"}, "BOT_COMMIT_WITHOUT_TRAILERS"},
	} {
		c, _ := checkProvenance([]commit{tc.c}, bots, wi, ClassSDKBump)
		assert.Equal(t, []string{tc.want}, c.Reasons, name)
	}

	c, _ = checkProvenance([]commit{human}, bots, wi, ClassSDKBump)
	assert.Equal(t, []string{"NO_BOT_COMMIT"}, c.Reasons)

	c, _ = checkProvenance([]commit{good}, nil, wi, ClassSDKBump)
	assert.Equal(t, StatusSkip, c.Status)
	assert.Equal(t, []string{"NO_BOT_EMAILS"}, c.Reasons)
}

// A bot commit must name its work item even when the run was not given one.
func TestProvenanceWithoutRunWorkItem(t *testing.T) {
	bots := []string{"bot@example.com"}
	c, _ := checkProvenance([]commit{botCommit("Producer: autopilot/sdk-bump\nChange-Class: SDK-bump")}, bots, "", ClassSDKBump)
	assert.Equal(t, []string{"BOT_COMMIT_WITHOUT_WORK_ITEM"}, c.Reasons)
	c, _ = checkProvenance([]commit{botCommit("Producer: autopilot/sdk-bump\nWork-Item: " + wi + "\nChange-Class: SDK-bump")}, bots, "", ClassSDKBump)
	assert.Equal(t, StatusPass, c.Status)
}

func TestAutopilotHead(t *testing.T) {
	bots := []string{"bot@example.com"}
	assert.True(t, autopilotHead(botCommit("Signed-off-by: x"), bots), "bot author")
	assert.True(t, autopilotHead(commit{AuthorEmail: "someone@example.com", Message: "x\n\nProducer: autopilot/sdk-bump"}, nil), "autopilot trailer")
	assert.False(t, autopilotHead(commit{AuthorEmail: "someone@example.com", Message: "fix typo"}, bots))
}
