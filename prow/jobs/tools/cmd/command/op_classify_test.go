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

		// The declared-resource check is case-insensitive, matching
		// code-generator's resourceExistsInConfig (strings.EqualFold). The
		// returned name is the one derived from the operation, not the
		// declaration's spelling.
		{"CreateDhcpOptions", []string{"dhcpoptions"}, OpTypeCreate, "DhcpOptions"},
		{"CreateDhcpOptions", []string{"DHCPOPTIONS"}, OpTypeCreate, "DhcpOptions"},

		// The declared-plural side of the Get and List branches. Without a
		// declaration these would singularize; with one they must keep the
		// plural spelling. Only the undeclared side is covered above.
		{"GetDhcpOptions", []string{"DhcpOptions"}, OpTypeGet, "DhcpOptions"},
		{"ListDhcpOptions", []string{"DhcpOptions"}, OpTypeList, "DhcpOptions"},

		// A Set prefix without an Attributes suffix matches the Set case, does
		// not return from it, and falls through to Unknown with the operation
		// name unstripped. That fallthrough is easy to break when editing the
		// switch, so pin it.
		{"SetBucketPolicy", nil, OpTypeUnknown, "SetBucketPolicy"},
	}

	for _, tc := range tests {
		// Include the declared resources in the subtest name: several rows
		// share an opID and differ only in that field, so naming by opID alone
		// yields CreateDhcpOptions#01 and you have to count table rows to find
		// which case failed.
		t.Run(fmt.Sprintf("%s/%v", tc.opID, tc.resources), func(t *testing.T) {
			gotType, gotRes := ClassifyOp(tc.opID, tc.resources)
			assert.Equal(t, tc.wantType, gotType, "op type")
			assert.Equal(t, tc.wantRes, gotRes, "resource name")
		})
	}
}

func TestClassifyOpBatchCreateIsNotACreate(t *testing.T) {
	// This distinction is load-bearing for producer 1. code-generator emits a
	// CRD only for resources with an OpTypeCreate operation
	// (pkg/model/model.go:128 builds crdNameKeys from opMap[OpTypeCreate]
	// alone). OpTypeCreateBatch and OpTypeReplace never produce a CRD, so
	// producer 1 must not treat them as implying one.
	opType, resName := ClassifyOp("BatchCreateTables", nil)
	assert.Equal(t, OpTypeCreateBatch, opType)
	assert.Equal(t, "Table", resName)

	// A plural Create<X>s with no declared resource also lands on CreateBatch,
	// so it likewise implies no CRD.
	opType, resName = ClassifyOp("CreateTables", nil)
	assert.Equal(t, OpTypeCreateBatch, opType)
	assert.Equal(t, "Table", resName)

	opType, _ = ClassifyOp("CreateOrUpdateTags", nil)
	assert.Equal(t, OpTypeReplace, opType)
}
