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
	LLM  bool
	args func(w *ledger.WorkItem) map[string]string
}

// lanes maps a change class to the workflow that does it (design 3, 6). A
// triaged item of a class not listed has no workflow in Phase 1 and is
// escalated: hand-written-code waits for fix-bug (Phase 2), new-resource for
// the plan gate.
var lanes = map[string]lane{
	// The sdk-bump workflow is not built yet; with the shadow launcher this
	// name only appears in launch lines.
	"SDK-bump": {Workflow: "sdk-bump", args: func(w *ledger.WorkItem) map[string]string {
		return map[string]string{"service": w.Service, "sdk-to": w.Evidence.SDKTo, "work-item": w.KeyHash}
	}},
	// add-field's required_args in prow/agent-workflows/templates/add-field.tpl.
	"config-only": {Workflow: "add-field", LLM: true, args: func(w *ledger.WorkItem) map[string]string {
		return map[string]string{"service": w.Service, "resource": w.Resource, "field": w.FieldPath}
	}},
}

// skipsTriage holds the change classes that go from DETECTED straight to
// TRIAGED. Every other class waits in DETECTED for the triage workflow (design
// 5.1), which is not built yet.
var skipsTriage = map[string]bool{"SDK-bump": true}
