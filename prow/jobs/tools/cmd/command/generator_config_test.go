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

	// Renames stay per operation.
	assert.Equal(t, map[string]string{"WidgetName": "Name"}, cfg.renamesFor("Widget", "CreateWidget"))
	assert.Equal(t, map[string]string{"WidgetArn": "ARN"}, cfg.renamesFor("Widget", "DescribeWidget"))
	assert.Equal(t, []string{"CreateWidget", "DescribeWidget"}, cfg.renamedOps("Widget"))

	assert.Empty(t, cfg.renamesFor("Widget", "UpdateWidget"))
	assert.Empty(t, cfg.renamesFor("Gadget", "CreateGadget"))
	assert.Empty(t, cfg.renamesFor("Nonexistent", "CreateWidget"))
}

func TestGeneratorConfigRenamesArePerOperation(t *testing.T) {
	// organizations' Account: one AWS member, two CRD fields.
	var cfg generatorConfig
	require.NoError(t, yaml.Unmarshal([]byte(`
resources:
  Account:
    renames:
      operations:
        CreateAccount:
          input_fields:
            AccountName: Name
          output_fields:
            Id: CreateAccountRequestId
        DescribeAccount:
          output_fields:
            Id: AccountID
        Both:
          input_fields:
            Id: FromInput
          output_fields:
            Id: FromOutput
`), &cfg))

	assert.Equal(t, []string{"CreateAccountRequestId"}, cfg.renamedPath("Account", "CreateAccount", []string{"Id"}))
	assert.Equal(t, []string{"AccountID"}, cfg.renamedPath("Account", "DescribeAccount", []string{"Id"}))
	assert.Equal(t, []string{"Id"}, cfg.renamedPath("Account", "CloseAccount", []string{"Id"}))
	assert.Equal(t, []string{"FromInput"}, cfg.renamedPath("Account", "Both", []string{"Id"}),
		"input_fields win, as in code-generator's GetResourceFieldName")
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
	assert.Equal(t, map[string]string{"VpcId": "VPCID"}, cfg.renamesFor("VPCEndpoint", "CreateVpcEndpoint"))

	_, ok = cfg.resource("VPCEndpointServiceConfiguration")
	assert.False(t, ok, "case-insensitive, not prefix")
	_, ok = (*generatorConfig)(nil).resource("VPCEndpoint")
	assert.False(t, ok)

	// An exact match wins over a key differing only in case.
	cfg.Resources["VPCEndpoint"] = resourceConfig{}
	assert.Empty(t, cfg.renamesFor("VPCEndpoint", "CreateVpcEndpoint"))
	assert.NotEmpty(t, cfg.renamesFor("VpcEndpoint", "CreateVpcEndpoint"))
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
		cfg.renamedPath("DeliveryStream", "UpdateDestination", split("HttpEndpointDestinationUpdate.S3Update.BucketARN")),
		"a dotted key may spell its parent renamed")
	assert.Equal(t, split("Original.Renamed"),
		cfg.renamedPath("DeliveryStream", "UpdateDestination", split("Original.Child")),
		"or as AWS names it")
	assert.Equal(t, split("Other.S3Update"),
		cfg.renamedPath("DeliveryStream", "UpdateDestination", split("Other.S3Update")),
		"a dotted key renames only under its own parent")
	assert.Equal(t, split("HttpEndpointDestinationConfiguration"),
		cfg.renamedPath("deliverystream", "UpdateDestination", split("HttpEndpointDestinationUpdate")),
		"the resource is found whatever its case")
	assert.Equal(t, split("A.B"), (*generatorConfig)(nil).renamedPath("DeliveryStream", "UpdateDestination", split("A.B")))
}
