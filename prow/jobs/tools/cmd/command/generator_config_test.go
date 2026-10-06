package command

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
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

	// Real generator.yaml files use both scalar and sequence forms; stringArray accepts both.
	override, ok := cfg.Operations["ChangeGadgetSettings"]
	require.True(t, ok)
	assert.Equal(t, stringArray{"Create", "Delete"}, override.OperationType)
	assert.Equal(t, stringArray{"Gadget"}, override.ResourceName)

	_, ok = cfg.Operations["NoSuchOperation"]
	assert.False(t, ok)

	assert.ElementsMatch(t, []string{"Widget", "Gadget"}, cfg.ResourceNames())

	// Renames are merged per resource across operations and input/output fields.
	assert.Equal(t, map[string]string{
		"WidgetName": "Name",
		"WidgetArn":  "ARN",
	}, cfg.RenamesForResource("Widget"))

	assert.Empty(t, cfg.RenamesForResource("Gadget"))
	assert.Empty(t, cfg.RenamesForResource("Nonexistent"))
}

func TestGeneratorConfigParsesInputWrapper(t *testing.T) {
	// backup's real declarations.
	var cfg generatorConfig
	require.NoError(t, yaml.Unmarshal([]byte(`
operations:
  CreateBackupPlan:
    input_wrapper_field_path: BackupPlan
  GetBackupPlan:
    output_wrapper_field_path: BackupPlan
`), &cfg))

	assert.Equal(t, "BackupPlan", cfg.inputWrapper("CreateBackupPlan"))
	assert.Equal(t, "", cfg.inputWrapper("GetBackupPlan"),
		"an output wrapper is not an input wrapper")
	assert.Equal(t, "", cfg.inputWrapper("DeleteBackupPlan"))
	assert.Equal(t, "", (*generatorConfig)(nil).inputWrapper("CreateBackupPlan"))
}

func TestGeneratorConfigResourceIgnoresCase(t *testing.T) {
	// ec2's real spellings: generator.yaml keys use the inferred name, callers hold the CRD kind.
	dhcp := resourceConfig{}
	dhcp.UpdateOperation.CustomMethodName = "customUpdate"
	cfg := &generatorConfig{Resources: map[string]resourceConfig{
		"VpcEndpoint": {Renames: resourceRenames{Operations: map[string]operationRenames{
			"CreateVpcEndpoint": {InputFields: map[string]string{"VpcId": "VPCID"}},
		}}},
		"DhcpOptions": dhcp,
	}}

	res, ok := cfg.resource("DHCPOptions")
	require.True(t, ok)
	assert.Equal(t, "customUpdate", res.UpdateOperation.CustomMethodName)
	assert.Equal(t, map[string]string{"VpcId": "VPCID"}, cfg.RenamesForResource("VPCEndpoint"))

	_, ok = cfg.resource("VPCEndpointServiceConfiguration")
	assert.False(t, ok, "case-insensitive, not prefix")
	_, ok = (*generatorConfig)(nil).resource("VPCEndpoint")
	assert.False(t, ok)

	// An exact match wins over a key differing only in case.
	cfg.Resources["VPCEndpoint"] = resourceConfig{}
	assert.Empty(t, cfg.RenamesForResource("VPCEndpoint"))
	assert.NotEmpty(t, cfg.RenamesForResource("VpcEndpoint"))
}

func TestGeneratorConfigRenamedPath(t *testing.T) {
	// firehose's DeliveryStream: a top-level rename, and a dotted one whose
	// parent is spelled as already renamed.
	cfg := &generatorConfig{Resources: map[string]resourceConfig{
		"DeliveryStream": {Renames: resourceRenames{Operations: map[string]operationRenames{
			"UpdateDestination": {InputFields: map[string]string{
				"HttpEndpointDestinationUpdate":                 "HttpEndpointDestinationConfiguration",
				"HttpEndpointDestinationConfiguration.S3Update": "S3Configuration",
				"Original.Child":                                "Renamed",
			}},
		}}},
	}}
	split := func(s string) []string { return strings.Split(s, ".") }

	assert.Equal(t, split("HttpEndpointDestinationConfiguration.S3Configuration.BucketARN"),
		cfg.renamedPath("DeliveryStream", split("HttpEndpointDestinationUpdate.S3Update.BucketARN")),
		"a dotted key may spell its parent renamed")
	assert.Equal(t, split("Original.Renamed"),
		cfg.renamedPath("DeliveryStream", split("Original.Child")),
		"or as AWS names it")
	assert.Equal(t, split("Other.S3Update"),
		cfg.renamedPath("DeliveryStream", split("Other.S3Update")),
		"a dotted key renames only under its own parent")
	assert.Equal(t, split("HttpEndpointDestinationConfiguration"),
		cfg.renamedPath("deliverystream", split("HttpEndpointDestinationUpdate")),
		"the resource is found whatever its case")
	assert.Equal(t, split("A.B"), (*generatorConfig)(nil).renamedPath("DeliveryStream", split("A.B")))
}
