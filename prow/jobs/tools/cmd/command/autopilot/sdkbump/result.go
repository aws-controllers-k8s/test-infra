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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dispatcher"
)

// Outcomes this workflow reports: a subset of result.py's OUTCOMES. A re-run
// that updates its PR reports pr_opened, because reconcile treats pr_updated
// as an unknown verdict.
const (
	OutcomePROpened   = "pr_opened"
	OutcomeNoChange   = "completed_no_change"
	OutcomeNeedsHuman = "needs_human"
	OutcomeFailed     = "failed"
)

// Result is what the run reports in agent-result.json.
type Result struct {
	Outcome, Reason, PR string
}

var (
	reasonRE    = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	prURLRE     = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9-]+/[A-Za-z0-9._-]+/pull/[1-9][0-9]*$`)
	prowJobIDRE = regexp.MustCompile(`^[A-Za-z0-9._-]{0,128}$`)
)

// ValidProwJobID reports whether s fits the result file's prow_job_id.
func ValidProwJobID(s string) bool { return prowJobIDRE.MatchString(s) }

type resultTokens struct {
	Input  int64 `json:"input"`
	Output int64 `json:"output"`
	Total  int64 `json:"total"`
}

type resultIterations struct {
	Impl   int `json:"impl"`
	Replan int `json:"replan"`
}

type resultE2E struct {
	Attempts int    `json:"attempts"`
	Status   string `json:"status"`
}

// resultDoc is ack.autopilot.result/v1 with every key in sorted order, so the
// bytes equal what result.py writes (json.dump sort_keys=True, indent=2).
type resultDoc struct {
	E2E        resultE2E        `json:"e2e"`
	Iterations resultIterations `json:"iterations"`
	Outcome    string           `json:"outcome"`
	PR         string           `json:"pr"`
	ProwJobID  string           `json:"prow_job_id"`
	Reason     string           `json:"reason"`
	Schema     string           `json:"schema"`
	Tokens     resultTokens     `json:"tokens"`
	WorkItem   string           `json:"work_item"`
	Workflow   string           `json:"workflow"`
}

// marshalResult checks r with result.py's rules and returns the file bytes.
// An LLM-free run spends no tokens and runs no e2e.
func marshalResult(workItem, prowJobID string, r Result) ([]byte, error) {
	switch r.Outcome {
	case OutcomePROpened, OutcomeNoChange, OutcomeNeedsHuman, OutcomeFailed:
	default:
		return nil, fmt.Errorf("outcome %q is not one sdk-bump reports", r.Outcome)
	}
	switch {
	case r.Reason != "" && !reasonRE.MatchString(r.Reason):
		return nil, fmt.Errorf("reason %q is not SCREAMING_SNAKE (at most 64)", r.Reason)
	case r.PR != "" && !prURLRE.MatchString(r.PR):
		return nil, fmt.Errorf("pr %q is not a GitHub pull request URL", r.PR)
	case r.Outcome == OutcomePROpened && r.PR == "":
		return nil, fmt.Errorf("outcome %s needs a pr", r.Outcome)
	case workItem != "" && !ValidWorkItem(workItem):
		return nil, fmt.Errorf("work item %q is not sha256:<16 hex>", workItem)
	case !ValidProwJobID(prowJobID):
		return nil, fmt.Errorf("prow job id %q is not at most 128 of [A-Za-z0-9._-]", prowJobID)
	}
	doc := resultDoc{
		E2E: resultE2E{Status: "NOT_RUN"}, Outcome: r.Outcome, PR: r.PR, ProwJobID: prowJobID,
		Reason: r.Reason, Schema: dispatcher.ResultSchema, WorkItem: workItem, Workflow: Workflow,
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	b = append(b, '\n')
	// The Dispatcher's own reader must accept what we write.
	if _, err := dispatcher.ParseResult(b, workItem); err != nil {
		return nil, err
	}
	return b, nil
}

// WriteResult replaces path atomically with mode 0644 (the Prow upload
// sidecar may run as another uid), as result.py does.
func WriteResult(path, workItem, prowJobID string, r Result) error {
	b, err := marshalResult(workItem, prowJobID, r)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".agent-result.")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // a no-op after the rename
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
