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
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod/dodtest"
)

// goModule builds a scratch clone of a stdlib-only module, so the real go
// command runs offline.
func goModule(t *testing.T, files map[string]string) gitRepo {
	t.Helper()
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOTOOLCHAIN", "local")
	r := dodtest.NewRepo(t)
	r.Write("go.mod", "module example.com/c\n\ngo 1.22\n")
	for p, c := range files {
		r.Write(p, c)
	}
	head := r.Commit("head")
	s, cleanup, err := cloneScratch(context.Background(), gitRepo{dir: r.Dir}, head)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	return s
}

func TestBuild(t *testing.T) {
	ctx := context.Background()
	ok := goModule(t, map[string]string{"main.go": "package main\n\nfunc main() {}\n"})
	assert.Equal(t, StatusPass, checkBuild(ctx, ClassSDKBump, ok, ExecRunner, &bytes.Buffer{}).Status)

	broken := goModule(t, map[string]string{"main.go": "package main\n\nfunc main() { undefined() }\n"})
	c := checkBuild(ctx, ClassSDKBump, broken, ExecRunner, &bytes.Buffer{})
	assert.Equal(t, []string{"BUILD_FAILED"}, c.Reasons)
	assert.Equal(t, ClassHandWritten, c.Reclassify, "design §5.1: a compile error reclassifies an SDK bump")
	assert.Empty(t, checkBuild(ctx, ClassHandWritten, broken, ExecRunner, &bytes.Buffer{}).Reclassify)

	red := goModule(t, map[string]string{
		"main.go":      "package main\n\nfunc main() {}\n",
		"main_test.go": "package main\n\nimport \"testing\"\n\nfunc TestRed(t *testing.T) { t.Fatal(\"red\") }\n",
	})
	assert.Equal(t, []string{"UNIT_TEST_FAILED"}, checkBuild(ctx, ClassSDKBump, red, ExecRunner, &bytes.Buffer{}).Reasons)
}

// TestBuildVetsWithGoTestSet: the vet step uses exactly go test's analyzer
// subset. ACK generated code trips composites and unreachable, which
// controllers never run; printf is in the subset and still fails.
func TestBuildVetsWithGoTestSet(t *testing.T) {
	ctx := context.Background()
	generated := goModule(t, map[string]string{
		"main.go": "package main\n\nimport \"image\"\n\nvar P = image.Point{1, 2}\n\nfunc f() int {\n\treturn 1\n\tpanic(\"x\")\n}\n\nfunc main() { _ = f() }\n",
	})
	assert.Equal(t, StatusPass, checkBuild(ctx, ClassSDKBump, generated, ExecRunner, &bytes.Buffer{}).Status,
		"composites and unreachable are not in go test's vet set")

	printf := goModule(t, map[string]string{
		"main.go": "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Printf(\"%d\\n\", \"x\") }\n",
	})
	c := checkBuild(ctx, ClassSDKBump, printf, ExecRunner, &bytes.Buffer{})
	assert.Contains(t, c.Reasons, "VET_FAILED")
}

func TestVetAnalyzersMatchGoTest(t *testing.T) {
	assert.Equal(t, []string{"atomic", "bool", "buildtags", "directive", "errorsas",
		"ifaceassert", "nilfunc", "printf", "stringintconv", "tests"}, goTestVetAnalyzers)
}
