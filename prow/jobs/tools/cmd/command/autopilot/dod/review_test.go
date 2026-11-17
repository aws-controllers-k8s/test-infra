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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod/dodtest"
)

// helmFixture is an SDK bump whose base has a generated helm template. attrs
// adds a .gitattributes that hides diffs and widens the role; extra adds a
// helm template that regeneration does not produce.
func helmFixture(t *testing.T, attrs, extra bool) fixture {
	t.Helper()
	f := sdkBump(t)
	r := dodtest.NewRepo(t)
	r.Write("go.mod", "module example.com/widgets-controller\n\ngo 1.22\n")
	r.Write("generator.yaml", "ignore:\n  field_paths: []\n")
	r.Write(metadataPath, metaYAML("v0.64.0", "2026-11-01"))
	r.Write("pkg/resource/widget/sdk.go", genSDK)
	r.Write("config/crd/bases/widgets.yaml", crdFixture)
	r.Write("helm/templates/role.yaml", "kind: ClusterRole\nrules: []\n")
	base := r.Commit("base")
	r.Write(metadataPath, f.regen[metadataPath])
	r.Write("pkg/resource/widget/sdk.go", genSDK+"\nvar NewField = 1\n")
	if attrs {
		r.Write("helm/templates/.gitattributes", "* -diff\n")
		r.Write("helm/templates/role.yaml", "kind: ClusterRole\nrules:\n- apiGroups: ['*']\n  resources: ['*']\n  verbs: ['*']\n")
	}
	if extra {
		r.Write("helm/templates/evil-crb.yaml", "kind: ClusterRoleBinding\nroleRef: {name: cluster-admin}\n")
	}
	head := r.CommitAs(dodtest.Bot, "Bump the SDK to v1.48.0"+bumpTrailers)
	regen := map[string]string{}
	for k, v := range f.regen {
		regen[k] = v
	}
	regen["helm/templates/role.yaml"] = "kind: ClusterRole\nrules: []\n"
	return fixture{repo: r, base: base, head: head, codegen: f.codegen, regen: regen}
}

func runFixture(t *testing.T, f fixture) *Verdict {
	t.Helper()
	o := options(t, f)
	v, err := Run(context.Background(), o)
	require.NoError(t, err)
	return v
}

func checkOf(v *Verdict, id string) Check {
	for _, c := range v.Checks {
		if c.ID == id {
			return c
		}
	}
	return Check{}
}

func findingFiles(c Check, sig string) []string {
	var out []string
	for _, f := range c.Findings {
		if f.Signature == sig {
			out = append(out, f.File)
		}
	}
	return out
}

// Review 1: a file under a regen output that regeneration does not produce
// (an extra helm template) must fail regen-clean.
func TestReviewUnreproducedRegenOutput(t *testing.T) {
	v := runFixture(t, helmFixture(t, false, true))
	assert.Equal(t, StatusFail, v.Verdict)
	assert.Contains(t, findingFiles(checkOf(v, CheckRegenClean), "GENERATED_FILE_NOT_REPRODUCED"), "helm/templates/evil-crb.yaml")

	assert.Equal(t, StatusPass, runFixture(t, helmFixture(t, false, false)).Verdict, "the plain bump still passes")
}

// Review 2: "* -diff" in a .gitattributes turns every diff into "Binary files
// differ"; dod must still see the lines, and the attributes file itself is a
// sensitive path.
func TestReviewGitattributesDoNotBlindTheChecks(t *testing.T) {
	v := runFixture(t, helmFixture(t, true, false))
	assert.Equal(t, StatusFail, v.Verdict)
	assert.Contains(t, findingFiles(checkOf(v, CheckRegenClean), "REGEN_DIFF"), "helm/templates/role.yaml")
	assert.Contains(t, findingFiles(checkOf(v, CheckPathAllowlist), "SENSITIVE_PATH"), "helm/templates/.gitattributes")

	r := dodtest.NewRepo(t)
	r.Write("README.md", "x\n")
	base := r.Commit("base")
	r.Write(".gitattributes", "* -diff\n")
	r.Write("test/e2e/conf.py", "KEY = 'AKIA"+"Z7QWERTYUIOPASDF'\n")
	head := r.Commit("head")
	files, err := gather(context.Background(), gitRepo{dir: r.Dir}, base, head)
	require.NoError(t, err)
	assert.Equal(t, StatusFail, checkLeaks(files, nil, nil).Status)
}

func TestReviewGitConfigFilesAreSensitive(t *testing.T) {
	for _, p := range []string{".gitattributes", ".gitignore", "helm/.gitattributes", "pkg/x/.gitignore"} {
		assert.True(t, sensitivePath(p), p)
	}
	assert.False(t, sensitivePath("pkg/x/gitignore.go"))
}

// Review 3: git quotes a path with '"' or '\' in "+++ b/...", and its added
// lines must still be attributed to it.
func TestReviewQuotedPathLinesAreScanned(t *testing.T) {
	r := dodtest.NewRepo(t)
	r.Write("README.md", "x\n")
	base := r.Commit("base")
	r.Write("test/e2e/a\"b.py", "KEY = 'AKIA"+"Z7QWERTYUIOPASDF'\nurl='http://169.254.169.254/'\n")
	r.Write("test/e2e/tab\there.py", "x = 1\n")
	head := r.Commit("head")
	files, err := gather(context.Background(), gitRepo{dir: r.Dir}, base, head)
	require.NoError(t, err)
	for _, f := range files {
		assert.NotEmpty(t, f.Added, f.Path)
	}
	assert.Contains(t, checkLeaks(files, nil, nil).Reasons, "AWS_ACCESS_KEY_ID")
	assert.Contains(t, checkCodeScan(ClassHandWritten, files).Reasons, "IMDS_ADDRESS")
}

// A changed text file whose new lines the diff parser lost fails closed.
func TestReviewUnparsedDiffFails(t *testing.T) {
	f := changedFile{Path: "pkg/x.go", Status: 'A', Head: []byte("package x\n")}
	markUnparsed(&f)
	require.True(t, f.Unparsed)
	assert.Contains(t, checkLeaks([]changedFile{f}, nil, nil).Reasons, "DIFF_UNPARSED")
	assert.Contains(t, checkCodeScan(ClassHandWritten, []changedFile{f}).Reasons, "DIFF_UNPARSED")

	del := changedFile{Path: "pkg/x.go", Status: 'M', Base: []byte("a\nb\n"), Head: []byte("a\n")}
	markUnparsed(&del)
	assert.False(t, del.Unparsed, "a pure deletion adds no lines")
	same := changedFile{Path: "run.sh", Status: 'M', Base: []byte("a\n"), Head: []byte("a\n")}
	markUnparsed(&same)
	assert.False(t, same.Unparsed, "a mode change adds no lines")
}
