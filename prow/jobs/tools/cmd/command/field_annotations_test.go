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

package command

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAnnotateFieldsPairsChangeLists is dynamodb's GlobalTableWitnesses: returned
// but not sent, and converged through a change list on UpdateTable, which makes it
// desired state rather than status.
func TestAnnotateFieldsPairsChangeLists(t *testing.T) {
	m, err := LoadSmithyModel([]byte(`{"shapes": {
		"demo#UpdateTable": {"type": "operation", "input": {"target": "demo#UpdateTableInput"}},
		"demo#UpdateTableInput": {"type": "structure", "members": {
			"TableName": {"target": "smithy.api#String"},
			"GlobalTableWitnessUpdates": {"target": "demo#WitnessUpdates"}}},
		"demo#WitnessUpdates": {"type": "list", "member": {"target": "demo#WitnessUpdate"}},
		"demo#WitnessUpdate": {"type": "structure", "members": {"Create": {"target": "demo#CreateWitness"}}},
		"demo#CreateWitness": {"type": "structure", "members": {"RegionName": {"target": "smithy.api#String"}}},
		"demo#DescribeTable": {"type": "operation", "output": {"target": "demo#DescribeTableOutput"}},
		"demo#DescribeTableOutput": {"type": "structure", "members": {
			"Table": {"target": "demo#TableDescription"}}},
		"demo#TableDescription": {"type": "structure", "members": {
			"GlobalTableWitnesses": {"target": "demo#Witnesses"}}},
		"demo#Witnesses": {"type": "list", "member": {"target": "demo#Witness"}},
		"demo#Witness": {"type": "structure", "members": {
			"RegionName": {"target": "smithy.api#String"},
			"WitnessStatus": {"target": "smithy.api#String"}}}
	}}`))
	require.NoError(t, err)
	in := &ControllerInputs{Config: &generatorConfig{}}

	got := annotateFields(m, in, []Finding{
		{Kind: "Table", Class: ClassStatusField, Subject: "GlobalTableWitnesses",
			NewSincePin: true, Evidence: "DescribeTable"},
		{Kind: "Table", Class: ClassLifecycleField, Subject: "GlobalTableWitnessUpdates",
			NewSincePin: true, Evidence: "UpdateTable"},
	})
	// RegionName is sent inside the change list, so it is the desired identity;
	// WitnessStatus is only ever returned, so it is a Status candidate of its own.
	assert.Equal(t, []Finding{
		{Kind: "Table", Class: ClassSpecField, Subject: "GlobalTableWitnesses",
			Detail: "custom reconciliation: `UpdateTable` changes it only through the change list " +
				"`GlobalTableWitnessUpdates`, so diffing and updating it needs custom code; Spec needs a " +
				"normalized entry shape holding `RegionName`, with the observed-only members listed under Status",
			NewSincePin: true, Evidence: "DescribeTable", ReadBy: "DescribeTable"},
		{Kind: "Table", Class: ClassLifecycleField, Subject: "GlobalTableWitnessUpdates",
			Detail: "internal change list `UpdateTable` applies to `GlobalTableWitnesses`: " +
				"a reconciliation detail, not a field to expose",
			NewSincePin: true, Evidence: "UpdateTable", SetBy: "UpdateTable"},
		{Kind: "Table", Class: ClassStatusField, Subject: "GlobalTableWitnesses.WitnessStatus",
			Detail:      "observed-only member of each `GlobalTableWitnesses` entry: no request sends it",
			NewSincePin: true, Evidence: "DescribeTable", ReadBy: "DescribeTable"},
	}, got)
}

// TestAnnotateFieldsFoldsReadBacks is networkfirewall's TransitGatewayId: sent on
// CreateFirewall and read back as `Firewall.TransitGatewayId`, which is one field,
// not two. A list wrapper is a per-element observation and stays.
func TestAnnotateFieldsFoldsReadBacks(t *testing.T) {
	m, err := LoadSmithyModel([]byte(`{"shapes": {
		"demo#CreateFirewall": {"type": "operation", "input": {"target": "demo#CreateFirewallInput"}},
		"demo#CreateFirewallInput": {"type": "structure", "members": {
			"TransitGatewayId": {"target": "smithy.api#String"}}},
		"demo#DescribeFirewall": {"type": "operation", "output": {"target": "demo#DescribeFirewallOutput"}},
		"demo#DescribeFirewallOutput": {"type": "structure", "members": {
			"Firewall": {"target": "demo#Firewall"},
			"Replicas": {"target": "demo#Replicas"}}},
		"demo#Firewall": {"type": "structure", "members": {
			"TransitGatewayId": {"target": "smithy.api#String"},
			"NumberOfAssociations": {"target": "smithy.api#Integer"}}},
		"demo#Replicas": {"type": "list", "member": {"target": "demo#Replica"}},
		"demo#Replica": {"type": "structure", "members": {
			"TransitGatewayId": {"target": "smithy.api#String"}}}
	}}`))
	require.NoError(t, err)

	got := annotateFields(m, &ControllerInputs{Config: &generatorConfig{}}, []Finding{
		{Kind: "Firewall", Class: ClassSpecField, Subject: "TransitGatewayId", NewSincePin: true, Evidence: "CreateFirewall"},
		{Kind: "Firewall", Class: ClassStatusField, Subject: "Firewall.TransitGatewayId", NewSincePin: true, Evidence: "DescribeFirewall"},
		{Kind: "Firewall", Class: ClassStatusField, Subject: "Firewall.NumberOfAssociations", NewSincePin: true, Evidence: "DescribeFirewall"},
		{Kind: "Firewall", Class: ClassStatusField, Subject: "Replicas.TransitGatewayId", NewSincePin: true, Evidence: "DescribeFirewall"},
	})
	subjects := make([]string, 0, len(got))
	for _, f := range got {
		subjects = append(subjects, f.Subject)
	}
	assert.Equal(t, []string{"TransitGatewayId", "Firewall.NumberOfAssociations", "Replicas.TransitGatewayId"}, subjects)
	assert.Equal(t, "CreateFirewall,DescribeFirewall", got[0].Evidence)
	assert.Equal(t, "CreateFirewall", got[0].SetBy)
	assert.Equal(t, "DescribeFirewall", got[0].ReadBy, "a read is durable read-back")
}

// TestAnnotateFieldsAddsUpdateOperations is networkfirewall's
// AvailabilityZoneMappings: new on CreateFirewall's request, and changed by two new
// operations findNewOperations reported separately.
func TestAnnotateFieldsAddsUpdateOperations(t *testing.T) {
	m := opsModel(t, map[string][]string{
		"CreateFirewall":                {"FirewallName", "AvailabilityZoneMappings"},
		"AssociateAvailabilityZones":    {"FirewallArn", "AvailabilityZoneMappings"},
		"DisassociateAvailabilityZones": {"FirewallArn", "AvailabilityZoneMappings"},
		"UpdateProxySettings":           {"FirewallArn", "ProxySettings"},
	})
	in := &ControllerInputs{Config: &generatorConfig{}}

	got := annotateFields(m, in, []Finding{
		{Kind: "Firewall", Class: ClassSpecField, Subject: "AvailabilityZoneMappings",
			NewSincePin: true, Evidence: "CreateFirewall"},
		{Kind: "Firewall", Class: ClassNewOperation, Subject: "AssociateAvailabilityZones", NewSincePin: true},
		{Kind: "Firewall", Class: ClassNewOperation, Subject: "DisassociateAvailabilityZones", NewSincePin: true},
		{Kind: "Firewall", Class: ClassNewOperation, Subject: "UpdateProxySettings", NewSincePin: true},
	})
	assert.Equal(t, "AssociateAvailabilityZones,CreateFirewall,DisassociateAvailabilityZones", got[0].Evidence)
	assert.Equal(t, "custom reconciliation: changed through `AssociateAvailabilityZones`, "+
		"`DisassociateAvailabilityZones`, not the resource's own Update, so an update hook must diff "+
		"the list and call them", got[0].Detail)
}

// TestAnnotateFieldsFlagsCustomSourcedSiblings is networkfirewall's
// EnableMonitoringDashboard, which arrives on the same operation the existing
// LoggingConfiguration field is read and written through by custom code.
func TestAnnotateFieldsFlagsCustomSourcedSiblings(t *testing.T) {
	m := opsModel(t, map[string][]string{
		"UpdateLoggingConfiguration": {"FirewallArn", "LoggingConfiguration", "EnableMonitoringDashboard"},
	})
	in := &ControllerInputs{Config: &generatorConfig{
		Resources: map[string]resourceConfig{"Firewall": {Fields: map[string]resourceFieldConfig{
			"LoggingConfiguration": {From: &resourceFieldFrom{
				Operation: "UpdateLoggingConfiguration", Path: "LoggingConfiguration"}},
		}}},
	}}

	got := annotateFields(m, in, []Finding{
		{Kind: "Firewall", Class: ClassSpecField, Subject: "EnableMonitoringDashboard",
			NewSincePin: true, Evidence: "UpdateLoggingConfiguration"},
	})
	assert.Equal(t, "sent with `LoggingConfiguration` on `UpdateLoggingConfiguration`, which custom code "+
		"reconciles: adding it means updating that hook, not only regenerating", got[0].Detail)
}

// TestAnnotateFieldsMarksCreateOnlyFields pins both halves of the rule: a field
// only a Create sends is immutable, and one another operation sends under a shorter
// path is not. Both are real lambda fields.
func TestAnnotateFieldsMarksCreateOnlyFields(t *testing.T) {
	m := opsModel(t, map[string][]string{
		"CreateFunction":      {"FunctionName", "StorageMode", "Runtime"},
		"UpdateFunctionCode":  {"FunctionName", "StorageMode"},
		"PublishLayerVersion": {"LayerName", "Runtime"},
	})
	in := &ControllerInputs{
		Config:  &generatorConfig{},
		UsedOps: map[string]map[string]bool{"function": {"CreateFunction": true, "UpdateFunctionCode": true}},
	}

	got := annotateFields(m, in, []Finding{
		{Kind: "Function", Class: ClassSpecField, Subject: "Runtime", NewSincePin: true, Evidence: "CreateFunction"},
		{Kind: "Function", Class: ClassSpecField, Subject: "StorageMode", NewSincePin: true, Evidence: "CreateFunction"},
	})
	assert.Equal(t, "create-only: no operation changes it after creation, so it is immutable", got[0].Detail)
	assert.Empty(t, got[1].Detail)
}

// TestAnnotateFieldsMultiRoleOperationIsNotCreateOnly: an operation declared
// operation_type: [Create, Update] is also the resource's Update, so a field only
// it sends changes after creation and needs no custom work.
func TestAnnotateFieldsMultiRoleOperationIsNotCreateOnly(t *testing.T) {
	m := opsModel(t, map[string][]string{"PutWidget": {"WidgetName", "Color"}})
	in := &ControllerInputs{
		Config: &generatorConfig{Operations: map[string]operationOverride{
			"PutWidget": {OperationType: stringArray{"Create", "Update"}, ResourceName: stringArray{"Widget"}},
		}},
		UsedOps: map[string]map[string]bool{"widget": {"PutWidget": true}},
	}

	got := annotateFields(m, in, []Finding{
		{Kind: "Widget", Class: ClassSpecField, Subject: "Color", NewSincePin: true, Evidence: "PutWidget"},
	})
	assert.Empty(t, got[0].Detail)
}

// TestAnnotateFieldsFlagsHandWrittenUpdate: a field the resource's own Update sends
// needs no hook when codegen writes that Update, but does when generator.yaml
// replaces it with update_operation.custom_method_name — codegen then sends
// nothing, and the hand-written method must.
func TestAnnotateFieldsFlagsHandWrittenUpdate(t *testing.T) {
	m := opsModel(t, map[string][]string{
		"CreateWidget": {"WidgetName"},
		"UpdateWidget": {"WidgetName", "Color"},
	})
	findings := []Finding{
		{Kind: "Widget", Class: ClassSpecField, Subject: "Color", NewSincePin: true, Evidence: "UpdateWidget"},
	}
	usedOps := map[string]map[string]bool{"widget": {"CreateWidget": true, "UpdateWidget": true}}

	generated := annotateFields(m, &ControllerInputs{Config: &generatorConfig{}, UsedOps: usedOps}, findings)
	assert.Empty(t, generated[0].Detail)

	widget := resourceConfig{}
	widget.UpdateOperation.CustomMethodName = "customUpdateWidget"
	in := &ControllerInputs{
		Config:  &generatorConfig{Resources: map[string]resourceConfig{"Widget": widget}},
		UsedOps: usedOps,
	}
	got := annotateFields(m, in, findings)
	assert.Equal(t, "custom reconciliation: sent on `UpdateWidget`, the resource's own Update, but that "+
		"update is the hand-written `customUpdateWidget`, so adding it means changing that code, not only "+
		"regenerating", got[0].Detail)
}

// TestAnnotateFieldsSeparatesSummaryAndDetailedViews is ec2's Instance application
// health with the real shapes: DescribeApplicationStatus returns, per instance, the
// detailed ApplicationStatus; DescribeInstanceStatus the two-member
// ApplicationStatusSummary. They are not one state with two sources, so both stay,
// named for the state rather than the `ApplicationStatuses` wrapper.
func TestAnnotateFieldsSeparatesSummaryAndDetailedViews(t *testing.T) {
	m, err := LoadSmithyModel([]byte(`{"shapes": {
		"demo#DescribeApplicationStatus": {"type": "operation", "output": {"target": "demo#AppOut"}},
		"demo#AppOut": {"type": "structure", "members": {"ApplicationStatuses": {"target": "demo#AppResp"}}},
		"demo#AppResp": {"type": "structure", "members": {"Instances": {"target": "demo#AppInstances"}}},
		"demo#AppInstances": {"type": "list", "member": {"target": "demo#AppInstance"}},
		"demo#AppInstance": {"type": "structure", "members": {
			"InstanceId": {"target": "smithy.api#String"},
			"ApplicationStatus": {"target": "demo#ApplicationStatus"}}},
		"demo#ApplicationStatus": {"type": "structure", "members": {
			"Status": {"target": "smithy.api#String"}, "StatusSince": {"target": "smithy.api#String"},
			"ResumeAt": {"target": "smithy.api#String"}}},
		"demo#DescribeInstanceStatus": {"type": "operation", "output": {"target": "demo#StatusOut"}},
		"demo#StatusOut": {"type": "structure", "members": {"InstanceStatuses": {"target": "demo#Statuses"}}},
		"demo#Statuses": {"type": "list", "member": {"target": "demo#InstanceStatus"}},
		"demo#InstanceStatus": {"type": "structure", "members": {
			"ApplicationStatus": {"target": "demo#ApplicationStatusSummary"}}},
		"demo#ApplicationStatusSummary": {"type": "structure", "members": {
			"Status": {"target": "smithy.api#String"}}}
	}}`))
	require.NoError(t, err)

	got := annotateFields(m, &ControllerInputs{Config: &generatorConfig{}}, []Finding{
		{Kind: "Instance", Class: ClassStatusField, Subject: "ApplicationStatuses",
			Detail: secondaryReadDetail, NewSincePin: true, Evidence: "DescribeApplicationStatus"},
		{Kind: "Instance", Class: ClassStatusField, Subject: "InstanceStatuses.ApplicationStatus",
			Detail: secondaryReadDetail, NewSincePin: true, Evidence: "DescribeInstanceStatus"},
	})
	require.Len(t, got, 2, "a summary beside a detailed view is two candidates")
	assert.Equal(t, secondaryReadDetail+"; candidate field `ApplicationStatus`, the detailed view: "+
		"`ApplicationStatus` (3 members) at `ApplicationStatuses.Instances.ApplicationStatus`, beside "+
		"`ApplicationStatusSummary` (1 member) from another read; list them as summary and detailed status, "+
		"or normalize them into one summary on purpose", got[0].Detail)
	assert.Equal(t, secondaryReadDetail+"; candidate field `ApplicationStatusSummary`, the summary view: "+
		"`ApplicationStatusSummary` (1 member) at `InstanceStatuses.ApplicationStatus`, beside "+
		"`ApplicationStatus` (3 members) from another read; list them as summary and detailed status, "+
		"or normalize them into one summary on purpose", got[1].Detail)
}

// TestAnnotateFieldsFoldsIdenticalViews: two reads returning the same shape are one
// candidate, and one whose shape the model does not give is left alone.
func TestAnnotateFieldsFoldsIdenticalViews(t *testing.T) {
	m, err := LoadSmithyModel([]byte(`{"shapes": {
		"demo#DescribeHealth": {"type": "operation", "output": {"target": "demo#HealthOut"}},
		"demo#HealthOut": {"type": "structure", "members": {"Healths": {"target": "demo#HealthList"}}},
		"demo#HealthList": {"type": "list", "member": {"target": "demo#Health"}},
		"demo#Health": {"type": "structure", "members": {"Status": {"target": "smithy.api#String"}}},
		"demo#DescribeWidgetStatus": {"type": "operation", "output": {"target": "demo#WidgetOut"}},
		"demo#WidgetOut": {"type": "structure", "members": {"Widgets": {"target": "demo#Widgets"}}},
		"demo#Widgets": {"type": "list", "member": {"target": "demo#Widget"}},
		"demo#Widget": {"type": "structure", "members": {"Health": {"target": "demo#Health"}}}
	}}`))
	require.NoError(t, err)

	got := annotateFields(m, &ControllerInputs{Config: &generatorConfig{}}, []Finding{
		{Kind: "Widget", Class: ClassStatusField, Subject: "Healths",
			Detail: secondaryReadDetail, NewSincePin: true, Evidence: "DescribeHealth"},
		{Kind: "Widget", Class: ClassStatusField, Subject: "Widgets.Health",
			Detail: secondaryReadDetail, NewSincePin: true, Evidence: "DescribeWidgetStatus"},
	})
	require.Len(t, got, 1)
	assert.Equal(t, "DescribeHealth,DescribeWidgetStatus", got[0].Evidence)
	assert.Equal(t, secondaryReadDetail+"; candidate field `Healths`, one state with alternative sources, "+
		"`Healths` from `DescribeHealth` or `Widgets.Health` from `DescribeWidgetStatus`", got[0].Detail)
}

// TestAnnotateResourcesFlagsPartsBeyondCRUD is networkfirewall's ProxyRuleGroup,
// with its real request and response members: no Update of its own, and the
// update token each rule operation takes comes from a different read.
func TestAnnotateResourcesFlagsPartsBeyondCRUD(t *testing.T) {
	requests := map[string][]string{
		"CreateProxyRuleGroup":      {"ProxyRuleGroupName"},
		"DeleteProxyRuleGroup":      {"ProxyRuleGroupName"},
		"DescribeProxyRuleGroup":    {"ProxyRuleGroupName", "ProxyRuleGroupArn"},
		"DescribeProxyRule":         {"ProxyRuleGroupName", "ProxyRuleGroupArn", "ProxyRuleName"},
		"CreateProxyRules":          {"ProxyRuleGroupName", "ProxyRuleGroupArn", "Rules"},
		"UpdateProxyRule":           {"ProxyRuleGroupName", "ProxyRuleGroupArn", "ProxyRuleName", "UpdateToken"},
		"UpdateProxyRulePriorities": {"ProxyRuleGroupName", "ProxyRuleGroupArn", "Rules", "UpdateToken"},
		"CreateLookupTable":         {"Name"},
		"DeleteLookupTable":         {"Name"},
	}
	m := responsesModel(t, requests, map[string][]string{
		"DescribeProxyRuleGroup": {"ProxyRuleGroup", "UpdateToken"},
		"DescribeProxyRule":      {"ProxyRule", "UpdateToken"},
	})
	in := &ControllerInputs{Config: &generatorConfig{}}

	got := annotateResources(m, in, []Finding{
		{Kind: "ProxyRuleGroup", Class: ClassNewResource, Subject: "ProxyRuleGroup", Detail: "implied",
			Evidence: "CreateProxyRuleGroup,CreateProxyRules,DeleteProxyRuleGroup,DescribeProxyRule," +
				"DescribeProxyRuleGroup,UpdateProxyRule,UpdateProxyRulePriorities"},
		{Kind: "LookupTable", Class: ClassNewResource, Subject: "LookupTable", Detail: "implied",
			Evidence: "CreateLookupTable,DeleteLookupTable"},
	})
	assert.Equal(t, "implied; it has no Update of its own: `CreateProxyRules`, `UpdateProxyRule`, "+
		"`UpdateProxyRulePriorities` are how it changes, and codegen does not wire them in, so it needs a "+
		"custom diff and update hooks; `UpdateProxyRule` takes the update token `DescribeProxyRule` returns, "+
		"`UpdateProxyRulePriorities` takes the update token `DescribeProxyRuleGroup` returns", got[0].Detail)
	assert.Equal(t, "implied", got[1].Detail, "a resource of only its own operations needs no note")
}

// TestAnnotateFieldsAttributesRolesByPath is ec2's VPCEndpoint payer fields. The
// setter sends a top-level Scope; responses return it only inside each
// PayerResponsibilities entry, and that difference is what a maintainer needs to
// see. PayerResponsibilities itself comes back unwrapped from the controller's own
// list read, through VpcEndpoints, which stands for the resource.
func TestAnnotateFieldsAttributesRolesByPath(t *testing.T) {
	m, err := LoadSmithyModel([]byte(`{"shapes": {
		"demo#ModifyVpcEndpointPayerResponsibility": {"type": "operation",
			"input": {"target": "demo#ModifyInput"}, "output": {"target": "demo#ModifyOutput"}},
		"demo#ModifyInput": {"type": "structure", "members": {
			"VpcEndpointId": {"target": "smithy.api#String"},
			"Scope": {"target": "smithy.api#String"}}},
		"demo#ModifyOutput": {"type": "structure", "members": {
			"VpcEndpointId": {"target": "smithy.api#String"},
			"PayerResponsibilities": {"target": "demo#Payers"}}},
		"demo#Payers": {"type": "list", "member": {"target": "demo#Payer"}},
		"demo#Payer": {"type": "structure", "members": {"Scope": {"target": "smithy.api#String"}}},
		"demo#DescribeVpcEndpoints": {"type": "operation", "output": {"target": "demo#DescribeOutput"}},
		"demo#DescribeOutput": {"type": "structure", "members": {
			"VpcEndpoints": {"target": "demo#Endpoints"},
			"NextToken": {"target": "smithy.api#String"}}},
		"demo#Endpoints": {"type": "list", "member": {"target": "demo#Endpoint"}},
		"demo#Endpoint": {"type": "structure", "members": {"PayerResponsibilities": {"target": "demo#Payers"}}}
	}}`))
	require.NoError(t, err)
	in := &ControllerInputs{
		Config:  &generatorConfig{},
		UsedOps: map[string]map[string]bool{"vpcendpoint": {"DescribeVpcEndpoints": true}},
	}

	got := annotateFields(m, in, []Finding{
		{Kind: "VPCEndpoint", Class: ClassLifecycleField, Subject: "Scope", NewSincePin: true,
			Evidence: "ModifyVpcEndpointPayerResponsibility"},
		{Kind: "VPCEndpoint", Class: ClassStatusField, Subject: "PayerResponsibilities", NewSincePin: true,
			Evidence: "ModifyVpcEndpointPayerResponsibility"},
	})
	assert.Equal(t, "ModifyVpcEndpointPayerResponsibility", got[0].SetBy)
	assert.Equal(t, "DescribeVpcEndpoints=VpcEndpoints.PayerResponsibilities.Scope", got[0].ReadBy)
	assert.Equal(t, "ModifyVpcEndpointPayerResponsibility=PayerResponsibilities.Scope", got[0].ReturnedBy)
	assert.Equal(t, "DescribeVpcEndpoints", got[1].ReadBy)
	assert.Equal(t, "ModifyVpcEndpointPayerResponsibility", got[1].ReturnedBy)
}
