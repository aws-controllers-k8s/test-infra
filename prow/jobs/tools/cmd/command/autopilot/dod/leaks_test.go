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

var (
	fakeKeyID   = "AK" + "IA" + "Z7QWERTYUIOPASDF"
	fakeGHToken = "gh" + "p_" + strings.Repeat("a1B2", 9)
	fakePEM     = "-----BEGIN " + "RSA PRIVATE KEY-----"
	exampleKey  = "AKIA" + "IOSFODNN7EXAMPLE"
	realAccount = "4" + "71920385617" // made up; not an example ID
)

func addedFile(path string, generated bool, lines ...string) changedFile {
	f := changedFile{Path: path, Status: 'M', Generated: generated}
	for i, l := range lines {
		f.Added = append(f.Added, addedLine{N: i + 1, Text: l})
	}
	return f
}

func TestLeaksSecrets(t *testing.T) {
	c := checkLeaks([]changedFile{addedFile("test/e2e/x.py", false, "key = '"+fakeKeyID+"'", fakePEM)}, nil, nil)
	assert.Equal(t, []string{"AWS_ACCESS_KEY_ID", "PRIVATE_KEY"}, c.Reasons)
	for _, f := range c.Findings {
		assert.NotContains(t, f.Detail, fakeKeyID, "values are never written")
		assert.Regexp(t, `^match sha256:[0-9a-f]{12}$`, f.Detail)
	}
	// Secrets fail even in generated files.
	assert.Equal(t, StatusFail, checkLeaks([]changedFile{addedFile("apis/v1alpha1/types.go", true, fakeGHToken)}, nil, nil).Status)
	assert.Empty(t, checkLeaks([]changedFile{addedFile("apis/v1alpha1/types.go", true, "// e.g. "+exampleKey)}, nil, nil).Findings)
}

func TestLeaksAccountIDs(t *testing.T) {
	arn := "arn:aws:iam::" + realAccount + ":role/x"
	assert.Equal(t, []string{"AWS_ACCOUNT_ID"}, checkLeaks([]changedFile{addedFile("test/e2e/x.yaml", false, arn)}, nil, nil).Reasons)

	gen := checkLeaks([]changedFile{addedFile("config/crd/bases/x.yaml", true, arn)}, nil, nil)
	assert.Equal(t, StatusPass, gen.Status, "inventory only in generated files")
	assert.Len(t, gen.Findings, 1)

	assert.Empty(t, checkLeaks([]changedFile{addedFile("test/e2e/x.yaml", false, "arn:aws:iam::123456789012:role/x")}, nil, nil).Findings)

	cm := checkLeaks(nil, []commit{{SHA: strings.Repeat("c", 40), Message: "bump\n\naccount id: " + realAccount}}, nil)
	assert.Equal(t, []string{"AWS_ACCOUNT_ID"}, cm.Reasons)
	assert.Equal(t, "commit:cccccccccccc", cm.Findings[0].File)

	pr := checkLeaks(nil, nil, []byte("line one\n"+fakeGHToken+"\n"))
	assert.Equal(t, "pr-body", pr.Findings[0].File)
	assert.Equal(t, 2, pr.Findings[0].Line)
}

func TestLeaksClean(t *testing.T) {
	c := checkLeaks([]changedFile{addedFile("pkg/a.go", false, "x := 1")}, nil, []byte("Bump the SDK."))
	assert.Equal(t, StatusPass, c.Status)
	assert.Empty(t, c.Findings)
}
