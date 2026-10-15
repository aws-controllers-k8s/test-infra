package command

import (
	"os"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loadTestModel(t *testing.T, name string) *SmithyModel {
	t.Helper()
	data, err := os.ReadFile("../../../testdata/" + name)
	require.NoError(t, err)
	m, err := LoadSmithyModel(data)
	require.NoError(t, err)
	return m
}

func TestLoadSmithyModel_OperationNames(t *testing.T) {
	m := loadTestModel(t, "smithy_basic.json")
	assert.Equal(t, []string{"CreateWidget", "DeleteWidget"}, m.OperationNames())
}

func TestSmithyModel_Operation(t *testing.T) {
	m := loadTestModel(t, "smithy_basic.json")

	op, ok := m.Operation("CreateWidget")
	require.True(t, ok)
	assert.Equal(t, "com.amazonaws.demo#CreateWidgetRequest", op.Input.Target)
	assert.Equal(t, "com.amazonaws.demo#CreateWidgetResponse", op.Output.Target)
	assert.Equal(t, "<p>Creates a widget.</p>", docTrait(op.Traits))

	_, ok = m.Operation("NoSuchOp")
	assert.False(t, ok)
}

func TestSmithyModel_OperationWithoutOutputOrDoc(t *testing.T) {
	m := loadTestModel(t, "smithy_basic.json")

	op, ok := m.Operation("DeleteWidget")
	require.True(t, ok)
	assert.Nil(t, op.Output)
	assert.Equal(t, "", docTrait(op.Traits))
}

func TestSmithyModel_WalkMembers(t *testing.T) {
	m := loadTestModel(t, "smithy_basic.json")

	got := m.WalkMembers("com.amazonaws.demo#CreateWidgetRequest", 4)

	paths := make([]string, 0, len(got))
	for p := range got {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	// Config.Nested is recorded but not descended into: WidgetConfig is already on the branch.
	assert.Equal(t, []string{
		"Config",
		"Config.Nested",
		"Config.Size",
		"Name",
	}, paths)

	assert.Equal(t, "<p>The name.</p>", got["Name"].Doc)
	assert.Equal(t, "smithy.api#String", got["Name"].Target)
}

func TestSmithyModel_WalkMembersRespectsDepth(t *testing.T) {
	m := loadTestModel(t, "smithy_basic.json")

	got := m.WalkMembers("com.amazonaws.demo#CreateWidgetRequest", 1)

	paths := make([]string, 0, len(got))
	for p := range got {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	assert.Equal(t, []string{"Config", "Name"}, paths)
}

func TestSmithyModel_WalkMembersUnknownShape(t *testing.T) {
	m := loadTestModel(t, "smithy_basic.json")
	assert.Empty(t, m.WalkMembers("com.amazonaws.demo#Nope", 4))
	assert.Empty(t, m.WalkMembers("", 4))
}

func TestSmithyModel_WalkMembersFlattensLists(t *testing.T) {
	m := loadTestModel(t, "smithy_list.json")

	got := m.WalkMembers("com.amazonaws.listdemo#PutRulesRequest", 4)

	paths := make([]string, 0, len(got))
	for p := range got {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	// Rules.Tier appears but its enum values (STANDARD, ARCHIVE) must not: Smithy enums have a
	// members map like structures, and on the s3 model that produced one field per region.
	assert.Equal(t, []string{
		"BucketName",
		"Rules",
		"Rules.Prefix",
		"Rules.Status",
		"Rules.Tier",
	}, paths)

	assert.NotContains(t, paths, "Rules.Tier.STANDARD")
	assert.NotContains(t, paths, "Rules.Tier.ARCHIVE")
}

// mapModel is networkfirewall's FirewallStatus.SyncStates map[string]SyncState,
// plus a scalar map and a self-referencing one for the cycle guard.
func mapModel(t *testing.T, latest bool) *SmithyModel {
	t.Helper()
	syncState := `"Attachment": {"target": "smithy.api#String"}`
	if latest {
		syncState += `, "NatGatewayAttachments": {"target": "smithy.api#String"}`
	}
	m, err := LoadSmithyModel([]byte(`{"shapes": {
		"fw#DescribeFirewall": {"type": "operation", "input": {"target": "fw#DescribeFirewallRequest"},
			"output": {"target": "fw#DescribeFirewallResponse"}},
		"fw#DescribeFirewallRequest": {"type": "structure", "members": {"FirewallArn": {"target": "smithy.api#String"}}},
		"fw#DescribeFirewallResponse": {"type": "structure", "members": {
			"FirewallArn": {"target": "smithy.api#String"},
			"FirewallStatus": {"target": "fw#FirewallStatus"}}},
		"fw#FirewallStatus": {"type": "structure", "members": {
			"Status": {"target": "smithy.api#String"},
			"SyncStates": {"target": "fw#SyncStates"},
			"Labels": {"target": "fw#Labels"},
			"Tree": {"target": "fw#Tree"}}},
		"fw#SyncStates": {"type": "map", "key": {"target": "smithy.api#String"}, "value": {"target": "fw#SyncState"}},
		"fw#SyncState": {"type": "structure", "members": {` + syncState + `}},
		"fw#Labels": {"type": "map", "key": {"target": "smithy.api#String"}, "value": {"target": "smithy.api#String"}},
		"fw#Tree": {"type": "map", "key": {"target": "smithy.api#String"}, "value": {"target": "fw#Tree"}}
	}}`))
	require.NoError(t, err)
	return m
}

func TestSmithyModel_WalkMembersFlattensMapValues(t *testing.T) {
	m := mapModel(t, true)

	got := m.WalkMembers("fw#FirewallStatus", 4)
	paths := make([]string, 0, len(got))
	for p := range got {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	// Structured values appear at the map's path; scalar values and the
	// self-referencing Tree add nothing beneath it.
	assert.Equal(t, []string{
		"Labels",
		"Status",
		"SyncStates",
		"SyncStates.Attachment",
		"SyncStates.NatGatewayAttachments",
		"Tree",
	}, paths)
	assert.Equal(t, "fw#SyncStates", got["SyncStates"].Target)

	// A map does not consume depth, as with lists.
	assert.Contains(t, m.WalkMembers("fw#FirewallStatus", 2), "SyncStates.NatGatewayAttachments")
	assert.NotContains(t, m.WalkMembers("fw#FirewallStatus", 1), "SyncStates.NatGatewayAttachments")
}

// A wafv2-shaped model: a nine-segment path below a recursive Statement.
func TestSmithyModel_WalkMembersReachesDeepPathsAndStopsCycles(t *testing.T) {
	m, err := LoadSmithyModel([]byte(`{"shapes": {
		"waf#GetWebACLResponse": {"type": "structure", "members": {"WebACL": {"target": "waf#WebACL"}}},
		"waf#WebACL": {"type": "structure", "members": {"Rules": {"target": "waf#Rules"}}},
		"waf#Rules": {"type": "list", "member": {"target": "waf#Rule"}},
		"waf#Rule": {"type": "structure", "members": {"Statement": {"target": "waf#Statement"}}},
		"waf#Statement": {"type": "structure", "members": {
			"AndStatement": {"target": "waf#AndStatement"},
			"ManagedRuleGroupStatement": {"target": "waf#ManagedRuleGroupStatement"}}},
		"waf#AndStatement": {"type": "structure", "members": {"Statements": {"target": "waf#Statements"}}},
		"waf#Statements": {"type": "list", "member": {"target": "waf#Statement"}},
		"waf#ManagedRuleGroupStatement": {"type": "structure", "members": {
			"ScopeDownStatement": {"target": "waf#Statement"},
			"RuleActionOverrides": {"target": "waf#RuleActionOverrides"}}},
		"waf#RuleActionOverrides": {"type": "list", "member": {"target": "waf#RuleActionOverride"}},
		"waf#RuleActionOverride": {"type": "structure", "members": {"ActionToUse": {"target": "waf#RuleAction"}}},
		"waf#RuleAction": {"type": "structure", "members": {"Monetize": {"target": "waf#Monetize"}}},
		"waf#Monetize": {"type": "structure", "members": {"Pricing": {"target": "waf#Pricing"}}},
		"waf#Pricing": {"type": "structure", "members": {"PriceMultiplier": {"target": "smithy.api#Integer"}}}
	}}`))
	require.NoError(t, err)

	got := m.WalkMembers("waf#GetWebACLResponse", maxWalkDepth)

	assert.Contains(t, got, "WebACL.Rules.Statement.ManagedRuleGroupStatement.RuleActionOverrides."+
		"ActionToUse.Monetize.Pricing.PriceMultiplier")
	// Statement is recorded where it recurs but not re-entered on its own branch.
	assert.Contains(t, got, "WebACL.Rules.Statement.AndStatement.Statements")
	assert.Contains(t, got, "WebACL.Rules.Statement.ManagedRuleGroupStatement.ScopeDownStatement")
	assert.NotContains(t, got, "WebACL.Rules.Statement.AndStatement.Statements.AndStatement")
	assert.NotContains(t, got, "WebACL.Rules.Statement.ManagedRuleGroupStatement.ScopeDownStatement.AndStatement")
}

func TestElementShapeUnwrapsMapValues(t *testing.T) {
	m := mapModel(t, true)
	assert.Equal(t, "fw#SyncState", elementShape(m, "fw#SyncStates"))
	assert.Equal(t, "smithy.api#String", elementShape(m, "fw#Labels"))
}
