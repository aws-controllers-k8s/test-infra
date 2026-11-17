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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func edit(path, base, head string) changedFile {
	f := changedFile{Path: path, Status: 'M', Base: []byte(base), Head: []byte(head)}
	if base == "" {
		f.Status, f.Base = 'A', nil
	}
	// Added lines: every head line not in base (enough for these tests).
	have := map[string]bool{}
	for _, l := range strings.Split(base, "\n") {
		have[l] = true
	}
	for i, l := range strings.Split(head, "\n") {
		if !have[l] {
			f.Added = append(f.Added, addedLine{N: i + 1, Text: l})
		}
	}
	return withGen(f)
}

func scanReasons(class string, files ...changedFile) []string {
	return checkCodeScan(class, files).Reasons
}

func TestCodeScanImports(t *testing.T) {
	base := "package bucket\n\nimport \"fmt\"\n"
	assert.Equal(t, []string{"NEW_RISKY_IMPORT"}, scanReasons(ClassHandWritten, edit("pkg/resource/bucket/hook.go", base, "package bucket\n\nimport (\n\t\"fmt\"\n\t\"os/exec\"\n)\n")))
	had := "package bucket\n\nimport \"net/http\"\n"
	assert.Empty(t, scanReasons(ClassHandWritten, edit("pkg/resource/bucket/hook.go", had, had+"\nvar X = 1\n")))
	assert.Equal(t, []string{"GO_PARSE_FAILED"}, scanReasons(ClassHandWritten, edit("pkg/resource/bucket/hook.go", base, "package {")))
}

func TestCodeScanLiterals(t *testing.T) {
	base := "package bucket\n"
	assert.Equal(t, []string{"NEW_URL_LITERAL"}, scanReasons(ClassHandWritten, edit("pkg/resource/bucket/hook.go", base, base+"var u = \"https://evil.example\"\n")))
	assert.Equal(t, []string{"NEW_IP_LITERAL"}, scanReasons(ClassHandWritten, edit("pkg/resource/bucket/hook.go", base, base+"var u = \"10.1.2.3\"\n")))
	assert.Empty(t, scanReasons(ClassHandWritten, edit("pkg/resource/bucket/hook.go", base, base+"// see https://docs.aws.amazon.com\nvar x = 1\n")), "comments are not literals")
	gen := dodtestHeader + "package bucket\n"
	assert.Empty(t, scanReasons(ClassSDKBump, edit("pkg/resource/bucket/sdk.go", gen, gen+"var u = \"https://s3.amazonaws.com\"\n")), "generated files are exempt")
	assert.Equal(t, []string{"IMDS_ADDRESS"}, scanReasons(ClassConfigOnly, edit("test/e2e/resources/x.yaml", "a: 1\n", "a: 1\nurl: http://169.254.169.254/latest\n")))
	blob := strings.Repeat("QUJD", 40)
	assert.Equal(t, []string{"BASE64_BLOB"}, scanReasons(ClassConfigOnly, edit("test/e2e/resources/x.yaml", "a: 1\n", "a: 1\nb: "+blob+"\n")))
}

func TestCodeScanGoModAndSum(t *testing.T) {
	mod := "module m\n\ngo 1.24\n\nrequire github.com/aws/aws-sdk-go-v2 v1.41.5\n"
	evil := mod + "require github.com/evil/x v1.0.0\n"
	assert.Equal(t, []string{"NEW_GO_MODULE"}, scanReasons(ClassSDKBump, edit("go.mod", mod, evil)))
	c := checkCodeScan(ClassHandWritten, []changedFile{edit("go.mod", mod, evil)})
	assert.Equal(t, StatusPass, c.Status, "a finding, not a failure, above SDK-bump")
	assert.Equal(t, "NEW_GO_MODULE", c.Findings[0].Signature)
	assert.Empty(t, scanReasons(ClassSDKBump, edit("go.mod", mod, mod+"require github.com/aws/aws-sdk-go-v2/service/s3 v1.97.3\n")))

	rep := checkCodeScan(ClassHandWritten, []changedFile{edit("go.mod", mod, mod+"replace github.com/aws/aws-sdk-go-v2 => ../fork\n")})
	assert.Equal(t, []string{"GOMOD_REPLACE_ADDED"}, rep.Reasons)
	assert.Equal(t, []string{LabelSensitivePath}, rep.Labels)

	sum := "github.com/a/b v1.0.0 h1:AAA=\ngithub.com/a/b v1.0.0/go.mod h1:BBB=\n"
	assert.Equal(t, []string{"GOSUM_HASH_CHANGED"}, scanReasons(ClassMechanical, edit("go.sum", sum, strings.Replace(sum, "AAA", "CCC", 1))))
	assert.Empty(t, scanReasons(ClassMechanical, edit("go.sum", sum, sum+"github.com/a/b v1.1.0 h1:DDD=\n")))
}

func TestCodeScanIAM(t *testing.T) {
	base := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:*"],"Resource":"*"}]}`
	assert.Empty(t, scanReasons(ClassConfigOnly, edit("config/iam/recommended-inline-policy", base, base+"\n")))
	wide := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:*"],"Resource":"*"},{"Effect":"Allow","Action":"*","Resource":"*"}]}`
	assert.Equal(t, []string{"IAM_WIDENED"}, scanReasons(ClassConfigOnly, edit("config/iam/recommended-inline-policy", base, wide)))
	not := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","NotAction":"iam:*","Resource":"*"}]}`
	assert.Contains(t, scanReasons(ClassConfigOnly, edit("config/iam/recommended-inline-policy", base, not)), "IAM_NOT_ACTION")
	assert.Equal(t, []string{"IAM_POLICY_UNPARSEABLE"}, scanReasons(ClassConfigOnly, edit("config/iam/recommended-inline-policy", base, "{")))
	assert.Equal(t, []string{"IAM_ADMIN_POLICY"}, scanReasons(ClassConfigOnly, edit("config/iam/recommended-policy-arn", "arn:aws:iam::aws:policy/AmazonS3FullAccess\n", "arn:aws:iam::aws:policy/AdministratorAccess\n")))
}
