package command

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadCRDFields(t *testing.T) {
	got, status, err := readCRDFields(fakeControllerPath)
	require.NoError(t, err)

	require.Contains(t, got, "Widget")
	widget := got["Widget"]

	// Spec fields, lowercased, with array item properties flattened at the
	// array's own path.
	for _, want := range []string{
		"name",
		"config",
		"config.size",
		"rules",
		"rules.prefix",
		// The literal CRD spelling, codegen's Go-keyword escape.
		"rules.type_",
		// And the un-suffixed spelling, which lookups from the AWS member name `Type` use.
		"rules.type",
	} {
		assert.True(t, widget[want], "expected field path %q", want)
	}

	assert.False(t, widget["nosuchfield"])
	assert.NotContains(t, got, "Gadget")

	// Status is kept apart: a status field is not a settable one.
	assert.False(t, widget["widgetid"])
	assert.Equal(t, map[string]bool{"widgetid": true}, status["Widget"])
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

	got, _, err := readCRDFields(root)
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

	got, status, err := readCRDFields(root)
	require.Error(t, err, "a malformed CRD must fail the controller")
	assert.Contains(t, err.Error(), "b_bad.yaml", "the error must name the file")
	assert.Nil(t, got, "no partial field set may escape")
	assert.Nil(t, status, "no partial field set may escape")
}

func TestReadCRDFieldsFlattensMapValues(t *testing.T) {
	// A structured map value is recorded at the map's own path, as WalkMembers
	// reports it; the boolean additionalProperties form has no fields.
	root := t.TempDir()
	dir := filepath.Join(root, "config", "crd", "bases")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "firewalls.yaml"), []byte(
		`apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: firewalls.networkfirewall.services.k8s.aws
spec:
  names:
    kind: Firewall
  versions:
  - name: v1alpha1
    schema:
      openAPIV3Schema:
        type: object
        properties:
          status:
            type: object
            properties:
              syncStates:
                type: object
                additionalProperties:
                  properties:
                    attachment:
                      properties:
                        subnetID:
                          type: string
                      type: object
                  type: object
              labels:
                type: object
                additionalProperties: true
`), 0o644))

	spec, got, err := readCRDFields(root)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{}, spec["Firewall"], "a status-only kind still has a spec entry")
	fw := got["Firewall"]
	for _, want := range []string{"syncstates", "syncstates.attachment", "syncstates.attachment.subnetid", "labels"} {
		assert.True(t, fw[want], "expected field path %q", want)
	}
	assert.False(t, fw["syncstates.natgatewayattachments"])
}

func TestReadCRDFieldsNoDir(t *testing.T) {
	got, status, err := readCRDFields("../../../testdata/does-not-exist")
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Empty(t, status)
}

// sectionModel has a Widget whose Create request and read response gain Mode, and
// whose read response alone gains Phase; UpdateWidgetMode is a new setter.
func sectionModel(t *testing.T, latest bool) *SmithyModel {
	t.Helper()
	input, output, ops := `"Name": {"target": "smithy.api#String"}`, `"Name": {"target": "smithy.api#String"}`, ""
	if latest {
		input += `, "Mode": {"target": "smithy.api#String"}`
		output += `, "Mode": {"target": "smithy.api#String"}, "Phase": {"target": "smithy.api#String"}`
		ops = `"demo#UpdateWidgetMode": {"type": "operation", "input": {"target": "demo#UpdateWidgetModeIn"}},
			"demo#UpdateWidgetModeIn": {"type": "structure", "members": {
				"WidgetId": {"target": "smithy.api#String"}, "Mode": {"target": "smithy.api#String"}}},`
	}
	m, err := LoadSmithyModel([]byte(`{"shapes": {` + ops + `
		"demo#CreateWidget": {"type": "operation", "input": {"target": "demo#CreateWidgetIn"}},
		"demo#CreateWidgetIn": {"type": "structure", "members": {` + input + `}},
		"demo#GetWidget": {"type": "operation", "output": {"target": "demo#GetWidgetOut"}},
		"demo#GetWidgetOut": {"type": "structure", "members": {"Widget": {"target": "demo#Widget"}}},
		"demo#Widget": {"type": "structure", "members": {` + output + `}}
	}}`))
	require.NoError(t, err)
	return m
}

func TestStatusFieldDoesNotHideSettableField(t *testing.T) {
	// status.mode and status.phase exist; spec.mode does not.
	in := &ControllerInputs{
		Config:          &generatorConfig{},
		CRDFields:       map[string]map[string]bool{"Widget": {"name": true}},
		CRDStatusFields: map[string]map[string]bool{"Widget": {"mode": true, "phase": true}},
		UsedOps:         map[string]map[string]bool{"widget": {"CreateWidget": true, "GetWidget": true}},
	}
	latest, baseline := sectionModel(t, true), sectionModel(t, false)

	got := findAddedFields(latest, baseline, in)
	assert.Equal(t, []string{"Mode"}, subjects(got, ClassSpecField), "a newly settable field is not in Spec")
	assert.Empty(t, subjects(got, ClassStatusField), "an output-only field Status has is exposed")

	got = setterFieldCandidates(latest, in, "Widget", "UpdateWidgetMode", OpTypes{OpTypeUpdate}, nil)
	assert.Equal(t, []string{"Mode"}, subjects(got, ClassSpecField))

	// A Spec field covers output-only candidates too: codegen leaves it out of Status.
	in.CRDFields["Widget"]["mode"], in.CRDFields["Widget"]["phase"] = true, true
	in.CRDStatusFields = nil
	assert.Empty(t, findAddedFields(latest, baseline, in))
}
