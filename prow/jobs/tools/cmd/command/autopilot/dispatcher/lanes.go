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

import "github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"

// lane is how one change class is worked in Phase 1.
type lane struct {
	Workflow string
	// LLM counts the run against max_concurrent_llm_runs.
	LLM bool
	// MaxRunning caps this lane's runs in flight, whatever the policy says:
	// the ledger's budgets can lower it, never raise it. 0 means no lane cap.
	MaxRunning int
	args       func(w *ledger.WorkItem) map[string]string
}

// detectorWorkArg lists the detector work names add-field accepts as
// --detector-work (issuefinder configOnlyWork; workflows/validation.py
// DETECTOR_WORKS). Other values are not passed.
var detectorWorkArg = map[string]bool{"create-only": true, "not-generated": true, "server-default": true}

// lanes maps a change class to the workflow that does it (design 3, 6). A
// triaged item of a class not listed has no workflow in Phase 1 and is held
// as "awaiting-workflow" in TRIAGED: hand-written-code waits for fix-bug
// (Phase 2).
var lanes = map[string]lane{
	// The sdk-bump workflow (ack-build-tools autopilot sdk-bump, Plan 6).
	// With the shadow launcher these args only appear in launch lines.
	// change-class travels with work-item, as for add-field (Plan 3).
	"SDK-bump": {Workflow: "sdk-bump", args: func(w *ledger.WorkItem) map[string]string {
		return map[string]string{
			"service": w.Service, "sdk-to": w.Evidence.SDKTo,
			"work-item": w.KeyHash, "change-class": w.ChangeClass,
		}
	}},
	// add-field's required_args, plus the provenance and detector-work
	// optional_args in prow/agent-workflows/templates/add-field.tpl. At most
	// two run at once (owner decision 2026-11-16).
	"config-only": {Workflow: "add-field", LLM: true, MaxRunning: 2, args: func(w *ledger.WorkItem) map[string]string {
		args := map[string]string{
			"service": w.Service, "resource": w.Resource, "field": w.FieldPath,
			"work-item": w.KeyHash, "change-class": w.ChangeClass,
		}
		// Plan 4 carry-over: a server-default item's field is already in the
		// CRD after its bump; the agent's job is the decision about it.
		if detectorWorkArg[w.DetectorWork] {
			args["detector-work"] = w.DetectorWork
		}
		return args
	}},
	// The add-resource workflow plans, reviews its plan and implements in one
	// run (owner decision 2026-11-17, option A: no plan gate). It is off
	// unless policy/new-resource.yaml enables it, at most 1 runs at once, and
	// its PR stays draft for the API-surface review (draft level).
	"new-resource": {Workflow: "add-resource", LLM: true, MaxRunning: 1, args: func(w *ledger.WorkItem) map[string]string {
		return map[string]string{
			"service": w.Service, "resource": w.Resource,
			"work-item": w.KeyHash, "change-class": w.ChangeClass,
		}
	}},
}

// skipsTriage holds the change classes that go from DETECTED straight to
// TRIAGED. Every other class waits in DETECTED for triage (design 5.1):
// needs-config items for the mode-api triage lane (Plan 8, triageHold), other
// classes as awaiting-triage.
// new-resource items from the detector need no triage either: add-resource
// plans and reviews its own plan (option A).
var skipsTriage = map[string]bool{"SDK-bump": true, "new-resource": true}
