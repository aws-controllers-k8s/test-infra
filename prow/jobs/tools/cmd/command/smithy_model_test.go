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

	// Config.Nested is recorded as a member, but the walk does not descend
	// into it: its target is WidgetConfig, which is already on the current
	// branch. That is the cycle guard working.
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

	// Rules.Tier appears, but its enum's permitted values (STANDARD, ARCHIVE)
	// must NOT. A Smithy enum carries a members map just like a structure, so an
	// unguarded walk reports every allowed value as a field — on the real s3
	// model that was ~40% of producer 3's output, one entry per AWS region under
	// LocationConstraint. An enum-typed member is a plain string in the CRD.
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
