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
	"fmt"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod/dodtest"
)

const (
	metaPath = "apis/v1alpha1/ack-generate-metadata.yaml"
	botEmail = "ack-bot@users.noreply.github.com"
)

const metadataYAML = "api_versions:\n- api_version: v1alpha1\n  status: available\n"

// genMeta is ack-generate-metadata.yaml; svcPin "" leaves the per-service pin out.
func genMeta(buildHash, svcPin, buildDate string) string {
	s := fmt.Sprintf("ack_generate_info:\n  build_date: %q\n  build_hash: %s\n  go_version: go1.26.3\n  version: v0.64.0\n"+
		"api_directory_checksum: abc\napi_version: v1alpha1\naws_sdk_go_version: v1.41.5\n", buildDate, buildHash)
	if svcPin != "" {
		s += "aws_service_sdk_version: " + svcPin + "\n"
	}
	return s
}

func goMod(s3 string) string {
	return "module github.com/aws-controllers-k8s/s3-controller\n\ngo 1.22\n\nrequire (\n" +
		"\tgithub.com/aws-controllers-k8s/runtime v0.68.0\n" +
		"\tgithub.com/aws/aws-sdk-go-v2 v1.41.5\n" +
		"\tgithub.com/aws/aws-sdk-go-v2/service/s3 " + s3 + "\n" +
		"\tgithub.com/aws/smithy-go v1.24.2\n)\n"
}

type repos struct {
	ctrl, codegen, runtime *dodtest.Repo
	cgSHA                  string
	remote                 string // file:// bare clone of ctrl
}

func newRepos(t *testing.T) *repos {
	t.Helper()
	r := &repos{codegen: dodtest.NewRepo(t), runtime: dodtest.NewRepo(t), ctrl: dodtest.NewRepo(t)}
	r.codegen.Write("cmd/ack-generate/command/root.go", "package command\n\nconst flag = \"aws-service-sdk-version\"\n")
	r.cgSHA = r.codegen.Commit("code-generator v0.64.0")
	r.runtime.Write("config/crd/common/a.yaml", "kind: x\n")
	r.runtime.Commit("runtime")
	r.runtime.Git("tag", "v0.68.0")
	r.ctrl.Write("metadata.yaml", metadataYAML)
	r.ctrl.Write(metaPath, genMeta(r.cgSHA, "", "2026-11-01T00:00:00Z"))
	r.ctrl.Write("go.mod", goMod("v1.90.0"))
	r.ctrl.Write("generator.yaml", "resources:\n  Bucket:\n    fields: {}\n")
	r.ctrl.Write("pkg/resource/bucket/sdk.go", dodtest.GoHeader+"package bucket\n")
	r.ctrl.Commit("base")
	bare := t.TempDir()
	out, err := exec.Command("git", "init", "--quiet", "--bare", "--initial-branch=main", bare).CombinedOutput()
	require.NoError(t, err, string(out))
	r.ctrl.Git("push", "--quiet", "file://"+bare, "main")
	r.remote = "file://" + bare
	return r
}
