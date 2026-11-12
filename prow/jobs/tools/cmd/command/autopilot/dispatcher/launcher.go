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
	// handles it once the claim's deadline and grace pass.
	PhaseUnknown Phase = "unknown"
	PhasePending Phase = "pending"
	PhaseRunning Phase = "running"
	// PhaseFinished: the run ended. Status.Result is what it reported, or nil
	// if it reported nothing.
	PhaseFinished Phase = "finished"
)

// RunResult is what a finished run reports (its agent-result.json). Every
// field is untrusted: reconcile validates it before anything reaches the
// ledger.
type RunResult struct {
	Outcome string
	Reason  string
	PR      string
}

type Status struct {
	Phase  Phase
	Result *RunResult
}

type LaunchRequest struct {
	LaunchID string
	Workflow string
	Args     map[string]string
	Timeout  time.Duration
}

// Launcher starts and inspects workflow runs.
type Launcher interface {
	// Launch starts a run. It must be idempotent on LaunchID: launching an id
	// that already exists succeeds and starts nothing, so a tick that
	// launched but lost its ledger push is safe to repeat.
	Launch(ctx context.Context, r LaunchRequest) error
	Status(ctx context.Context, launchID string) (Status, error)
	// Shadow reports that Launch only records requests. The Dispatcher then
	// changes no item for a launch.
	Shadow() bool
}

// ShadowLauncher records launch requests and starts nothing.
type ShadowLauncher struct {
	Requests []LaunchRequest
}

func (s *ShadowLauncher) Launch(_ context.Context, r LaunchRequest) error {
	s.Requests = append(s.Requests, r)
	return nil
}

func (s *ShadowLauncher) Status(context.Context, string) (Status, error) {
	return Status{Phase: PhaseUnknown}, nil
}

func (s *ShadowLauncher) Shadow() bool { return true }

// NewLauncher returns the launcher the policy names.
func NewLauncher(p policy.Policy) (Launcher, error) {
	switch p.Switches.Launcher {
	case policy.LauncherShadow:
		return &ShadowLauncher{}, nil
	}
	return nil, fmt.Errorf("launcher %q is not supported", p.Switches.Launcher)
}

// launchID names an item's run-th run. It is built from the key hash alone,
// so it is stable across ticks and fits a Kubernetes name (at most 63).
func launchID(w *ledger.WorkItem, run int) string {
	return fmt.Sprintf("autopilot-%s-r%d", strings.TrimPrefix(ledger.KeyHash(w.Key), "sha256:"), run)
}
