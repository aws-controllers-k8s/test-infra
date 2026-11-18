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

package autopilot

import (
	"fmt"
	"net/http"
	"regexp"

	"github.com/google/go-github/v63/github"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dispatcher"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/prs"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/sdkbump"
)

// prWriteEnv must be "true" before the Dispatcher marks a gated draft ready
// for review (Plan 9 decision 4). The autopilot-dispatch periodic sets it to
// "false". prWriteTokenEnv is the token it then uses: a separate secret from
// GITHUB_TOKEN (the ledger and issue reads), so the ledger token can move to
// the ack-autopilot App without gaining controller-repo writes.
const (
	prWriteEnv      = "AUTOPILOT_PR_WRITE"
	prWriteTokenEnv = "AUTOPILOT_PR_GITHUB_TOKEN"
)

// prOptions are the dispatch flags for PR tracking (Plan 9).
type prOptions struct {
	// SDKBumpAuthor and AgentAuthor are the logins sdk-bump and the agent
	// workflows (add-field, add-resource) open PRs as. A PR by anyone else is
	// never gated or tracked.
	SDKBumpAuthor, AgentAuthor string
}

var prOpts prOptions

var loginRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)

func init() {
	f := dispatchCmd.Flags()
	f.StringVar(&prOpts.SDKBumpAuthor, "sdk-bump-author", "ack-bot", "login sdk-bump opens its PRs as")
	f.StringVar(&prOpts.AgentAuthor, "agent-author", "ack-agent", "login add-field and add-resource open their PRs as")
}

// prAuthors maps each lane's workflow to its PR author.
func (o prOptions) prAuthors() map[string]string {
	return map[string]string{"sdk-bump": o.SDKBumpAuthor, "add-field": o.AgentAuthor, "add-resource": o.AgentAuthor}
}

func (o prOptions) bots() []string { return []string{o.SDKBumpAuthor, o.AgentAuthor} }

func (o prOptions) validate() error {
	if !loginRE.MatchString(o.SDKBumpAuthor) || !loginRE.MatchString(o.AgentAuthor) {
		return fmt.Errorf("--sdk-bump-author %q and --agent-author %q must be GitHub logins", o.SDKBumpAuthor, o.AgentAuthor)
	}
	return nil
}

// prReader is the Dispatcher's PR client; replaced in tests.
var prReader = func(c *github.Client) dispatcher.PRClient { return &prs.Reader{Client: c} }

// prWriterFor is the PR writer the job's environment allows, or nil.
func prWriterFor(env func(string) string) (dispatcher.PRWriter, error) {
	if env(prWriteEnv) != "true" {
		return nil, nil
	}
	token := env(prWriteTokenEnv)
	if token == "" {
		return nil, fmt.Errorf("%s=true but %s is not provided", prWriteEnv, prWriteTokenEnv)
	}
	c := github.NewClient(&http.Client{Timeout: requestTimeout}).WithAuthToken(token)
	return &prs.Writer{Forge: &sdkbump.GitHubForge{Client: c}}, nil
}
