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
	"context"
	"io"
	"strings"
)

var buildSteps = []struct {
	sig  string
	args []string
}{
	{"BUILD_FAILED", []string{"build", "./..."}},
	{"VET_FAILED", []string{"vet", "./..."}},
	{"UNIT_TEST_FAILED", []string{"test", "-count=1", "./..."}},
	{"GO_MOD_NOT_TIDY", []string{"mod", "tidy", "-diff"}},
}

// checkBuild runs design §6's build/vet/unit gate and the go.mod tidiness
// check (sdk-bump runs "build → tidy → build") in the scratch clone. A
// compile or vet failure reclassifies anything below hand-written-code.
// Module mode is the go command's default, -mod=readonly.
func checkBuild(ctx context.Context, class string, scratch gitRepo, run Runner, log io.Writer) Check {
	var findings []Finding
	compileFailed := false
	for _, s := range buildSteps {
		_, stderr, err := run(ctx, scratch.dir, nil, "go", s.args...)
		if err == nil {
			continue
		}
		cmdline := "go " + strings.Join(s.args, " ")
		logTail(log, cmdline, stderr)
		findings = append(findings, Finding{Signature: s.sig, Detail: cmdline, Fails: true})
		if s.sig == "BUILD_FAILED" || s.sig == "VET_FAILED" {
			compileFailed = true
		}
		if s.sig == "BUILD_FAILED" {
			break
		}
	}
	c := newCheck(CheckBuild, findings)
	if compileFailed && classRank[class] < classRank[ClassHandWritten] {
		c.Reclassify = ClassHandWritten
	}
	return c
}
