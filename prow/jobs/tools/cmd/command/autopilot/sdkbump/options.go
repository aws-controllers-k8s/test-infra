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

package sdkbump

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod"
)

// DefaultBotLogin is the GitHub login that authors every bump PR. dod gets it
// as the PR author, so only it (not a spoofable commit email) makes the PR an
// autopilot PR.
const DefaultBotLogin = "ack-bot"

const (
	ModeBump    = "bump"    // regenerate, gate, publish
	ModeRefresh = "refresh" // re-evaluate the merge block of the open PR only
)

// Options configure one run.
type Options struct {
	Inputs
	Mode string
	Org  string
	// ControllerDir is a clean clone of <org>/<svc>-controller with the base
	// (main) checked out. The run commits and checks out in it.
	ControllerDir string
	// CodegenRepo and RuntimeRepo are full clones; the run checks the
	// controller's pins out of them in temporary worktrees.
	CodegenRepo, RuntimeRepo string
	// ACKGenerate has crd-compat-check --output json (Plan 4 Part A).
	ACKGenerate string
	CacheDir    string
	// LedgerDir is ack-private's autopilot/ directory, read only. Without it
	// the server-default dependents are unknown and the merge stays blocked.
	LedgerDir                   string
	BotName, BotEmail, BotLogin string
	// Remote overrides the push remote; only a file:// path is accepted
	// besides the org repository.
	Remote          string
	Write           bool
	ArtifactsDir    string
	JobName, JobURL string
	Forge           Forge
	Run             dod.Runner
	Gate            func(context.Context, dod.Options) (*dod.Verdict, error)
	Log             io.Writer
}

// Report is what a run did; Result is what it records in agent-result.json.
type Report struct {
	Result
	Title, Body, Head string
	Pushed            bool
	Verdict           *dod.Verdict
	VerdictBytes      []byte
	// Planned lists pushes a run without --write did not make.
	Planned []string
}

func stop(outcome, reason, pr string) Report {
	return Report{Result: Result{Outcome: outcome, Reason: reason, PR: pr}}
}

func (r Report) with(outcome, reason, pr string) Report {
	r.Result = Result{Outcome: outcome, Reason: reason, PR: pr}
	return r
}

var orgRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`)

// check fills defaults and refuses every unsafe combination before the run
// touches anything.
func (o *Options) check() error {
	if o.Log == nil {
		o.Log = io.Discard
	}
	if o.Mode == "" {
		o.Mode = ModeBump
	}
	if o.BotLogin == "" {
		o.BotLogin = DefaultBotLogin
	}
	switch {
	case o.Mode != ModeBump && o.Mode != ModeRefresh:
		return fmt.Errorf("mode %q is not %s or %s", o.Mode, ModeBump, ModeRefresh)
	case !orgRE.MatchString(o.Org):
		return fmt.Errorf("org %q is not a GitHub organization", o.Org)
	case o.ControllerDir == "":
		return fmt.Errorf("the controller checkout must be set")
	case o.Mode == ModeBump && (o.CodegenRepo == "" || o.RuntimeRepo == ""):
		return fmt.Errorf("a bump needs the code-generator and runtime clones")
	case o.BotName == "" || !strings.Contains(o.BotEmail, "@"):
		return fmt.Errorf("the bot name and email must be set")
	case o.Forge == nil || o.Run == nil || o.Gate == nil:
		return fmt.Errorf("forge, runner and gate must be set")
	case o.Write && o.remote() != canonicalRemote(o.Org, o.Service):
		return fmt.Errorf("--write pushes only to %s", canonicalRemote(o.Org, o.Service))
	case o.Write && o.WorkItem == "":
		return fmt.Errorf("--write needs a work item: every published bump is a ledger item")
	case !o.Write && o.Forge.Writes():
		return fmt.Errorf("a forge that writes to GitHub needs --write")
	case !isLocalRemote(o.remote()) && o.remote() != canonicalRemote(o.Org, o.Service):
		return fmt.Errorf("--remote must be the org repository or a file:// path")
	}
	abs, err := filepath.Abs(o.ControllerDir)
	if err != nil {
		return err
	}
	o.ControllerDir = abs
	return nil
}

func (o Options) repo() string { return o.Org + "/" + o.Service + "-controller" }

func (o Options) remote() string {
	if o.Remote != "" {
		return o.Remote
	}
	return canonicalRemote(o.Org, o.Service)
}

// pushes reports whether the run may push: with --write (to the org repo) or
// to a local file:// remote.
func (o Options) pushes() bool { return o.Write || isLocalRemote(o.remote()) }

func (o Options) statusURL() string {
	if strings.HasPrefix(o.JobURL, "https://") {
		return o.JobURL
	}
	return ""
}

func (o Options) logf(format string, a ...any) { fmt.Fprintf(o.Log, "sdk-bump: "+format+"\n", a...) }
