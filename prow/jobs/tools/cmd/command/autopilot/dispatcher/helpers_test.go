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

package dispatcher

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/policy"
)

const t0 = "2026-11-12T08:00:00Z"

var now = time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)

// item builds a work item for the service named in key's second segment.
func item(key, class string, state ledger.State) *ledger.WorkItem {
	svc := strings.Split(key, "|")[1]
	return &ledger.WorkItem{
		Schema: ledger.Schema, Key: key, KeyHash: ledger.KeyHash(key), Generation: 1,
		Source: "api-change", Service: svc, Repo: "aws-controllers-k8s/" + svc + "-controller",
		ChangeClass: class, AutonomyLevel: "human-merge", PriorityBand: 3, State: state,
		Evidence: ledger.Evidence{SDKTo: "service/" + svc + "/v1.2.0"},
		History:  []ledger.Transition{{TS: t0, To: state, By: "test"}},
	}
}

// running makes w an active run launched at ts with the given attempts.
func running(w *ledger.WorkItem, ts string, runs, max int) *ledger.WorkItem {
	id := launchID(w, runs)
	w.State = ledger.Running
	w.Attempts = &ledger.Attempts{Runs: runs, MaxRuns: max}
	w.Claim = &ledger.Claim{LaunchID: id, Workflow: lanes[w.ChangeClass].Workflow, TS: ts,
		Deadline: mustTime(ts).Add(5 * time.Hour).Format(time.RFC3339)}
	w.Links.ProwJobs = append(w.Links.ProwJobs, id)
	w.History = append(w.History, ledger.Transition{TS: ts, To: ledger.Running, By: actor, Reason: id})
	return w
}

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func ledgerOf(ws ...*ledger.WorkItem) map[string]*ledger.WorkItem {
	out := map[string]*ledger.WorkItem{}
	for _, w := range ws {
		out[w.Key] = w
	}
	return out
}

func testPolicy(t *testing.T) policy.Policy {
	t.Helper()
	p, err := policy.Load("../policy/testdata")
	require.NoError(t, err)
	return p
}

// fakeLauncher is a non-shadow launcher with canned statuses.
type fakeLauncher struct {
	statuses  map[string]Status
	statusErr map[string]error
	launchErr error
	launched  []LaunchRequest
}

func (f *fakeLauncher) Launch(_ context.Context, r LaunchRequest) error {
	if f.launchErr != nil {
		return f.launchErr
	}
	f.launched = append(f.launched, r)
	return nil
}

func (f *fakeLauncher) Status(_ context.Context, id string) (Status, error) {
	if err := f.statusErr[id]; err != nil {
		return Status{}, err
	}
	if s, ok := f.statuses[id]; ok {
		return s, nil
	}
	return Status{Phase: PhaseUnknown}, nil
}

func (f *fakeLauncher) Shadow() bool { return false }
