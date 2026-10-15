package command

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fakeControllerPath = "../../../testdata/fake-controller"

func TestReadControllerInputs(t *testing.T) {
	in, err := ReadControllerInputs("../../../testdata", "fake")
	require.NoError(t, err)

	assert.Equal(t, "fake", in.Service)
	assert.Equal(t, "v1.41.5", in.SDKVersion)
	assert.Equal(t, "v1.95.1", in.ServiceSDKVersion)
	assert.Equal(t, "demo-service", in.ModelName)
	assert.Equal(t, "demoservice", in.PackageName)
	assert.True(t, in.CRDFields["Widget"]["config.size"])
	assert.True(t, in.UsedOps["widget"]["PutWidgetTagging"])
	assert.True(t, in.HasCRD("Widget"))
	assert.False(t, in.HasCRD("Gadget"))
}

func TestHasCRDIgnoresCase(t *testing.T) {
	in, err := ReadControllerInputs("../../../testdata", "fake")
	require.NoError(t, err)

	// Kind lookup is case-insensitive (AWS "VpcEndpoint" vs CRD "VPCEndpoint").
	assert.True(t, in.HasCRD("Widget"))
	assert.True(t, in.HasCRD("WIDGET"))
	assert.True(t, in.HasCRD("widget"))
	assert.False(t, in.HasCRD("Gizmo"))

	canonical, ok := in.CanonicalKind("WIDGET")
	assert.True(t, ok)
	assert.Equal(t, "Widget", canonical, "must return the CRD's own casing")

	_, ok = in.CanonicalKind("Gizmo")
	assert.False(t, ok)
}

func TestReadControllerInputsMissingRepo(t *testing.T) {
	_, err := ReadControllerInputs("../../../testdata", "nope")
	assert.Error(t, err)
}

func TestClassifyOpWithOverrides(t *testing.T) {
	in, err := ReadControllerInputs("../../../testdata", "fake")
	require.NoError(t, err)

	// No override: falls back to name inference.
	opTypes, resName := in.ClassifyOpWithOverrides("CreateWidget", nil)
	assert.Equal(t, OpTypes{OpTypeCreate}, opTypes)
	assert.Equal(t, "Widget", resName)

	// Override sets resource_name and operation_type; the name alone infers OpTypeUnknown.
	bare, _ := ClassifyOp("ChangeGadgetSettings", nil)
	assert.Equal(t, OpTypeUnknown, bare, "precondition: name alone must not classify")

	// A multi-valued operation_type registers the op under each role, as code-generator does,
	// so Gadget must not look undeletable.
	opTypes, resName = in.ClassifyOpWithOverrides("ChangeGadgetSettings", nil)
	assert.Equal(t, OpTypes{OpTypeCreate, OpTypeDelete}, opTypes)
	assert.True(t, opTypes.Has(OpTypeCreate))
	assert.True(t, opTypes.Has(OpTypeDelete))
	assert.False(t, opTypes.Has(OpTypeUpdate))
	assert.False(t, opTypes.Only(OpTypeCreate, OpTypeCreateBatch))
	assert.Equal(t, "Gadget", resName)
}

func TestClassifyOpWithOverridesDedupesTypes(t *testing.T) {
	in := &ControllerInputs{
		Config: &generatorConfig{
			Operations: map[string]operationOverride{
				"PutThing": {
					OperationType: stringArray{"Create", "CREATE", "Frobnicate", "Update"},
					ResourceName:  stringArray{"Thing"},
				},
			},
		},
	}
	opTypes, _ := in.ClassifyOpWithOverrides("PutThing", nil)
	assert.Equal(t, OpTypes{OpTypeCreate, OpTypeUpdate}, opTypes,
		"each recognised type once, unrecognised ones skipped")
}

func TestClassifyOpWithOverridesMultiResourceName(t *testing.T) {
	// Modeled on lambda's DeleteFunction override (resource_name: [Version, Function]):
	// the inferred name wins when it is in the list, instead of element [0].
	// kindsByLower is nil: only override resolution is tested here.
	in := &ControllerInputs{
		Config: &generatorConfig{
			Operations: map[string]operationOverride{
				"DeleteFunction": {
					OperationType: stringArray{"Delete"},
					ResourceName:  stringArray{"Version", "Function"},
				},
			},
		},
	}

	opType, resName := in.ClassifyOpWithOverrides("DeleteFunction", nil)
	assert.Equal(t, OpTypes{OpTypeDelete}, opType)
	assert.Equal(t, "Function", resName,
		"the inferred name must win over resource_name[0]")

	// When the inferred name is not among the declared ones, the first declared
	// name is the only available answer.
	in.Config.Operations["ChangeThing"] = operationOverride{
		OperationType: stringArray{"Create"},
		ResourceName:  stringArray{"Alpha", "Beta"},
	}
	_, resName = in.ClassifyOpWithOverrides("ChangeThing", nil)
	assert.Equal(t, "Alpha", resName)
}

func TestClassifyOpWithOverridesUnrecognisedOperationType(t *testing.T) {
	// An override listing only spellings we do not recognise must fall back to
	// name inference for the type, while still honouring resource_name.
	in := &ControllerInputs{
		Config: &generatorConfig{
			Operations: map[string]operationOverride{
				"CreateFooThing": {
					OperationType: stringArray{"Frobnicate", "Bespoke"},
					ResourceName:  stringArray{"Thing"},
				},
			},
		},
	}

	inferredType, _ := ClassifyOp("CreateFooThing", nil)

	opType, resName := in.ClassifyOpWithOverrides("CreateFooThing", nil)
	assert.Equal(t, OpTypes{inferredType}, opType, "unrecognised types must fall back to inference")
	assert.Equal(t, "Thing", resName, "resource_name still applies")
}

func TestClassifyOpWithOverridesNilConfig(t *testing.T) {
	in := &ControllerInputs{}
	opType, resName := in.ClassifyOpWithOverrides("CreateBucket", nil)
	assert.Equal(t, OpTypes{OpTypeCreate}, opType)
	assert.Equal(t, "Bucket", resName)
}

func TestOpTypeFromConfigStringAcceptsSnakeCase(t *testing.T) {
	// Real controllers use upper-snake spellings (READ_ONE, GET_ATTRIBUTES, SET_ATTRIBUTES).
	for _, tc := range []struct {
		in   string
		want OpType
	}{
		{"READ_ONE", OpTypeGet},
		{"read_one", OpTypeGet},
		{"READ_MANY", OpTypeList},
		{"GET_ATTRIBUTES", OpTypeGetAttributes},
		{"SET_ATTRIBUTES", OpTypeSetAttributes},
		{"Create", OpTypeCreate},
	} {
		got, known := opTypeFromConfigString(tc.in)
		assert.True(t, known, "%q must be recognised", tc.in)
		assert.Equal(t, tc.want, got, "for %q", tc.in)
	}

	_, known := opTypeFromConfigString("Frobnicate")
	assert.False(t, known)
}
