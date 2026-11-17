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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod/dodtest"
)

const hdr = dodtest.GoHeader + "package x\n"

func mod(path string) changedFile {
	return withGen(changedFile{Path: path, Status: 'M', Base: []byte("a\n"), Head: []byte("b\n")})
}

func withGen(f changedFile) changedFile { f.Generated = generatedFile(f); return f }

func TestGeneratedFile(t *testing.T) {
	assert.True(t, withGen(changedFile{Path: "pkg/resource/b/sdk.go", Status: 'M', Base: []byte(hdr), Head: []byte(hdr)}).Generated)
	assert.True(t, withGen(changedFile{Path: "apis/v1alpha1/new.go", Status: 'A', Head: []byte(hdr)}).Generated)
	assert.True(t, mod("config/crd/bases/s3.services.k8s.aws_buckets.yaml").Generated)
	assert.True(t, mod("apis/v1alpha1/ack-generate-metadata.yaml").Generated)
	assert.True(t, mod("config/rbac/cluster-role-controller.yaml").Generated)
	// A header that appears only at the head of a hand-written file is forged.
	assert.False(t, withGen(changedFile{Path: "pkg/resource/b/hook.go", Status: 'M', Base: []byte("package x\n"), Head: []byte(hdr)}).Generated)
	// s3's sdk_file_end.go has no header: hand-written.
	assert.False(t, mod("pkg/resource/bucket/sdk_file_end.go").Generated)
	// A header outside apis/, cmd/ and pkg/ does not count.
	assert.False(t, withGen(changedFile{Path: "test/e2e/x.go", Status: 'A', Head: []byte(hdr)}).Generated)
}

func TestPathAllowlistSDKBump(t *testing.T) {
	gen := withGen(changedFile{Path: "pkg/resource/bucket/sdk.go", Status: 'M', Base: []byte(hdr), Head: []byte(hdr)})
	c := checkPathAllowlist(ClassSDKBump, []changedFile{gen, mod("go.mod"), mod("go.sum"), mod("ATTRIBUTION.md"), mod("helm/values.yaml")})
	assert.Equal(t, StatusPass, c.Status)

	c = checkPathAllowlist(ClassSDKBump, []changedFile{gen, mod("pkg/resource/bucket/sdk_file_end.go")})
	assert.Equal(t, StatusFail, c.Status)
	assert.Equal(t, []string{"PATH_NOT_ALLOWED"}, c.Reasons)
	assert.Equal(t, ClassHandWritten, c.Reclassify)

	c = checkPathAllowlist(ClassSDKBump, []changedFile{mod("generator.yaml")})
	assert.Equal(t, ClassConfigOnly, c.Reclassify)
}

func TestPathAllowlistSensitivePaths(t *testing.T) {
	for _, p := range []string{".github/workflows/x.yaml", "Makefile", "scripts/a.sh", "OWNERS", "OWNERS_ALIASES", "config/rbac/role.yaml", "test/e2e/requirements.txt"} {
		c := checkPathAllowlist(ClassHandWritten, []changedFile{mod(p)})
		assert.Equal(t, []string{"SENSITIVE_PATH"}, c.Reasons, p)
		assert.Equal(t, []string{LabelSensitivePath}, c.Labels, p)
	}
	// Generated RBAC is regeneration output, not a sensitive path.
	assert.Equal(t, StatusPass, checkPathAllowlist(ClassSDKBump, []changedFile{mod("config/rbac/cluster-role-controller.yaml")}).Status)
}

func TestPathAllowlistPerClass(t *testing.T) {
	assert.Equal(t, StatusPass, checkPathAllowlist(ClassConfigOnly, []changedFile{mod("generator.yaml"), mod("test/e2e/tests/test_bucket.py"), mod("config/iam/recommended-inline-policy")}).Status)
	assert.Equal(t, StatusFail, checkPathAllowlist(ClassConfigOnly, []changedFile{mod("pkg/resource/bucket/hook.go")}).Status)
	assert.Equal(t, StatusPass, checkPathAllowlist(ClassHandWritten, []changedFile{mod("pkg/resource/bucket/hook.go"), mod("templates/hooks/bucket/x.go.tpl")}).Status)

	c := checkPathAllowlist(ClassMechanical, []changedFile{mod("go.mod"), mod("apis/v1alpha1/ack-generate-metadata.yaml")})
	assert.Equal(t, StatusFail, c.Status)
	assert.Equal(t, ClassSDKBump, c.Reclassify)
	assert.Equal(t, StatusPass, checkPathAllowlist(ClassMechanical, []changedFile{mod("go.mod"), mod("go.sum"), mod("Dockerfile")}).Status)

	// A path no class allows still reclassifies to at least hand-written-code.
	c = checkPathAllowlist(ClassSDKBump, []changedFile{mod("README.md")})
	assert.Equal(t, []string{"PATH_NOT_ALLOWED"}, c.Reasons)
	assert.Equal(t, ClassHandWritten, c.Reclassify)

	c = checkPathAllowlist(ClassSDKBump, nil)
	assert.Equal(t, []string{"EMPTY_DIFF"}, c.Reasons)
}
