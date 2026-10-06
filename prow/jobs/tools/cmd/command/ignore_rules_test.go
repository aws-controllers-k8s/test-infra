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

func TestIgnoresOperation(t *testing.T) {
	cfg := &generatorConfig{Ignore: ignoreConfig{Operations: []string{"ModifyVpcEndpoint"}}}
	assert.True(t, cfg.ignoresOperation("ModifyVpcEndpoint"))
	// Exact, as in codegen: neither a longer name nor another casing matches.
	assert.False(t, cfg.ignoresOperation("ModifyVpcEndpointServicePermissions"))
	assert.False(t, cfg.ignoresOperation("modifyvpcendpoint"))
	var none *generatorConfig
	assert.False(t, none.ignoresOperation("ModifyVpcEndpoint"))
}

func TestIgnoredOpReasonForIgnoredResources(t *testing.T) {
	in := &ControllerInputs{Config: &generatorConfig{Ignore: ignoreConfig{ResourceNames: []string{"Foo"}}}}

	// Every prefix code-generator's op-type inference recognises, including its
	// plural and batch forms.
	for _, op := range []string{
		"CreateFoo", "BatchCreateFoos", "CreateBatchFoos", "CreateFoos", "CreateOrUpdateFoo",
		"UpdateFoo", "ModifyFoo", "DeleteFoo",
		"DescribeFoo", "DescribeFoos", "GetFoo", "GetFoos", "ListFoos",
		"GetFooAttributes", "SetFooAttributes",
	} {
		assert.Equal(t, "concerns `Foo`, which generator.yaml ignores", in.ignoredOpReason(op), op)
	}
	// Near misses name another resource, and verbs codegen does not infer from
	// classify onto none; those are left to namesIgnoredResource.
	for _, op := range []string{
		"CreateFooBar", "DeleteFooBar", "DescribeFooBars", "GetFooPolicy",
		"CreateBarFoo", "PutFoo", "StartFoo", "StopFoo",
	} {
		assert.Empty(t, in.ignoredOpReason(op), op)
	}

	// An `operations:` override moves the operation to the resource it declares.
	in.Config.Operations = map[string]operationOverride{
		"CreateFoo": {OperationType: stringArray{"Create"}, ResourceName: stringArray{"Bar"}},
	}
	assert.Empty(t, in.ignoredOpReason("CreateFoo"))

	in.Config.Ignore.Operations = []string{"CreateFooBar"}
	assert.Equal(t, "listed in generator.yaml `ignore.operations`", in.ignoredOpReason("CreateFooBar"))
	assert.Empty(t, (&ControllerInputs{}).ignoredOpReason("CreateFoo"))
}

func TestFindNewResourcesHonoursIgnores(t *testing.T) {
	latest := opsModel(t, map[string][]string{
		"CreateFoo": {"Name"}, "DeleteFoo": {"FooId"},
		"CreateFooBar": {"Name"}, "DeleteFooBar": {"FooBarId"},
		"CreateGadget": {"Name"}, "DeleteGadget": {"GadgetId"},
	})
	baseline := opsModel(t, map[string][]string{"Placeholder": nil})
	in := &ControllerInputs{
		Config: &generatorConfig{Ignore: ignoreConfig{
			ResourceNames: []string{"Foo"},
			Operations:    []string{"CreateGadget"},
		}},
		kindsByLower: map[string]string{},
	}

	got := findNewResources(latest, baseline, in)
	assert.Equal(t, []string{"FooBar"}, subjects(got, ClassNewResource),
		"an ignored resource, or one only an ignored operation creates, is not new; FooBar is not Foo")
	assert.Equal(t, map[string]bool{"FooBar": true}, createResourceNames(latest, in, nil))
}

func TestFindNewOperationsHonoursIgnores(t *testing.T) {
	latest := opsModel(t, map[string][]string{
		"CreateWidget":       {"Name"},
		"DescribeWidget":     {"WidgetId"},
		"ModifyWidgetSecret": {"WidgetId", "Secret"},
		"DescribeFoos":       {"FooId"},
		"DeleteFoo":          {"FooId"},
		"CreateFooBar":       {"Name"},
		"DeleteFooBar":       {"FooBarId"},
		"ResetWidget":        {"WidgetId"},
	})
	baseline := opsModel(t, map[string][]string{"CreateWidget": {"Name"}, "DescribeWidget": {"WidgetId"}})
	in := &ControllerInputs{
		Config: &generatorConfig{Ignore: ignoreConfig{
			ResourceNames: []string{"Foo"},
			Operations:    []string{"ModifyWidgetSecret"},
		}},
		CRDFields:    map[string]map[string]bool{"Widget": {"name": true}},
		UsedOps:      map[string]map[string]bool{"widget": {"CreateWidget": true, "DescribeWidget": true}},
		kindsByLower: map[string]string{"widget": "Widget"},
	}

	got := findNewOperations(latest, baseline, in)
	for _, f := range reportable(got) {
		assert.NotContains(t, []string{"ModifyWidgetSecret", "DescribeFoos", "DeleteFoo"}, f.Subject)
		assert.NotEqual(t, "Secret", f.Subject, "an ignored operation yields no setter fields")
	}
	assert.Contains(t, got, Finding{Class: ClassDroppedOperation, Subject: "ModifyWidgetSecret",
		Detail: "listed in generator.yaml `ignore.operations`", NewSincePin: true})
	assert.Contains(t, got, Finding{Class: ClassDroppedOperation, Subject: "DescribeFoos",
		Detail: "concerns `Foo`, which generator.yaml ignores", NewSincePin: true})
	assert.Equal(t, []string{"ResetWidget"}, subjects(got, ClassNewOperation),
		"operations on the managed Widget are still reported")
	assert.ElementsMatch(t, []string{"DeleteFoo", "DescribeFoos", "ModifyWidgetSecret"},
		subjects(got, ClassDroppedOperation), "FooBar's operations are not Foo's")

	// Ignored operations already in the baseline are not news, so not listed.
	got = findNewOperations(latest, latest, in)
	assert.Empty(t, subjects(got, ClassDroppedOperation))
}

// ignoreFieldModel has a managed Widget. The latest model adds members that each
// ignore rule declines, beside look-alikes that must survive. ModifyWidgetSecret
// is called by the controller but in ignore.operations.
func ignoreFieldModel(t *testing.T, latest bool) *SmithyModel {
	t.Helper()
	create := `"Name": {"target": "smithy.api#String"},
		"Config": {"target": "demo#WidgetConfig"}`
	update := `"WidgetId": {"target": "smithy.api#String"},
		"Config": {"target": "demo#WidgetConfig"}`
	config := `"Size": {"target": "smithy.api#Integer"}`
	describe := `"Name": {"target": "smithy.api#String"}`
	rotate := `"WidgetId": {"target": "smithy.api#String"}`
	if latest {
		create += `, "Extra": {"target": "demo#ExtraSettings"},
			"Secret": {"target": "smithy.api#String"}`
		update += `, "Secret": {"target": "smithy.api#String"}`
		config += `, "Secret": {"target": "smithy.api#String"}`
		describe += `, "Extra": {"target": "demo#ExtraSettings"}`
		rotate += `, "Rotation": {"target": "smithy.api#String"}`
	}
	m, err := LoadSmithyModel([]byte(`{"shapes": {
		"demo#CreateWidget": {"type": "operation", "input": {"target": "demo#CreateWidgetRequest"}},
		"demo#CreateWidgetRequest": {"type": "structure", "members": {` + create + `}},
		"demo#UpdateWidget": {"type": "operation", "input": {"target": "demo#UpdateWidgetRequest"}},
		"demo#UpdateWidgetRequest": {"type": "structure", "members": {` + update + `}},
		"demo#DescribeWidget": {"type": "operation", "output": {"target": "demo#DescribeWidgetResponse"}},
		"demo#DescribeWidgetResponse": {"type": "structure", "members": {` + describe + `}},
		"demo#ModifyWidgetSecret": {"type": "operation", "input": {"target": "demo#ModifyWidgetSecretRequest"}},
		"demo#ModifyWidgetSecretRequest": {"type": "structure", "members": {` + rotate + `}},
		"demo#WidgetConfig": {"type": "structure", "members": {` + config + `}},
		"demo#ExtraSettings": {"type": "structure", "members": {"Mode": {"target": "smithy.api#String"}}}
	}}`))
	require.NoError(t, err)
	return m
}

func TestFindAddedFieldsHonoursIgnores(t *testing.T) {
	in := &ControllerInputs{
		Config: &generatorConfig{Ignore: ignoreConfig{
			Operations: []string{"ModifyWidgetSecret"},
			ShapeNames: []string{"ExtraSettings"},
			FieldPaths: []string{"CreateWidgetInput.Config.Secret"},
		}},
		CRDFields: map[string]map[string]bool{"Widget": {"name": true, "config": true, "config.size": true}},
		UsedOps: map[string]map[string]bool{"widget": {
			"CreateWidget": true, "UpdateWidget": true, "DescribeWidget": true, "ModifyWidgetSecret": true,
		}},
	}

	got := findAddedFields(ignoreFieldModel(t, true), ignoreFieldModel(t, false), in)
	assert.Equal(t, []Finding{
		// The field path names Config.Secret under CreateWidget's request, so
		// UpdateWidget's Config.Secret, at another root, still counts.
		{Kind: "Widget", Class: ClassLifecycleField, NewSincePin: true,
			Subject: "Config.Secret", Evidence: "UpdateWidget"},
		// ModifyWidgetSecret is ignored but hand-written code calls it, so it
		// still reports.
		{Kind: "Widget", Class: ClassLifecycleField, NewSincePin: true,
			Subject: "Rotation", Evidence: "ModifyWidgetSecret"},
		// Same last segment, other path: not ignored.
		{Kind: "Widget", Class: ClassSpecField, NewSincePin: true,
			Subject: "Secret", Evidence: "CreateWidget,UpdateWidget"},
	}, got, "Extra targets an ignored shape on both sides")
}

func TestIgnoresMember(t *testing.T) {
	m := ignoreFieldModel(t, true)
	cfg := &generatorConfig{Ignore: ignoreConfig{
		ShapeNames: []string{"ExtraSettings"},
		FieldPaths: []string{"CreateWidgetInput.Config.Secret", "WidgetConfig.Size"},
	}}
	ignored := func(op, path string, isOutput bool) bool {
		shape, ok := m.Operation(op)
		require.True(t, ok)
		ref := shape.Input
		if isOutput {
			ref = shape.Output
		}
		members := m.WalkMembers(ref.Target, maxWalkDepth)
		require.Contains(t, members, path)
		return cfg.ignoresMember(m, op, ref, isOutput, path, members)
	}

	// An ignored shape at any depth, and its subtree.
	assert.True(t, ignored("CreateWidget", "Extra", false))
	assert.True(t, ignored("CreateWidget", "Extra.Mode", false))
	assert.True(t, ignored("DescribeWidget", "Extra.Mode", true))
	// A field path rooted at the operation's request matches only that path.
	assert.True(t, ignored("CreateWidget", "Config.Secret", false))
	assert.False(t, ignored("CreateWidget", "Secret", false))
	assert.False(t, ignored("UpdateWidget", "Config.Secret", false))
	// One rooted at a nested shape matches wherever that shape is reached.
	assert.True(t, ignored("CreateWidget", "Config.Size", false))
	assert.True(t, ignored("UpdateWidget", "Config.Size", false))
	assert.False(t, ignored("CreateWidget", "Name", false))

	var none *generatorConfig
	assert.False(t, none.ignoresMember(m, "CreateWidget", nil, false, "Extra", nil))
}

func TestSetterFieldCandidatesHonoursIgnores(t *testing.T) {
	m, err := LoadSmithyModel([]byte(`{"shapes": {
		"demo#UpdateWidgetOptions": {"type": "operation", "input": {"target": "demo#UpdateWidgetOptionsRequest"}},
		"demo#UpdateWidgetOptionsRequest": {"type": "structure", "members": {
			"WidgetId": {"target": "smithy.api#String"},
			"Mode": {"target": "demo#ExtraSettings"},
			"Level": {"target": "smithy.api#String"},
			"Note": {"target": "smithy.api#String"}}},
		"demo#ExtraSettings": {"type": "structure", "members": {"Mode": {"target": "smithy.api#String"}}}
	}}`))
	require.NoError(t, err)
	in := &ControllerInputs{Config: &generatorConfig{Ignore: ignoreConfig{
		ShapeNames: []string{"ExtraSettings"},
		FieldPaths: []string{"UpdateWidgetOptionsInput.Level"},
	}}}

	got := setterFieldCandidates(m, in, "Widget", "UpdateWidgetOptions", OpTypes{OpTypeUpdate})
	assert.Equal(t, []string{"Note"}, subjects(got, ClassLifecycleField))
	assert.Empty(t, subjects(got, ClassSpecField))
}

func TestReadStatusCandidatesHonoursIgnores(t *testing.T) {
	model := func(latest bool) *SmithyModel {
		status := `"State": {"target": "smithy.api#String"}`
		if latest {
			status += `, "Extra": {"target": "demo#ExtraSettings"},
				"Reason": {"target": "smithy.api#String"},
				"Code": {"target": "smithy.api#String"}`
		}
		m, err := LoadSmithyModel([]byte(`{"shapes": {
			"demo#DescribeWidgetStatus": {"type": "operation", "output": {"target": "demo#DescribeWidgetStatusResponse"}},
			"demo#DescribeWidgetStatusResponse": {"type": "structure", "members": {
				"Status": {"target": "demo#StatusInfo"}}},
			"demo#StatusInfo": {"type": "structure", "members": {` + status + `}},
			"demo#ExtraSettings": {"type": "structure", "members": {"Mode": {"target": "smithy.api#String"}}}
		}}`))
		require.NoError(t, err)
		return m
	}
	in := &ControllerInputs{Config: &generatorConfig{Ignore: ignoreConfig{
		ShapeNames: []string{"ExtraSettings"},
		FieldPaths: []string{"StatusInfo.Reason"},
	}}}

	got := readStatusCandidates(model(true), model(false), in, "Widget", "DescribeWidgetStatus")
	assert.Equal(t, []string{"Status.Code"}, subjects(got, ClassStatusField))
}

func TestEntryMembersHonoursIgnores(t *testing.T) {
	m, err := LoadSmithyModel([]byte(`{"shapes": {
		"demo#DescribeTable": {"type": "operation", "output": {"target": "demo#DescribeTableResponse"}},
		"demo#DescribeTableResponse": {"type": "structure", "members": {
			"Replicas": {"target": "demo#ReplicaList"}}},
		"demo#ReplicaList": {"type": "list", "member": {"target": "demo#Replica"}},
		"demo#Replica": {"type": "structure", "members": {
			"RegionName": {"target": "smithy.api#String"},
			"WarmThroughput": {"target": "demo#WarmThroughput"},
			"ReplicaStatus": {"target": "smithy.api#String"}}},
		"demo#WarmThroughput": {"type": "structure", "members": {"ReadUnits": {"target": "smithy.api#Long"}}}
	}}`))
	require.NoError(t, err)
	cfg := &generatorConfig{Ignore: ignoreConfig{ShapeNames: []string{"WarmThroughput"}}}

	_, observed := entryMembers(m, cfg, []string{"DescribeTable"}, "Replicas", "ReplicaUpdates")
	names := []string{}
	for _, member := range observed {
		names = append(names, member.name)
	}
	assert.Equal(t, []string{"RegionName", "ReplicaStatus"}, names)
}
