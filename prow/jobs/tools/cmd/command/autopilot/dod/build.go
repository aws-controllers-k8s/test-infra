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

// goTestVetAnalyzers is the "high-confidence subset" go test runs (go help
// test, Go 1.25 and 1.26). Full go vet trips on ACK generated code (unkeyed
// composite literals, unreachable code) that controllers only ever vet
// through go test, so the vet step uses exactly this set.
var goTestVetAnalyzers = []string{"atomic", "bool", "buildtags", "directive", "errorsas",
	"ifaceassert", "nilfunc", "printf", "stringintconv", "tests"}

func vetArgs() []string {
	args := []string{"vet"}
	for _, a := range goTestVetAnalyzers {
		args = append(args, "-"+a)
	}
	return append(args, "./...")
}

var buildSteps = []struct {
	sig  string
	args []string
}{
	{"BUILD_FAILED", []string{"build", "./..."}},
	{"VET_FAILED", vetArgs()},
	{"UNIT_TEST_FAILED", []string{"test", "-count=1", "./..."}},
	{"GO_MOD_NOT_TIDY", []string{"mod", "tidy", "-diff"}},
}

// checkBuild runs design §6's build/vet/unit gate and the go.mod tidiness
// check (sdk-bump runs "build → tidy → build") in the scratch clone. A
// compile or vet failure reclassifies anything below hand-written-code.
// Module mode is the go command's default, -mod=readonly.
//
// With sandbox set (Plan 9 review F1), every step runs as
// sandbox[0] sandbox[1:]... go <args>, after a probe (sandbox... true) that
// must succeed: a sandbox that cannot start fails closed with
// BUILD_SANDBOX_FAILED, and nothing runs unsandboxed.
func checkBuild(ctx context.Context, class string, scratch gitRepo, run Runner, log io.Writer, sandbox []string) Check {
	name, prefix := "go", []string(nil)
	if len(sandbox) > 0 {
		probe := append(append([]string(nil), sandbox[1:]...), "true")
		if _, stderr, err := run(ctx, scratch.dir, nil, sandbox[0], probe...); err != nil {
			logTail(log, "build sandbox probe", stderr)
			return newCheck(CheckBuild, []Finding{{Signature: "BUILD_SANDBOX_FAILED",
				Detail: "the build sandbox could not run a command", Fails: true}})
		}
		name = sandbox[0]
		prefix = append(append([]string(nil), sandbox[1:]...), "go")
	}
	var findings []Finding
	compileFailed := false
	for _, s := range buildSteps {
		_, stderr, err := run(ctx, scratch.dir, nil, name, append(append([]string(nil), prefix...), s.args...)...)
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
