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
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// opsModel builds a model from operation name -> request member names. A nil
// member list means the operation has no request shape.
func opsModel(t *testing.T, ops map[string][]string) *SmithyModel {
	t.Helper()
	shapes := map[string]any{}
	for op, members := range ops {
		shape := map[string]any{"type": "operation"}
		if members != nil {
			ms := map[string]any{}
			for _, m := range members {
				ms[m] = map[string]string{"target": "smithy.api#String"}
			}
			shapes["demo#"+op+"Request"] = map[string]any{"type": "structure", "members": ms}
			shape["input"] = map[string]string{"target": "demo#" + op + "Request"}
		}
		shapes["demo#"+op] = shape
	}
	data, err := json.Marshal(map[string]any{"shapes": shapes})
	require.NoError(t, err)
	m, err := LoadSmithyModel(data)
	require.NoError(t, err)
	return m
}

// TestPlaceUnknownOp pins each rule to a real operation. Request members come
// from the v1.47.1 models because several rules depend on them.
func TestPlaceUnknownOp(t *testing.T) {
	model := opsModel(t, map[string][]string{
		// s3control: AccessPointScope has its own Get/Put/Delete, but the request
		// carries GetAccessPoint's identifiers, so it configures the AccessPoint.
		"GetAccessPoint":      {"AccountId", "Name"},
		"PutAccessPointScope": {"AccountId", "Name", "Scope"},
		"GetAccessPointScope": {"AccountId", "Name"},
		// ec2: the attachment shares TransitGateway's name prefix but is
		// addressed by its own ID, so it is its own resource.
		"DescribeTransitGateways":                    {"TransitGatewayIds", "Filters", "DryRun"},
		"AcceptTransitGatewayClientVpnAttachment":    {"TransitGatewayAttachmentId", "DryRun"},
		"DescribeTransitGatewayClientVpnAttachments": {"Filters"},
		// networkfirewall: no name match at all; FirewallArn identifies it.
		"AssociateAvailabilityZones": {"UpdateToken", "FirewallArn", "FirewallName", "AvailabilityZoneMappings"},
		"StartFlowCapture":           {"FirewallArn"},
		// cloudwatchlogs: its own Get/Put, attributed by logGroupIdentifier.
		"PutSyslogConfiguration": {"logGroupIdentifier", "vpcEndpointId"},
		"GetSyslogConfiguration": {"logGroupIdentifier"},
		// Exact names: a new resource takes even an action verb; an existing one does not.
		"AcceptDelegationRequest":              {"DelegationRequestId"},
		"StopAIBenchmarkJob":                   {"AIBenchmarkJobName"},
		"AcquireRole":                          {"RoleArn"},
		"AttachRuleGroupsToProxyConfiguration": {"ProxyConfigurationName", "RuleGroups"},
		// ec2: named after the new ApplicationStatusCheck but takes InstanceIds, so it
		// acts on instances. A request naming the prefix resource keeps it there.
		"EnableApplicationStatusCheckSuppression": {"ClientToken", "DryRun", "DurationSeconds", "InstanceIds"},
		"PutApplicationStatusCheckThreshold":      {"ApplicationStatusCheckId", "Threshold"},
		// Queries and end-user operations, dropped even when they name a resource.
		"SearchVectors":            {"IndexName"},
		"BatchGetCollectionGroup":  {"names"},
		"AdminDeleteSoftwareToken": {"UserPoolId", "Username"},
		// Account-level settings: nothing but AccountId, or no request at all.
		"PutAccountProperties":                {"AccountId", "Properties"},
		"GetAccountProperties":                {"AccountId"},
		"EnableOutboundWebIdentityFederation": nil,
		// Resources the classifier cannot see, having no Create verb.
		"PutAsset":             {"DomainIdentifier", "Name"},
		"GetAsset":             {"DomainIdentifier", "Identifier"},
		"RegisterCapability":   {"applicationId", "capabilityName"},
		"DeregisterCapability": {"applicationId", "capabilityName"},
		"GetCapability":        {"applicationId", "capabilityName"},
		// ec2: the registration is only listed, so its name decides.
		"GetIpamRoutingPolicyRegistrationDeltas": {"DeltaId", "IpamInternetRegistryAssociationId", "StartTime"},
		"GetIpamRoutingPolicyRegistrations":      {"Cidr", "IpamInternetRegistryAssociationId"},
		// Nothing to go on.
		"ReticulateSplines": {"SplineArn"},
	})
	resources := map[string]string{}
	existing := map[string]bool{}
	for _, kind := range []string{"AccessPoint", "Firewall", "Instance", "LogGroup", "Role", "TransitGateway"} {
		resources[strings.ToLower(kind)] = kind
		existing[kind] = true
	}
	for _, kind := range []string{
		"AIBenchmarkJob", "ApplicationStatusCheck", "CollectionGroup", "DelegationRequest", "ProxyConfiguration",
		"IpamInternetRegistryAssociation", "IpamRoutingPolicyRegistration",
	} {
		resources[strings.ToLower(kind)] = kind
	}

	for _, tc := range []struct {
		op        string
		placement opPlacement
		name      string
	}{
		{"PutAccessPointScope", placeOnResource, "AccessPoint"},
		{"AcceptTransitGatewayClientVpnAttachment", placePossibleResource, "TransitGatewayClientVpnAttachment"},
		{"AssociateAvailabilityZones", placeOnResource, "Firewall"},
		{"PutSyslogConfiguration", placeOnResource, "LogGroup"},
		{"AcceptDelegationRequest", placeOnResource, "DelegationRequest"},
		{"StopAIBenchmarkJob", placeOnResource, "AIBenchmarkJob"},
		{"AttachRuleGroupsToProxyConfiguration", placeOnResource, "ProxyConfiguration"},
		{"EnableApplicationStatusCheckSuppression", placeOnResource, "Instance"},
		{"PutApplicationStatusCheckThreshold", placeOnResource, "ApplicationStatusCheck"},
		{"AcquireRole", placeDrop, dropAction},
		{"StartFlowCapture", placeDrop, dropAction},
		{"SearchVectors", placeDrop, dropQuery},
		{"BatchGetCollectionGroup", placeDrop, dropQuery},
		{"AdminDeleteSoftwareToken", placeDrop, dropEndUser},
		{"PutAccountProperties", placeDrop, dropAccountLevel},
		{"EnableOutboundWebIdentityFederation", placeDrop, dropAccountLevel},
		{"PutAsset", placePossibleResource, "Asset"},
		{"RegisterCapability", placePossibleResource, "Capability"},
		{"DeregisterCapability", placePossibleResource, "Capability"},
		{"GetIpamRoutingPolicyRegistrationDeltas", placeOnResource, "IpamRoutingPolicyRegistration"},
		{"ReticulateSplines", placeUnclassified, ""},
	} {
		t.Run(tc.op, func(t *testing.T) {
			placement, name := placeUnknownOp(tc.op, model, resources, existing)
			assert.Equal(t, tc.placement, placement)
			assert.Equal(t, tc.name, name)
		})
	}
}

func TestFindNewOperationsHonoursIgnoredResourcesBeforePlacing(t *testing.T) {
	// Guards: ignore.resource_names drops AbortSession before placement, which
	// would otherwise make it part of a possible Session resource.
	latest := opsModel(t, map[string][]string{
		"CreateSession": {"Name"},
		"AbortSession":  {"SessionId"},
	})
	baseline := opsModel(t, map[string][]string{"Placeholder": nil})
	in := &ControllerInputs{
		Config:       &generatorConfig{Ignore: ignoreConfig{ResourceNames: []string{"Session"}}},
		kindsByLower: map[string]string{},
	}
	got := findNewOperations(latest, baseline, in)
	assert.Empty(t, reportable(got))
	assert.Equal(t, []Finding{{Class: ClassDroppedOperation, Subject: "AbortSession",
		Detail: "concerns `Session`, which generator.yaml ignores", NewSincePin: true}}, got)
}

func TestFindNewOperationsCollapsesPossibleResources(t *testing.T) {
	// Register and Deregister of one Capability collapse into a single finding.
	latest := opsModel(t, map[string][]string{
		"RegisterCapability":   {"applicationId", "capabilityName"},
		"DeregisterCapability": {"applicationId", "capabilityName"},
		"GetCapability":        {"applicationId", "capabilityName"},
	})
	// A model needs at least one shape.
	baseline := opsModel(t, map[string][]string{"Placeholder": nil})
	in := &ControllerInputs{Config: &generatorConfig{}, kindsByLower: map[string]string{}}

	got := findNewOperations(latest, baseline, in)
	assert.Equal(t, []Finding{{
		Kind:        "Capability",
		Class:       ClassPossibleResource,
		Subject:     "Capability",
		Detail:      "no Create operation, so a controller cannot create it; modelling it needs an `operations:` override",
		NewSincePin: true,
		Evidence:    "DeregisterCapability,GetCapability,RegisterCapability",
	}}, got)
}

func TestFindNewOperationsPlacesSubObjectOperations(t *testing.T) {
	// Guards: ec2 operations that classify to names no resource has are still
	// placed on a sub-object, not skipped.
	latest := opsModel(t, map[string][]string{
		"ModifyVpcEndpointPayerResponsibility":       {"DryRun", "PayerResponsibility", "Scope", "ServiceId", "VpcEndpointId"},
		"DescribeApplicationStatus":                  {"DryRun", "Filters", "InstanceIds", "MaxResults", "NextToken"},
		"CreateApplicationStatusCheck":               {"Port"},
		"DeleteApplicationStatusCheck":               {"ApplicationStatusCheckId"},
		"DescribeApplicationStatusChecks":            {"ApplicationStatusCheckIds", "Filters", "IncludeAll"},
		"DescribeApplicationStatusCheckAssociations": {"ApplicationStatusCheckIds", "Filters"},
	})
	baseline := opsModel(t, map[string][]string{"Placeholder": nil})
	in := &ControllerInputs{
		Config:       &generatorConfig{},
		CRDFields:    map[string]map[string]bool{"VPCEndpoint": {"vpcendpointid": true}, "Instance": {}},
		kindsByLower: map[string]string{"vpcendpoint": "VPCEndpoint", "instance": "Instance"},
	}

	got := foldOperationsIntoResources(append(findNewResources(latest, baseline, in), findNewOperations(latest, baseline, in)...))
	byKind := map[string][]string{}
	for _, f := range got {
		byKind[f.Kind] = append(byKind[f.Kind], f.Class.String()+" "+f.Subject+" "+f.Evidence)
	}
	for _, v := range byKind {
		sort.Strings(v)
	}
	assert.Equal(t, map[string][]string{
		// No read returns these members under the same names, so they are manual
		// review. VpcEndpointId is the identifier and DryRun carries no state.
		"VPCEndpoint": {
			"lifecycle-field PayerResponsibility ModifyVpcEndpointPayerResponsibility",
			"lifecycle-field Scope ModifyVpcEndpointPayerResponsibility",
			"lifecycle-field ServiceId ModifyVpcEndpointPayerResponsibility",
			"new-operation ModifyVpcEndpointPayerResponsibility ",
		},
		"Instance": {"new-operation DescribeApplicationStatus "},
		"ApplicationStatusCheck": {
			"new-resource ApplicationStatusCheck CreateApplicationStatusCheck,DeleteApplicationStatusCheck," +
				"DescribeApplicationStatusCheckAssociations,DescribeApplicationStatusChecks",
		},
	}, byKind)
	for _, f := range got {
		if f.Subject == "DescribeApplicationStatus" {
			assert.Equal(t, "read: its response could back Status fields", f.Detail)
		}
	}
}

func TestFindNewResourcesFlagsResourcesNothingDeletes(t *testing.T) {
	// ec2's quotes: created, described, consumed by Cancel/Modify, never deleted.
	latest := opsModel(t, map[string][]string{
		"CreateCapacityReservationCancellationQuote":    {"CapacityReservationId"},
		"DescribeCapacityReservationCancellationQuotes": {"QuoteIds"},
		"CreateSecondaryNetwork":                        {"Ipv4CidrBlock"},
		"DeleteSecondaryNetwork":                        {"SecondaryNetworkId"},
	})
	baseline := opsModel(t, map[string][]string{"Placeholder": nil})
	in := &ControllerInputs{Config: &generatorConfig{}, kindsByLower: map[string]string{}}

	classes := map[string]FindingClass{}
	for _, f := range findNewResources(latest, baseline, in) {
		classes[f.Subject] = f.Class
	}
	assert.Equal(t, map[string]FindingClass{
		"CapacityReservationCancellationQuote": ClassTransientResource,
		"SecondaryNetwork":                     ClassNewResource,
	}, classes)
}

func TestFindNewOperationsDropsReadOnlyPossibleResources(t *testing.T) {
	// networkfirewall flow operations are only described and listed, so there is
	// nothing to manage.
	latest := opsModel(t, map[string][]string{
		"DescribeFlowOperation": {"FlowOperationId", "FirewallArn"},
		"ListFlowOperations":    {"FirewallArn"},
	})
	baseline := opsModel(t, map[string][]string{"Placeholder": nil})
	in := &ControllerInputs{Config: &generatorConfig{}, kindsByLower: map[string]string{}}

	got := findNewOperations(latest, baseline, in)
	assert.Empty(t, reportable(got))
	for _, f := range got {
		assert.Equal(t, ClassDroppedOperation, f.Class, f.Subject)
		assert.Equal(t, dropQuery, f.Detail, f.Subject)
	}
}

func TestSetterFieldCandidatesSkipRequestPlumbing(t *testing.T) {
	m := opsModel(t, map[string][]string{
		"UpdateBucketMetadataAnnotationTableConfiguration": {
			"AnnotationTableConfiguration", "Bucket", "ChecksumAlgorithm", "ContentMD5", "ExpectedBucketOwner",
		},
	})
	in := &ControllerInputs{Config: &generatorConfig{}, CRDFields: map[string]map[string]bool{"Bucket": {}}}
	got := setterFieldCandidates(m, in, "Bucket", "UpdateBucketMetadataAnnotationTableConfiguration", OpTypes{OpTypeUpdate})
	require.Len(t, got, 1)
	assert.Equal(t, "AnnotationTableConfiguration", got[0].Subject)
}

func TestMergeEvidence(t *testing.T) {
	got := mergeEvidence([]Finding{
		{Kind: "Firewall", Class: ClassSpecField, Subject: "ProxySettings", NewSincePin: true, Evidence: "CreateFirewall"},
		{Kind: "Firewall", Class: ClassStatusField, Subject: "ProxySettings", NewSincePin: true, Evidence: "DescribeFirewall"},
		{Kind: "Firewall", Class: ClassSpecField, Subject: "ProxySettings", NewSincePin: true, Evidence: "UpdateProxySettings"},
	})
	assert.Equal(t, []Finding{
		{Kind: "Firewall", Class: ClassSpecField, Subject: "ProxySettings", NewSincePin: true, Evidence: "CreateFirewall,UpdateProxySettings"},
		{Kind: "Firewall", Class: ClassStatusField, Subject: "ProxySettings", NewSincePin: true, Evidence: "DescribeFirewall"},
	}, got)
}

// responsesModel adds response members to opsModel's operations.
func responsesModel(t *testing.T, requests, responses map[string][]string) *SmithyModel {
	t.Helper()
	shapes := map[string]any{}
	for op, members := range requests {
		shape := map[string]any{"type": "operation"}
		for suffix, list := range map[string][]string{"Request": members, "Response": responses[op]} {
			if list == nil {
				continue
			}
			ms := map[string]any{}
			for _, m := range list {
				ms[m] = map[string]string{"target": "smithy.api#String"}
			}
			shapes["demo#"+op+suffix] = map[string]any{"type": "structure", "members": ms}
			key := "input"
			if suffix == "Response" {
				key = "output"
			}
			shape[key] = map[string]string{"target": "demo#" + op + suffix}
		}
		shapes["demo#"+op] = shape
	}
	data, err := json.Marshal(map[string]any{"shapes": shapes})
	require.NoError(t, err)
	m, err := LoadSmithyModel(data)
	require.NoError(t, err)
	return m
}

func TestReadStatusCandidates(t *testing.T) {
	// An old read the controller never calls still reports response members it gained.
	requests := map[string][]string{"DescribeInstanceStatus": {"InstanceIds"}}
	baseline := responsesModel(t, requests, map[string][]string{"DescribeInstanceStatus": {"InstanceState", "NextToken"}})
	latest := responsesModel(t, requests, map[string][]string{"DescribeInstanceStatus": {"InstanceState", "ApplicationStatus", "NextToken"}})
	in := &ControllerInputs{
		Config:       &generatorConfig{},
		CRDFields:    map[string]map[string]bool{"Instance": {}},
		kindsByLower: map[string]string{"instance": "Instance"},
	}
	got := findNewOperations(latest, baseline, in)
	assert.Equal(t, []Finding{{Kind: "Instance", Class: ClassStatusField, Subject: "ApplicationStatus",
		Detail: secondaryReadDetail, NewSincePin: true, Evidence: "DescribeInstanceStatus"}}, got)
}

func TestSetterFieldCandidatesReturnedUnderTheSameNameAreSpec(t *testing.T) {
	m := responsesModel(t,
		map[string][]string{"PutWidgetColor": {"WidgetId", "Color", "Shade"}, "GetWidget": {"WidgetId"}},
		map[string][]string{"GetWidget": {"Color"}})
	in := &ControllerInputs{
		Config:    &generatorConfig{},
		CRDFields: map[string]map[string]bool{"Widget": {}},
		UsedOps:   map[string]map[string]bool{"widget": {"GetWidget": true}},
	}
	got := setterFieldCandidates(m, in, "Widget", "PutWidgetColor", OpTypes{OpTypeUnknown})
	sort.Slice(got, func(i, j int) bool { return got[i].Subject < got[j].Subject })
	assert.Equal(t, []Finding{
		{Kind: "Widget", Class: ClassSpecField, Subject: "Color", NewSincePin: true, Evidence: "PutWidgetColor"},
		{Kind: "Widget", Class: ClassLifecycleField, Subject: "Shade", NewSincePin: true, Evidence: "PutWidgetColor",
			Detail: "not returned at this path, so reconciling it needs custom code"},
	}, got)
}

func TestPossibleResourcesIncludeGenericReads(t *testing.T) {
	latest := opsModel(t, map[string][]string{
		"AcceptTransitGatewayClientVpnAttachment": {"TransitGatewayAttachmentId"},
		"RejectTransitGatewayClientVpnAttachment": {"TransitGatewayAttachmentId"},
		"DeleteTransitGatewayClientVpnAttachment": {"TransitGatewayAttachmentId"},
		"DescribeTransitGatewayAttachments":       {"TransitGatewayAttachmentIds"},
	})
	baseline := opsModel(t, map[string][]string{"DescribeTransitGatewayAttachments": {"TransitGatewayAttachmentIds"}})
	in := &ControllerInputs{Config: &generatorConfig{}, kindsByLower: map[string]string{}}

	var evidence string
	for _, f := range findNewOperations(latest, baseline, in) {
		if f.Class == ClassPossibleResource {
			evidence = f.Evidence
		}
	}
	assert.Equal(t, "AcceptTransitGatewayClientVpnAttachment,DeleteTransitGatewayClientVpnAttachment,"+
		"DescribeTransitGatewayAttachments,RejectTransitGatewayClientVpnAttachment", evidence)
}

func TestIsStatusRead(t *testing.T) {
	assert.True(t, isStatusRead("DescribeInstanceStatus", "Instance"))
	assert.True(t, isStatusRead("GetFirewallHealth", "Firewall"))
	assert.False(t, isStatusRead("DescribeInstanceAttribute", "Instance"))
	assert.False(t, isStatusRead("DescribeQueries", "LogGroup"))
}

func TestReadStatusCandidatesIgnoreOtherOldReads(t *testing.T) {
	requests := map[string][]string{"DescribeInstanceAttribute": {"InstanceId"}}
	baseline := responsesModel(t, requests, map[string][]string{"DescribeInstanceAttribute": {"Groups"}})
	latest := responsesModel(t, requests, map[string][]string{"DescribeInstanceAttribute": {"Groups", "Operator"}})
	in := &ControllerInputs{
		Config:       &generatorConfig{},
		CRDFields:    map[string]map[string]bool{"Instance": {}},
		kindsByLower: map[string]string{"instance": "Instance"},
	}
	assert.Empty(t, findNewOperations(latest, baseline, in))
}

// TestFindNewOperationsDropsUnreadablePossibleResources: a resource with no Create
// and no read cannot have its state recovered, so it is dropped.
func TestFindNewOperationsDropsUnreadablePossibleResources(t *testing.T) {
	latest := opsModel(t, map[string][]string{
		"AcceptNetworkFirewallTransitGatewayAttachment": {"TransitGatewayAttachmentId"},
		"RejectNetworkFirewallTransitGatewayAttachment": {"TransitGatewayAttachmentId"},
		"DeleteNetworkFirewallTransitGatewayAttachment": {"TransitGatewayAttachmentId"},
	})
	baseline := opsModel(t, map[string][]string{"DescribeFirewall": {"FirewallName"}})
	in := &ControllerInputs{Config: &generatorConfig{}, kindsByLower: map[string]string{}}

	got := findNewOperations(latest, baseline, in)
	assert.Empty(t, reportable(got))
	var dropped []string
	for _, f := range got {
		dropped = append(dropped, f.Subject)
		assert.Equal(t, "lifecycle action on `NetworkFirewallTransitGatewayAttachment`, which has no Create "+
			"and no read operation, so a controller could not recover its state", f.Detail)
	}
	sort.Strings(dropped)
	assert.Equal(t, []string{
		"AcceptNetworkFirewallTransitGatewayAttachment",
		"DeleteNetworkFirewallTransitGatewayAttachment",
		"RejectNetworkFirewallTransitGatewayAttachment",
	}, dropped)
}

// TestReadStatusCandidatesDropGeneratedHistory: reports that only a Start*
// operation produces are dropped; configurations a Put* sets are kept.
func TestReadStatusCandidatesDropGeneratedHistory(t *testing.T) {
	requests := map[string][]string{
		"ListAnalysisReports":      {"FirewallArn"},
		"StartAnalysisReport":      {"FirewallArn"},
		"ListSyslogConfigurations": {"FirewallArn"},
		"PutSyslogConfiguration":   {"FirewallArn"},
	}
	latest := responsesModel(t, requests, map[string][]string{
		"ListAnalysisReports":      {"AnalysisReports", "NextToken"},
		"ListSyslogConfigurations": {"syslogConfigurations", "nextToken"},
	})
	baseline := opsModel(t, map[string][]string{"DescribeFirewall": {"FirewallName"}})
	in := &ControllerInputs{Config: &generatorConfig{}, CRDFields: map[string]map[string]bool{"Firewall": {}}}

	reports := readStatusCandidates(latest, baseline, in, "Firewall", "ListAnalysisReports")
	require.Len(t, reports, 1)
	assert.Equal(t, ClassDroppedField, reports[0].Class)
	assert.Equal(t, dropGeneratedHistory, reports[0].Detail)

	syslog := readStatusCandidates(latest, baseline, in, "Firewall", "ListSyslogConfigurations")
	require.Len(t, syslog, 1)
	assert.Equal(t, ClassStatusField, syslog[0].Class)
}
