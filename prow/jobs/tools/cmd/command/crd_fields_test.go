package command

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadCRDFields(t *testing.T) {
	got, err := readCRDFields(fakeControllerPath)
	require.NoError(t, err)

	require.Contains(t, got, "Widget")
	widget := got["Widget"]

	// Spec and status fields are merged into one set, lowercased, with array
	// item properties flattened at the array's own path.
	for _, want := range []string{
		"name",
		"config",
		"config.size",
		"rules",
		"rules.prefix",
		"widgetid",
		// The literal CRD spelling, codegen's Go-keyword escape.
		"rules.type_",
		// And the un-suffixed spelling, which lookups from the AWS member name `Type` use.
		"rules.type",
	} {
		assert.True(t, widget[want], "expected field path %q", want)
	}

	assert.False(t, widget["nosuchfield"])
	assert.NotContains(t, got, "Gadget")
}

func TestReadCRDFieldsNoVersions(t *testing.T) {
	// A CRD with no versions must not panic on Versions[0].
	root := t.TempDir()
	dir := filepath.Join(root, "config", "crd", "bases")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "empty.yaml"), []byte(
		`apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.demo.services.k8s.aws
spec:
  names:
    kind: Widget
  versions: []
`), 0o644))

	got, err := readCRDFields(root)
	require.NoError(t, err, "an empty versions list must not error")
	assert.NotContains(t, got, "Widget", "a kind with no schema contributes nothing")
}

func TestReadCRDFieldsFailsOnMalformedFile(t *testing.T) {
	// A malformed CRD fails the controller instead of being skipped: a partial field
	// set would silently misreport resources and fields.
	root := t.TempDir()
	dir := filepath.Join(root, "config", "crd", "bases")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(dir, "a_good.yaml"), []byte(
		`apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.demo.services.k8s.aws
spec:
  names:
    kind: Widget
  versions:
  - name: v1alpha1
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec:
            type: object
            properties:
              name:
                type: string
`), 0o644))

	// Sorts after a_good.yaml, so the failure is not just "nothing parsed".
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b_bad.yaml"),
		[]byte("\t- this: is not: valid yaml\n"), 0o644))

	got, err := readCRDFields(root)
	require.Error(t, err, "a malformed CRD must fail the controller")
	assert.Contains(t, err.Error(), "b_bad.yaml", "the error must name the file")
	assert.Nil(t, got, "no partial field set may escape")
}

func TestReadCRDFieldsNoDir(t *testing.T) {
	got, err := readCRDFields("../../../testdata/does-not-exist")
	require.NoError(t, err)
	assert.Empty(t, got)
}
