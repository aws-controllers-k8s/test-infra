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

// Package dispatcher is the ACK Autopilot Dispatcher: one deterministic tick
// that reconciles runs, routes work items through the state machine (design
// 5.4), applies caps (design 8) and launches work through a Launcher. It is
// the only writer of the ledger besides the issue finder it runs first.
package dispatcher

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/policy"
)

// actor is the Transition.By of every history entry the Dispatcher writes.
const actor = "dispatcher"

type Phase string

const (
	// PhaseUnknown: no such run, or the launcher cannot tell. The reaper
	// handles it once the claim's deadline and grace pass. Switching to the
	// shadow launcher while real runs are in flight makes those runs report
	// unknown, so they are reaped after their deadline plus grace.
	PhaseUnknown Phase = "unknown"
	PhasePending Phase = "pending"
	PhaseRunning Phase = "running"
	// PhaseFinished: the run ended. Status.Result is what it reported, or nil
	// if it reported nothing.
	PhaseFinished Phase = "finished"
)

// RunResult is what a finished run reports (its agent-result.json, read by
// ParseResult). Every field is untrusted: reconcile validates it before
// anything reaches the ledger.
type RunResult struct {
	Outcome string
	Reason  string
	PR      string
	// Workflow and ProwJobID say which run wrote the file. A real launcher
	// checks them against the job it read the file from.
	Workflow  string
	ProwJobID string
	// Tokens and E2ERuns are what the run spent; reconcile adds them to the
	// item's usage.
	Tokens  int64
	E2ERuns int
}

// JobState is how a finished run's job ended, as the launcher saw it.
type JobState string

const (
	JobSucceeded JobState = "succeeded"
	JobFailed    JobState = "failed"
	// JobAborted: someone or something stopped the job (Deck, a newer run).
	JobAborted JobState = "aborted"
	// JobErrored: Prow could not run the job (pod never started, bad spec).
	JobErrored JobState = "errored"
)

type Status struct {
	Phase  Phase
	Result *RunResult
	// Job is how a finished run's job ended. A real launcher sets it for every
	// finished run, and reconcile checks Result against it: the agents share
	// the pod and can write the result file, but not the job's exit code.
	// Empty skips the check (test fakes).
	Job JobState
	// Problem is why a finished run has no Result, as a SCREAMING_SNAKE
	// token (RESULT_MISSING, RESULT_INVALID, ...). Empty means
	// INFRA_NO_VERDICT.
	Problem string
}

type LaunchRequest struct {
	LaunchID string
	Workflow string
	Args     map[string]string
	Timeout  time.Duration
}

// Launcher starts and inspects workflow runs.
type Launcher interface {
	// Launch starts a run. It must be idempotent on LaunchID: the same
	// LaunchID with an identical request succeeds and starts nothing, so a
	// tick that launched but lost its ledger push is safe to repeat. That holds
	// only if the rerun picks the same items; the real launcher must adopt or
	// reap orphaned runs by id prefix. The same
	// LaunchID with a different Workflow or Args is an error. A real launcher
	// must remember the ids it has started even after Prow deletes the job,
	// so that a relaunch never starts a second run.
	Launch(ctx context.Context, r LaunchRequest) error
	Status(ctx context.Context, launchID string) (Status, error)
	// Abort stops a run. It must be idempotent: aborting a run that is
	// finished, already aborted or unknown succeeds and changes nothing.
	Abort(ctx context.Context, launchID string) error
	// Shadow reports that Launch only records requests. The Dispatcher then
	// changes no item for a launch.
	Shadow() bool
}

// ActiveJob is a run a launcher sees as not finished.
type ActiveJob struct {
	LaunchID, Workflow, Service string
	// Refresh marks an sdk-bump --mode refresh run, which no item claims.
	Refresh bool
}

// Inventory is implemented by launchers that can list their own unfinished
// runs. Tick counts the runs no item claims (a launch whose ledger push was
// lost) against the caps, so a lost push never lets more runs start than the
// caps allow.
type Inventory interface {
	Active(ctx context.Context) ([]ActiveJob, error)
}

// ShadowLauncher records launch and abort requests and starts nothing.
type ShadowLauncher struct {
	Requests []LaunchRequest
	Aborts   []string
}

func (s *ShadowLauncher) Launch(_ context.Context, r LaunchRequest) error {
	s.Requests = append(s.Requests, r)
	return nil
}

// Status always reports unknown. Switching to shadow while real runs are in
// flight means those runs report unknown and are reaped after their deadline
// plus grace.
func (s *ShadowLauncher) Status(context.Context, string) (Status, error) {
	return Status{Phase: PhaseUnknown}, nil
}

// Abort records the request and stops nothing.
func (s *ShadowLauncher) Abort(_ context.Context, launchID string) error {
	s.Aborts = append(s.Aborts, launchID)
	return nil
}

func (s *ShadowLauncher) Shadow() bool { return true }

// dryRun reads run status through a real launcher but launches and aborts
// nothing: a dry-run tick can never start or stop a run.
type dryRun struct {
	real   Launcher
	shadow ShadowLauncher
}

func (d *dryRun) Launch(ctx context.Context, r LaunchRequest) error { return d.shadow.Launch(ctx, r) }
func (d *dryRun) Status(ctx context.Context, id string) (Status, error) {
	return d.real.Status(ctx, id)
}
func (d *dryRun) Abort(ctx context.Context, id string) error { return d.shadow.Abort(ctx, id) }
func (d *dryRun) Shadow() bool                               { return true }

// NewLauncher returns the launcher the policy names.
func NewLauncher(p policy.Policy) (Launcher, error) {
	switch p.Switches.Launcher {
	case policy.LauncherShadow:
		return &ShadowLauncher{}, nil
	case policy.LauncherProw:
		return nil, fmt.Errorf("launcher %q is built by the dispatch command (it needs the cluster and the test-infra checkout)", p.Switches.Launcher)
	}
	return nil, fmt.Errorf("launcher %q is not supported", p.Switches.Launcher)
}

// launchID names an item's run-th run in its current generation. It is built
// from the key hash and generation, so it is stable across ticks, fits a
// Kubernetes name (at most 63), and a reopened item (generation+1, fresh
// attempts) never collides with an old run.
func launchID(w *ledger.WorkItem, run int) string {
	return fmt.Sprintf("autopilot-%s-g%d-r%d", strings.TrimPrefix(ledger.KeyHash(w.Key), "sha256:"), w.Generation, run)
}
