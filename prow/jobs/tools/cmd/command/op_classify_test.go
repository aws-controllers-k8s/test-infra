package command

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassifyOp(t *testing.T) {
	tests := []struct {
		opID      string
		resources []string
		wantType  OpType
		wantRes   string
	}{
		{"CreateBucket", nil, OpTypeCreate, "Bucket"},
		{"CreateOrUpdateTags", nil, OpTypeReplace, "Tags"},
		{"BatchCreateTables", nil, OpTypeCreateBatch, "Table"},
		{"CreateBatchTables", nil, OpTypeCreateBatch, "Table"},
		{"ModifyDBInstance", nil, OpTypeUpdate, "DBInstance"},
		{"UpdateFunction", nil, OpTypeUpdate, "Function"},
		{"DeleteBucket", nil, OpTypeDelete, "Bucket"},
		{"DescribeCluster", nil, OpTypeGet, "Cluster"},
		{"DescribeClusters", nil, OpTypeList, "Cluster"},
		{"GetBucket", nil, OpTypeGet, "Bucket"},
		{"GetBuckets", nil, OpTypeList, "Bucket"},
		{"ListBuckets", nil, OpTypeList, "Bucket"},
		{"GetQueueAttributes", nil, OpTypeGetAttributes, "Queue"},
		{"SetQueueAttributes", nil, OpTypeSetAttributes, "Queue"},
		{"PutBucketTagging", nil, OpTypeUnknown, "PutBucketTagging"},
		{"TagResource", nil, OpTypeUnknown, "TagResource"},

		// Plural Create with no matching config resource becomes a batch
		// create on the singular form.
		{"CreateTables", nil, OpTypeCreateBatch, "Table"},
		// ...but a declared "pluralized singular" resource stays as-is. This
		// is the EC2 DhcpOptions case.
		{"CreateDhcpOptions", []string{"DhcpOptions"}, OpTypeCreate, "DhcpOptions"},
		{"DescribeDhcpOptions", []string{"DhcpOptions"}, OpTypeList, "DhcpOptions"},

		// The declared-resource check is case-insensitive, like code-generator's
		// resourceExistsInConfig; the returned name comes from the operation.
		{"CreateDhcpOptions", []string{"dhcpoptions"}, OpTypeCreate, "DhcpOptions"},
		{"CreateDhcpOptions", []string{"DHCPOPTIONS"}, OpTypeCreate, "DhcpOptions"},

		// Declared plurals keep their spelling in the Get and List branches.
		{"GetDhcpOptions", []string{"DhcpOptions"}, OpTypeGet, "DhcpOptions"},
		{"ListDhcpOptions", []string{"DhcpOptions"}, OpTypeList, "DhcpOptions"},

		// A Set prefix without an Attributes suffix falls through to Unknown, name unstripped.
		{"SetBucketPolicy", nil, OpTypeUnknown, "SetBucketPolicy"},
	}

	for _, tc := range tests {
		// Rows share an opID, so include the declared resources in the subtest name.
		t.Run(fmt.Sprintf("%s/%v", tc.opID, tc.resources), func(t *testing.T) {
			gotType, gotRes := ClassifyOp(tc.opID, tc.resources)
			assert.Equal(t, tc.wantType, gotType, "op type")
			assert.Equal(t, tc.wantRes, gotRes, "resource name")
		})
	}
}

func TestClassifyOpBatchCreateIsNotACreate(t *testing.T) {
	// code-generator emits a CRD only for OpTypeCreate resources (pkg/model/model.go), so
	// CreateBatch and Replace must not imply one.
	opType, resName := ClassifyOp("BatchCreateTables", nil)
	assert.Equal(t, OpTypeCreateBatch, opType)
	assert.Equal(t, "Table", resName)

	// An undeclared plural Create<X>s is CreateBatch, so it implies no CRD either.
	opType, resName = ClassifyOp("CreateTables", nil)
	assert.Equal(t, OpTypeCreateBatch, opType)
	assert.Equal(t, "Table", resName)

	opType, _ = ClassifyOp("CreateOrUpdateTags", nil)
	assert.Equal(t, OpTypeReplace, opType)
}
