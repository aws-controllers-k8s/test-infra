package command

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadGeneratorConfig(t *testing.T) {
	cfg, err := readGeneratorConfig(fakeControllerPath)
	require.NoError(t, err)

	assert.Equal(t, "demo-service", cfg.SDKNames.ModelName)
	assert.Equal(t, "demoservice", cfg.SDKNames.PackageName)
	assert.Equal(t, []string{"Object", "Session"}, cfg.Ignore.ResourceNames)
	assert.Equal(t, []string{"PutBucketTagging"}, cfg.Ignore.Operations)
	assert.Equal(t, []string{"Ignored"}, cfg.Ignore.ShapeNames)
	assert.Equal(t, []string{"CreateWidgetInput.DeclinedField"}, cfg.Ignore.FieldPaths)

	// Field sourcing: the CRD field `Sourced` comes from another operation's
	// member rather than from a rename.
	widget, ok := cfg.Resources["Widget"]
	require.True(t, ok)
	sourced, ok := widget.Fields["Sourced"]
	require.True(t, ok)
	require.NotNil(t, sourced.From)
	assert.Equal(t, "CreateWidget", sourced.From.Operation)
	assert.Equal(t, "SourcedMember", sourced.From.Path)

	// `operation_type` is a sequence and `resource_name` a scalar in the
	// fixture; stringArray must accept both spellings, because real
	// generator.yaml files use each.
	override, ok := cfg.Operations["ChangeGadgetSettings"]
	require.True(t, ok)
	assert.Equal(t, stringArray{"Create", "Delete"}, override.OperationType)
	assert.Equal(t, stringArray{"Gadget"}, override.ResourceName)

	_, ok = cfg.Operations["NoSuchOperation"]
	assert.False(t, ok)

	assert.ElementsMatch(t, []string{"Widget", "Gadget"}, cfg.ResourceNames())

	// Renames are collapsed per resource across operations and across
	// input/output fields, because ACK keeps them consistent per resource.
	assert.Equal(t, map[string]string{
		"WidgetName": "Name",
		"WidgetArn":  "ARN",
	}, cfg.RenamesForResource("Widget"))

	assert.Empty(t, cfg.RenamesForResource("Gadget"))
	assert.Empty(t, cfg.RenamesForResource("Nonexistent"))
}
