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
		// And the un-suffixed spelling, which is what a lookup derived from
		// the AWS member name `Type` will ask for. Without this the suppression
		// filter misses and we report a field the CRD already exposes.
		"rules.type",
	} {
		assert.True(t, widget[want], "expected field path %q", want)
	}

	assert.False(t, widget["nosuchfield"])
	assert.NotContains(t, got, "Gadget")
}

func TestReadCRDFieldsNoVersions(t *testing.T) {
	// Guards the len(doc.Spec.Versions) == 0 check. Without it, indexing
	// Versions[0] panics, and this code parses YAML from repos we do not
	// control. A committed test is what stops a future edit reintroducing that.
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
	// A malformed CRD must fail the controller, not be skipped. A partial field
	// set is a suppression filter with holes in it: the missing kind would be
	// announced as a new resource by producer 1 and skipped entirely by
	// producers 3 and 4, both silently. Failing here turns that into a visible
	// per-service error instead.
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

	// Sorts after a_good.yaml, so a good CRD is already in the accumulator when
	// the bad one is hit — proving the failure is not merely "nothing parsed".
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
