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
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// ResultSchema is the schema of agent-result.json, written by
// prow/agent-workflows/agents/workflows/result.py. Both sides test against
// testdata/agent-result-v1.json.
const ResultSchema = "ack.autopilot.result/v1"

// maxResultBytes bounds what is read from a run's artifacts.
const maxResultBytes = 64 << 10

type resultFile struct {
	Schema    string `json:"schema"`
	Workflow  string `json:"workflow"`
	WorkItem  string `json:"work_item"`
	ProwJobID string `json:"prow_job_id"`
	Outcome   string `json:"outcome"`
	Reason    string `json:"reason"`
	PR        string `json:"pr"`
	Tokens    struct {
		Input  int64 `json:"input"`
		Output int64 `json:"output"`
		Total  int64 `json:"total"`
	} `json:"tokens"`
	Iterations struct {
		Impl   int `json:"impl"`
		Replan int `json:"replan"`
	} `json:"iterations"`
	E2E struct {
		Status   string `json:"status"`
		Attempts int    `json:"attempts"`
	} `json:"e2e"`
}

// ParseResult decodes a run's agent-result.json. workItem, when not empty,
// must equal the file's work_item, so a launcher can never apply another
// item's result. It checks only structure; reconcile still checks the
// outcome, reason and PR link. Unknown fields are ignored, so a newer writer
// with extra fields still parses.
func ParseResult(data []byte, workItem string) (RunResult, error) {
	if len(data) > maxResultBytes {
		return RunResult{}, fmt.Errorf("agent-result.json is larger than %d bytes", maxResultBytes)
	}
	var f resultFile
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&f); err != nil {
		return RunResult{}, fmt.Errorf("decode agent-result.json: %w", err)
	}
	if dec.More() {
		return RunResult{}, errors.New("decode agent-result.json: data after the object")
	}
	if f.Schema != ResultSchema {
		return RunResult{}, fmt.Errorf("agent-result.json schema %q, want %q", f.Schema, ResultSchema)
	}
	if workItem != "" && f.WorkItem != workItem {
		return RunResult{}, fmt.Errorf("agent-result.json is for work item %q, want %q", f.WorkItem, workItem)
	}
	if f.Tokens.Input < 0 || f.Tokens.Output < 0 || f.Tokens.Total < 0 || f.E2E.Attempts < 0 {
		return RunResult{}, errors.New("agent-result.json has a negative count")
	}
	return RunResult{Outcome: f.Outcome, Reason: f.Reason, PR: f.PR, Tokens: f.Tokens.Total, E2ERuns: f.E2E.Attempts}, nil
}
