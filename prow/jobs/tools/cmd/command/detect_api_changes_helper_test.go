package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-github/v63/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTagPrefixFor(t *testing.T) {
	assert.Equal(t, "tags/v", tagPrefixFor(""))
	assert.Equal(t, "tags/service/s3/", tagPrefixFor("s3"))
}

func TestNewestSemverRef(t *testing.T) {
	refs := []string{
		"refs/tags/v1.41.5",
		"refs/tags/v1.44.0",
		"refs/tags/v1.9.0",
		"refs/tags/v1.44.0-rc.1",
		"refs/tags/vNotASemver",
	}
	got, err := newestSemverRef(refs)
	require.NoError(t, err)
	assert.Equal(t, "v1.44.0", got)
}

func TestNewestSemverRefPerService(t *testing.T) {
	refs := []string{
		"refs/tags/service/s3/v1.95.1",
		"refs/tags/service/s3/v1.100.0",
		"refs/tags/service/s3/v1.99.9",
	}
	got, err := newestSemverRef(refs)
	require.NoError(t, err)
	assert.Equal(t, "v1.100.0", got)
}

func TestNewestSemverRefNoUsableTags(t *testing.T) {
	_, err := newestSemverRef([]string{"refs/tags/garbage"})
	assert.Error(t, err)

	_, err = newestSemverRef(nil)
	assert.Error(t, err)
}

func TestNewestSemverRefHandlesVPrefix(t *testing.T) {
	// aquasecurity/go-version's regex is anchored with no `v?` tolerance, so
	// the prefix must be stripped before comparison. Without that, every real
	// aws-sdk-go-v2 tag is unparseable and this returns an error instead of a
	// version. The returned value must keep the prefix, since callers build
	// model URLs from it.
	got, err := newestSemverRef([]string{
		"refs/tags/v1.9.0",
		"refs/tags/v1.41.5",
		"refs/tags/v1.100.0",
	})
	require.NoError(t, err, "v-prefixed tags must be parseable")
	assert.Equal(t, "v1.100.0", got, "prefix preserved, and compared numerically not lexically")

	// Numeric, not lexical: "1.100.0" < "1.9.0" as strings.
	assert.NotEqual(t, "v1.9.0", got)
}

func TestRefsAtPrefixDepth(t *testing.T) {
	// Real shapes from aws/aws-sdk-go-v2: the s3 service module is tagged
	// `service/s3/vX.Y.Z`, but nested sub-modules like
	// `service/s3/internal/configtesting/vX.Y.Z` match the same textual prefix.
	// Only the single-segment remainder is a real service version.
	got := refsAtPrefixDepth([]string{
		"refs/tags/service/s3/v1.113.4",
		"refs/tags/service/s3/internal/configtesting/v0.1.0",
		"refs/tags/service/s3control/v1.0.0",
	}, "tags/service/s3/")

	assert.Equal(t, []string{"refs/tags/service/s3/v1.113.4"}, got,
		"nested sub-module tags must be discarded")

	// The core series: a hypothetical non-version tag beginning with v must not
	// survive either.
	got = refsAtPrefixDepth([]string{
		"refs/tags/v1.41.5",
		"refs/tags/v1.47.1",
		"refs/tags/validation/v1.0.0",
	}, "tags/v")

	assert.Equal(t, []string{"refs/tags/v1.41.5", "refs/tags/v1.47.1"}, got)
}

func TestModelURL(t *testing.T) {
	assert.Equal(t,
		"https://raw.githubusercontent.com/aws/aws-sdk-go-v2/v1.44.0/codegen/sdk-codegen/aws-models/route-53.json",
		modelURL("route-53", "route53", "v1.44.0", ""),
	)
	assert.Equal(t,
		"https://raw.githubusercontent.com/aws/aws-sdk-go-v2/service/docdb/v1.50.0/codegen/sdk-codegen/aws-models/docdb.json",
		modelURL("docdb", "docdb", "v1.44.0", "v1.50.0"),
	)
}

// failingTransport makes any HTTP attempt an immediate error.
type failingTransport struct{ t *testing.T }

func (f failingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.t.Errorf("unexpected network call to %s", req.URL)
	return nil, fmt.Errorf("network disabled in tests")
}

// withNoNetwork points modelHTTPClient at a transport that fails and errors the
// test on any attempt, restoring the original afterwards.
//
// Without this a cache-path bug does not fail the test, it silently reaches
// raw.githubusercontent.com — which is how an earlier revision of this test kept
// "passing" while making a live network call. Fail closed, not open.
func withNoNetwork(t *testing.T) {
	t.Helper()
	original := modelHTTPClient
	modelHTTPClient = &http.Client{Transport: failingTransport{t: t}}
	t.Cleanup(func() { modelHTTPClient = original })
}

func TestFetchModelUsesCache(t *testing.T) {
	withNoNetwork(t)

	cacheDir := t.TempDir()
	// Note the "core" segment: fetchModel qualifies the cache path by tag
	// series, so a path without it is a miss.
	cachePath := filepath.Join(cacheDir, "core", "demoservice", "v1.41.5", "demo-service.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(cachePath), 0o755))

	data, err := os.ReadFile("../../../testdata/smithy_basic.json")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cachePath, data, 0o644))

	// No network: a cache hit must satisfy this entirely.
	m, err := fetchModel(context.Background(), cacheDir, "demo-service", "demoservice", "v1.41.5", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"CreateWidget", "DeleteWidget"}, m.OperationNames())
}

func TestHTTPGet(t *testing.T) {
	// httpGet takes a plain URL, so unlike fetchModel it is testable without
	// changing production code — the model URL templates hardcode
	// raw.githubusercontent.com, so fetchModel's fetch path has no seam. This
	// covers the two branches that matter for an unattended job: a successful
	// body read, and the non-200 error naming both URL and status.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing.json" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"smithy":"2.0","shapes":{}}`))
	}))
	defer srv.Close()

	data, err := httpGet(context.Background(), srv.URL+"/model.json")
	require.NoError(t, err)
	assert.JSONEq(t, `{"smithy":"2.0","shapes":{}}`, string(data))

	_, err = httpGet(context.Background(), srv.URL+"/missing.json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404", "the status code must be reported")
	assert.Contains(t, err.Error(), "/missing.json", "the URL must be reported")
}

func subjects(findings []Finding, class FindingClass) []string {
	out := []string{}
	for _, f := range findings {
		if f.Class == class {
			out = append(out, f.Subject)
		}
	}
	sort.Strings(out)
	return out
}

func TestFindNewResources(t *testing.T) {
	baseline := loadTestModel(t, "smithy_basic.json")
	latest := loadTestModel(t, "smithy_latest.json")
	in, err := ReadControllerInputs("../../../testdata", "fake")
	require.NoError(t, err)

	got := findNewResources(latest, baseline, in)

	// Gizmo is new and has no CRD. Widget has a CRD. Session is in
	// ignore.resource_names.
	assert.Equal(t, []string{"Gizmo"}, subjects(got, ClassNewResource))
	require.Len(t, got, 1)
	assert.True(t, got[0].NewSincePin)
}

func TestFindNewOperations(t *testing.T) {
	baseline := loadTestModel(t, "smithy_basic.json")
	latest := loadTestModel(t, "smithy_latest.json")
	in, err := ReadControllerInputs("../../../testdata", "fake")
	require.NoError(t, err)

	got := findNewOperations(latest, baseline, in)

	// ListWidgets classifies (List, singularised to Widget) and the fake
	// controller never calls it. PutWidgetPolicy and ResetWidget do not
	// classify, so placeUnknownOp places them: PutWidgetPolicy is a sub-object of
	// Widget by name, and ResetWidget names Widget exactly. Nothing is left
	// unclassified.
	assert.Equal(t, []string{"ListWidgets", "PutWidgetPolicy", "ResetWidget"}, subjects(got, ClassNewOperation))
	assert.Empty(t, subjects(got, ClassUnknownOperation))

	// TagResource is denylisted. PutWidgetTagging is in ignore.operations.
	// AbortSession: "Abort" is an unrecognised verb, and its name mentions
	// `Session`, which the fixture's ignore.resource_names declines. This is the
	// only case that exercises namesIgnoredResource *through* findNewOperations —
	// CreateSession is excluded for an unrelated reason (no CRD) — so it is
	// checked against every reportable finding: placeUnknownOp would otherwise
	// happily put it somewhere. It is still listed, as dropped, with the reason.
	all := make([]string, 0, len(got))
	for _, f := range reportable(got) {
		all = append(all, f.Subject)
	}
	assert.NotContains(t, all, "AbortSession")
	assert.Contains(t, got, Finding{Class: ClassDroppedOperation, Subject: "AbortSession",
		Detail: "concerns `Session`, which generator.yaml ignores", NewSincePin: true})
	assert.NotContains(t, all, "TagResource")
	assert.NotContains(t, all, "PutWidgetTagging")

	for _, f := range got {
		if f.Class == ClassNewOperation {
			assert.Equal(t, "Widget", f.Kind,
				"must report the CRD's own casing")
		}
	}
}

func TestFindNewOperationsSkipsOperationsPredatingThePin(t *testing.T) {
	// The same model on both sides, so every operation predates the pin.
	// ListWidgets is still uncalled on a resource with a CRD, which is exactly
	// what the live sns run reported for ListTopics and friends: years-old
	// operations ACK declines by design, filed as an issue nobody can close by
	// regenerating. Only an operation new since the pin is news.
	latest := loadTestModel(t, "smithy_latest.json")
	in, err := ReadControllerInputs("../../../testdata", "fake")
	require.NoError(t, err)

	got := findNewOperations(latest, latest, in)
	assert.Empty(t, subjects(got, ClassNewOperation))
	assert.Empty(t, subjects(got, ClassUnknownOperation))
}

func TestNamesIgnoredResource(t *testing.T) {
	// s3's real ignore.resource_names. Every one of these operations would
	// otherwise sit in the unclassified bucket on every run forever, because
	// their verbs are unrecognised so they classify to OpTypeUnknown and carry
	// no usable resource name to filter on.
	ignored := []string{
		"Object", "MultipartUpload", "Session",
		"BucketMetadataTableConfiguration", "BucketMetadataConfiguration",
	}

	for _, op := range []string{
		"AbortMultipartUpload",
		"CopyObject",
		"PutObjectAcl",
		"RestoreObject",
		"SelectObjectContent",
		"WriteGetObjectResponse",
		"CreateSession",
	} {
		assert.True(t, namesIgnoredResource(op, ignored, nil),
			"%q concerns an ignored resource", op)
	}

	// Operations on resources the controller really does manage must survive.
	for _, op := range []string{"HeadBucket", "PutBucketPolicy", "ListBuckets"} {
		assert.False(t, namesIgnoredResource(op, ignored, nil),
			"%q concerns a managed resource and must still be reported", op)
	}

	// An empty ignore list must not swallow everything.
	assert.False(t, namesIgnoredResource("CopyObject", nil, nil))
	assert.False(t, namesIgnoredResource("CopyObject", []string{""}, nil))
}

func TestNamesIgnoredResourceSparesLongerKnownResources(t *testing.T) {
	// ec2 ignores the broad Ipam while the same report lists new resources whose
	// names contain it; their operations must not be hidden.
	ignored := []string{"Ipam"}
	known := map[string]string{
		"ipaminternetregistryassociation": "IpamInternetRegistryAssociation",
		"ipamroutingpolicyregistration":   "IpamRoutingPolicyRegistration",
	}
	assert.False(t, namesIgnoredResource("EnableIpamInternetRegistryAssociation", ignored, known))
	assert.False(t, namesIgnoredResource("BatchModifyIpamRoutingPolicyRegistrations", ignored, known))
	// An operation on Ipam itself is still ignored.
	assert.True(t, namesIgnoredResource("EnableIpamOrganizationAdminAccount", ignored, known))
	assert.True(t, namesIgnoredResource("EnableIpamInternetRegistryAssociation", ignored, nil))
}

func TestExposedInCRD(t *testing.T) {
	in, err := ReadControllerInputs("../../../testdata", "fake")
	require.NoError(t, err)

	// Direct case-insensitive match against the CRD.
	assert.True(t, exposedInCRD(in, "Widget", "Name"))
	assert.True(t, exposedInCRD(in, "Widget", "Config.Size"))
	assert.True(t, exposedInCRD(in, "Widget", "Rules.Prefix"))
	// Status field.
	assert.True(t, exposedInCRD(in, "Widget", "WidgetID"))

	// Renamed: generator.yaml maps WidgetName -> Name, and the CRD has "name".
	assert.True(t, exposedInCRD(in, "Widget", "WidgetName"))

	// Genuinely absent.
	assert.False(t, exposedInCRD(in, "Widget", "Description"))
	assert.False(t, exposedInCRD(in, "Widget", "Config.Color"))

	// Unknown kind.
	assert.False(t, exposedInCRD(in, "Gizmo", "Name"))
}

// declinedFieldModel has s3's CreateBucket and PutBucketVersioning shapes, plus an
// ec2-style response that reaches the CapacityReservation shape through a list.
func declinedFieldModel(t *testing.T) *SmithyModel {
	t.Helper()
	m, err := LoadSmithyModel([]byte(`{"shapes": {
		"demo#CreateBucket": {"type": "operation",
			"input": {"target": "demo#CreateBucketRequest"},
			"output": {"target": "demo#CreateBucketOutput"}},
		"demo#CreateBucketRequest": {"type": "structure", "members": {
			"CreateBucketConfiguration": {"target": "demo#CreateBucketConfiguration"},
			"BucketArn": {"target": "smithy.api#String"},
			"DryRun": {"target": "smithy.api#Boolean"}}},
		"demo#CreateBucketConfiguration": {"type": "structure", "members": {
			"Tags": {"target": "demo#TagSet"},
			"TagsExtra": {"target": "smithy.api#String"},
			"Location": {"target": "smithy.api#String"}}},
		"demo#TagSet": {"type": "list", "member": {"target": "demo#Tag"}},
		"demo#Tag": {"type": "structure", "members": {
			"Key": {"target": "smithy.api#String"},
			"Value": {"target": "smithy.api#String"}}},
		"demo#CreateBucketOutput": {"type": "structure", "members": {
			"Reservations": {"target": "demo#CapacityReservationList"},
			"DryRun": {"target": "smithy.api#Boolean"}}},
		"demo#CapacityReservationList": {"type": "list", "member": {"target": "demo#CapacityReservation"}},
		"demo#CapacityReservation": {"type": "structure", "members": {
			"CapacityBlockId": {"target": "smithy.api#String"},
			"Interruptible": {"target": "smithy.api#Boolean"}}},
		"demo#PutBucketVersioning": {"type": "operation",
			"input": {"target": "demo#PutBucketVersioningRequest"}},
		"demo#PutBucketVersioningRequest": {"type": "structure", "members": {
			"VersioningConfiguration": {"target": "demo#VersioningConfiguration"}}},
		"demo#VersioningConfiguration": {"type": "structure", "members": {
			"MFADelete": {"target": "smithy.api#String"},
			"Status": {"target": "smithy.api#String"}}}
	}}`))
	require.NoError(t, err)
	return m
}

func TestDeclinedFieldPath(t *testing.T) {
	m := declinedFieldModel(t)
	declined := func(op, path string, isOutput bool, entries ...string) bool {
		shape, ok := m.Operation(op)
		require.True(t, ok)
		ref := shape.Input
		if isOutput {
			ref = shape.Output
		}
		members := m.WalkMembers(ref.Target, maxWalkDepth)
		require.Contains(t, members, path)
		return declinedFieldPath(m, fieldRootNames(op, ref, isOutput), path, members, entries)
	}

	// s3's real entries, which name a shape and then a member inside it.
	s3 := []string{
		"VersioningConfiguration.MFADelete",
		"CreateBucketConfiguration.Tags",
	}
	assert.True(t, declined("CreateBucket", "CreateBucketConfiguration.Tags", false, s3...))
	// Descendants of a declined path are declined too — codegen emits neither.
	assert.True(t, declined("CreateBucket", "CreateBucketConfiguration.Tags.Key", false, s3...))
	assert.True(t, declined("PutBucketVersioning", "VersioningConfiguration.MFADelete", false, s3...))
	// Case-insensitive.
	assert.True(t, declined("PutBucketVersioning", "VersioningConfiguration.MFADelete", false,
		"versioningconfiguration.mfadelete"))

	// A sibling under the same parent must survive.
	assert.False(t, declined("CreateBucket", "CreateBucketConfiguration.Location", false, s3...))
	assert.False(t, declined("PutBucketVersioning", "VersioningConfiguration.Status", false, s3...))
	// A prefix collision must not suppress: TagsExtra is not Tags.
	assert.False(t, declined("CreateBucket", "CreateBucketConfiguration.TagsExtra", false, s3...))
	assert.False(t, declined("CreateBucket", "BucketArn", false))

	// ec2's entries are qualified by the root shape's codegen name, which the
	// relative path never carries, or by a nested shape reached through a list.
	assert.True(t, declined("CreateBucket", "DryRun", false, "CreateBucketInput.DryRun"))
	assert.True(t, declined("CreateBucket", "DryRun", false, "CreateBucketRequest.DryRun"))
	assert.True(t, declined("CreateBucket", "DryRun", true, "CreateBucketOutput.DryRun"))
	assert.True(t, declined("CreateBucket", "Reservations.CapacityBlockId", true,
		"CapacityReservation.CapacityBlockId"))
	// Qualified by the wrong side's root, or the wrong shape, it does not apply.
	assert.False(t, declined("CreateBucket", "DryRun", true, "CreateBucketInput.DryRun"))
	assert.False(t, declined("CreateBucket", "Reservations.Interruptible", true,
		"CapacityReservation.CapacityBlockId"))
	// A path relative to the operation is not an entry codegen understands.
	assert.False(t, declined("CreateBucket", "Reservations.CapacityBlockId", true,
		"Reservations.CapacityBlockId"))
}

func TestDeclinedShapeName(t *testing.T) {
	// s3 declares BlockedEncryptionTypes, and the real member's target is the
	// absolute ID com.amazonaws.s3#BlockedEncryptionTypes — only the short name
	// is compared.
	declined := []string{"BlockedEncryptionTypes", "Ignored"}

	assert.True(t, declinedShapeName("com.amazonaws.s3#BlockedEncryptionTypes", declined))
	assert.True(t, declinedShapeName("com.amazonaws.demo#ignored", declined),
		"comparison is case-insensitive")
	assert.False(t, declinedShapeName("com.amazonaws.s3#EncryptionRule", declined))
	assert.False(t, declinedShapeName("", declined))
	assert.False(t, declinedShapeName("com.amazonaws.s3#Anything", nil))
}

func TestDeclinedShapeAncestor(t *testing.T) {
	// Mirrors the real s3 case. Declining BlockedEncryptionTypes must also
	// remove what hangs beneath it, even though the child's own target
	// (EncryptionTypeList) is a different, undeclined shape. A self-only check
	// let that child through on the real delta.
	members := map[string]MemberInfo{
		"ServerSideEncryptionConfiguration": {
			Target: "com.amazonaws.s3#ServerSideEncryptionConfiguration",
		},
		"ServerSideEncryptionConfiguration.Rules": {
			Target: "com.amazonaws.s3#ServerSideEncryptionRules",
		},
		"ServerSideEncryptionConfiguration.Rules.BlockedEncryptionTypes": {
			Target: "com.amazonaws.s3#BlockedEncryptionTypes",
		},
		"ServerSideEncryptionConfiguration.Rules.BlockedEncryptionTypes.EncryptionType": {
			Target: "com.amazonaws.s3#EncryptionTypeList",
		},
		"ServerSideEncryptionConfiguration.Rules.ApplyServerSideEncryptionByDefault": {
			Target: "com.amazonaws.s3#ServerSideEncryptionByDefault",
		},
	}
	declined := []string{"BlockedEncryptionTypes"}

	assert.True(t, declinedShapeAncestor(
		"ServerSideEncryptionConfiguration.Rules.BlockedEncryptionTypes",
		members, declined))
	assert.True(t, declinedShapeAncestor(
		"ServerSideEncryptionConfiguration.Rules.BlockedEncryptionTypes.EncryptionType",
		members, declined),
		"a child of a declined shape is declined too")

	// A sibling under the same parent must survive.
	assert.False(t, declinedShapeAncestor(
		"ServerSideEncryptionConfiguration.Rules.ApplyServerSideEncryptionByDefault",
		members, declined))
	assert.False(t, declinedShapeAncestor(
		"ServerSideEncryptionConfiguration.Rules", members, declined))
	assert.False(t, declinedShapeAncestor("Anything", members, nil))
}

func TestSourcedAsCRDField(t *testing.T) {
	// s3's real shape: the CRD field `abac` is sourced from PutBucketAbac's
	// AbacStatus member, so AbacStatus is exposed despite no field of that name.
	in := &ControllerInputs{
		Config: &generatorConfig{
			Resources: map[string]resourceConfig{
				"Bucket": {
					Fields: map[string]resourceFieldConfig{
						"Abac": {From: &resourceFieldFrom{
							Operation: "PutBucketAbac",
							Path:      "AbacStatus",
						}},
					},
				},
			},
		},
	}

	assert.True(t, sourcedAsCRDField(in, "Bucket", "PutBucketAbac", "AbacStatus"))
	assert.True(t, sourcedAsCRDField(in, "Bucket", "PutBucketAbac", "AbacStatus.Status"),
		"sourcing a path brings its children")

	// The same path on a different operation is not covered by this declaration.
	assert.False(t, sourcedAsCRDField(in, "Bucket", "GetBucketAbac", "AbacStatus"))
	// A different path on the declared operation is not covered either.
	assert.False(t, sourcedAsCRDField(in, "Bucket", "PutBucketAbac", "ChecksumAlgorithm"))
	// Unknown kind.
	assert.False(t, sourcedAsCRDField(in, "Gizmo", "PutBucketAbac", "AbacStatus"))
}

func TestIsACKManagedARN(t *testing.T) {
	// ack-generate assigns a Create/List response's Arn or <Kind>Arn member to
	// Status.ACKResourceMetadata.ARN, which every ACK CRD exposes. Nothing in
	// generator.yaml declares this.
	assert.True(t, isACKManagedARN("Bucket", "BucketArn"))
	assert.True(t, isACKManagedARN("Bucket", "Buckets.BucketArn"))
	assert.True(t, isACKManagedARN("Bucket", "Arn"))
	assert.True(t, isACKManagedARN("Table", "TableArn"))

	// Deliberately narrow: only the final segment, only those two spellings.
	assert.False(t, isACKManagedARN("Bucket", "BucketArnPrefix"))
	assert.False(t, isACKManagedARN("Bucket", "BucketArn.Nested"))
	assert.False(t, isACKManagedARN("Bucket", "SourceArnOwner"))
}

// wrapperModel builds a model whose responses wrap the resource the way iam's and
// s3's do. withNewField adds SourceRoleTemplate to Role and DefaultEventHold to
// DefaultRetention, the two members that appeared live between v1.32.6/v1.41.5
// and v1.47.1.
func wrapperModel(t *testing.T, withNewField bool) *SmithyModel {
	t.Helper()
	role := `"RoleName": {"target": "smithy.api#String"}`
	retention := `"Days": {"target": "smithy.api#Integer"}`
	if withNewField {
		role += `, "SourceRoleTemplate": {"target": "demo#Template"}`
		retention += `, "DefaultEventHold": {"target": "demo#Hold"}`
	}
	m, err := LoadSmithyModel([]byte(`{"shapes": {
		"demo#GetRole": {"type": "operation", "output": {"target": "demo#GetRoleOut"}},
		"demo#CreateRole": {"type": "operation", "output": {"target": "demo#GetRoleOut"}},
		"demo#UpdateRole": {"type": "operation", "input": {"target": "demo#Role"}},
		"demo#GetRoleOut": {"type": "structure", "members": {"Role": {"target": "demo#Role"}}},
		"demo#GetGroup": {"type": "operation", "output": {"target": "demo#GetGroupOut"}},
		"demo#GetGroupOut": {"type": "structure", "members": {
			"Group": {"target": "demo#Role"}, "Marker": {"target": "smithy.api#String"}}},
		"demo#GetProfile": {"type": "operation", "output": {"target": "demo#GetProfileOut"}},
		"demo#GetProfileOut": {"type": "structure", "members": {"Profile": {"target": "demo#Profile"}}},
		"demo#Profile": {"type": "structure", "members": {
			"Name": {"target": "smithy.api#String"}, "Roles": {"target": "demo#RoleList"}}},
		"demo#RoleList": {"type": "list", "member": {"target": "demo#Role"}},
		"demo#Role": {"type": "structure", "members": {` + role + `}},
		"demo#Template": {"type": "structure", "members": {"TemplateArn": {"target": "smithy.api#String"}}},
		"demo#GetLock": {"type": "operation", "output": {"target": "demo#GetLockOut"}},
		"demo#GetLockOut": {"type": "structure", "members": {"ObjectLockConfiguration": {"target": "demo#Lock"}}},
		"demo#Lock": {"type": "structure", "members": {"Rule": {"target": "demo#Rule"}}},
		"demo#Rule": {"type": "structure", "members": {"DefaultRetention": {"target": "demo#Retention"}}},
		"demo#Retention": {"type": "structure", "members": {` + retention + `}},
		"demo#Hold": {"type": "structure", "members": {"Days": {"target": "smithy.api#Integer"}}}
	}}`))
	require.NoError(t, err)
	return m
}

func TestFindAddedFieldsFollowsCodegenOutputUnwrapping(t *testing.T) {
	in := &ControllerInputs{
		Config: &generatorConfig{Operations: map[string]operationOverride{
			// Configured wrapper on a response with two members, which the
			// single-member rule alone would not unwrap.
			"GetGroup": {OutputWrapperFieldPath: "Group"},
		}},
		CRDFields: map[string]map[string]bool{
			// Codegen unwrapped GetRoleOutput.Role, so its fields are top level.
			"Role":  {"rolename": true},
			"Group": {"rolename": true},
			// Models the profile but not the Roles list it returns.
			"Profile": {"name": true},
			// Models the whole single-member response as an ordinary field, as
			// s3's Bucket does with objectLockConfiguration.
			"Bucket": {
				"objectlockconfiguration":                            true,
				"objectlockconfiguration.rule":                       true,
				"objectlockconfiguration.rule.defaultretention":      true,
				"objectlockconfiguration.rule.defaultretention.days": true,
			},
		},
		UsedOps: map[string]map[string]bool{
			"role":    {"GetRole": true, "CreateRole": true, "UpdateRole": true},
			"group":   {"GetGroup": true},
			"profile": {"GetProfile": true},
			"bucket":  {"GetLock": true},
		},
	}

	// One finding per new field per resource: children of a new field (TemplateArn,
	// DefaultEventHold.Days) are part of it, and the operations it appeared in are
	// the Evidence rather than one finding each. Only returned, Bucket's and
	// Group's are Status candidates; Role's is also sent on CreateRole, so Spec.
	got := findAddedFields(wrapperModel(t, true), wrapperModel(t, false), in)
	assert.Equal(t, []Finding{
		{Kind: "Bucket", Class: ClassStatusField, NewSincePin: true,
			Subject: "ObjectLockConfiguration.Rule.DefaultRetention.DefaultEventHold", Evidence: "GetLock"},
		{Kind: "Group", Class: ClassStatusField, NewSincePin: true,
			Subject: "SourceRoleTemplate", Evidence: "GetGroup"},
		{Kind: "Role", Class: ClassSpecField, NewSincePin: true,
			Subject: "SourceRoleTemplate", Evidence: "CreateRole,GetRole,UpdateRole"},
	}, got,
		"Profile must report nothing: its CRD does not model Roles, so a new field "+
			"inside Roles cannot reach it; Bucket must still report, because its CRD "+
			"exposes the parent under the wrapped spelling")
}

func TestOutputWrapper(t *testing.T) {
	m := wrapperModel(t, false)
	in := &ControllerInputs{Config: &generatorConfig{Operations: map[string]operationOverride{
		"GetGroup": {OutputWrapperFieldPath: "Group"},
	}}}
	ref := func(target string) *SmithyMemberRef { return &SmithyMemberRef{Target: target} }

	assert.Equal(t, "Role", outputWrapper(m, in, "GetRole", ref("demo#GetRoleOut")),
		"a response whose only member is a structure is unwrapped")
	assert.Equal(t, "Group", outputWrapper(m, in, "GetGroup", ref("demo#GetGroupOut")),
		"the configured output_wrapper_field_path wins")
	assert.Equal(t, "", outputWrapper(m, in, "GetOther", ref("demo#GetGroupOut")),
		"two members and no configuration: read as-is")
	assert.Equal(t, "", outputWrapper(m, in, "GetOther", ref("demo#Retention")),
		"a single non-structure member is not a wrapper")
}

func TestFindAddedFields(t *testing.T) {
	baseline := loadTestModel(t, "smithy_basic.json")
	latest := loadTestModel(t, "smithy_latest.json")
	in, err := ReadControllerInputs("../../../testdata", "fake")
	require.NoError(t, err)

	got := findAddedFields(latest, baseline, in)

	// This asserts the whole suppression chain end-to-end, not just that the
	// producer runs. Every mechanism has a member in the fixture that only it
	// can suppress, because five of the six defects fixed in this task were
	// "mechanism X exists but is not wired into findAddedFields" — a failure the
	// per-predicate unit tests below cannot catch.
	//
	// Reported:
	//   Config.Color   new, nested, absent from the CRD
	//   Description    new, absent from the CRD
	//   WidgetArn      new on the *input* shape; the ARN convention applies only
	//                  to responses, so this must survive
	// Suppressed:
	//   WidgetName     renames -> Name, which the CRD exposes
	//   DeclinedField  ignore.field_paths
	//   IgnoredThing   ignore.shape_names, via its target shape `Ignored`
	//   IgnoredThing.Inner  descendant of a declined shape
	//   SourcedMember  resources.Widget.fields.Sourced.from
	//   WidgetArn      on the *output* shape, ACK's status-ARN wiring
	//
	// Config.Nested.Color is absent for an unrelated reason: the cycle guard
	// stops the walk descending into the self-referential WidgetConfig shape.
	assert.Equal(t, []string{
		"Config.Color",
		"Description",
		"WidgetArn",
	}, subjects(got, ClassSpecField))

	// Spelled out individually so a failure names the mechanism that broke.
	reported := subjects(got, ClassSpecField)
	assert.NotContains(t, reported, "DeclinedField",
		"ignore.field_paths must be honoured through findAddedFields")
	assert.NotContains(t, reported, "IgnoredThing",
		"ignore.shape_names must be honoured through findAddedFields")
	assert.NotContains(t, reported, "IgnoredThing.Inner",
		"a declined shape's descendants must be honoured through findAddedFields")
	assert.NotContains(t, reported, "SourcedMember",
		"fields.<N>.from must be honoured through findAddedFields")
	assert.NotContains(t, reported, "WidgetName",
		"renames must be honoured through findAddedFields")

	for _, f := range got {
		assert.Equal(t, "Widget", f.Kind)
	}
}

func sampleFindings() []Finding {
	return []Finding{
		{Kind: "Gizmo", Class: ClassNewResource, Subject: "Gizmo", Detail: "implied by `CreateGizmo`",
			NewSincePin: true, Evidence: "CreateGizmo,DeleteGizmo"},
		// As the producers emit them: no Detail, and NewSincePin always true, which
		// the renderer must not annotate on these classes.
		{Kind: "Widget", Class: ClassNewOperation, Subject: "PutWidgetPolicy", NewSincePin: true},
		{Kind: "Widget", Class: ClassSpecField, Subject: "Description", NewSincePin: true, Evidence: "CreateWidget"},
		{Class: ClassUnknownOperation, Subject: "ResetWidget", NewSincePin: true},
	}
}

func TestFingerprintStability(t *testing.T) {
	a := fingerprintFindings("demo", sampleFindings())
	b := fingerprintFindings("demo", sampleFindings())
	assert.Equal(t, a, b)
	assert.Len(t, a, 64)

	// Reordering the same findings must not change the fingerprint.
	reordered := sampleFindings()
	reordered[0], reordered[3] = reordered[3], reordered[0]
	assert.Equal(t, a, fingerprintFindings("demo", reordered))

	// A different Detail must not change it either — details carry version
	// strings and other prose.
	detailChanged := sampleFindings()
	detailChanged[0].Detail = "completely different prose"
	assert.Equal(t, a, fingerprintFindings("demo", detailChanged))

	// Evidence does change it: a resource gaining an operation, or a field turning
	// up in another one, is new information for the issue.
	evidenceChanged := sampleFindings()
	evidenceChanged[0].Evidence = "CreateGizmo,DeleteGizmo,DescribeGizmos"
	assert.NotEqual(t, a, fingerprintFindings("demo", evidenceChanged))

	// A genuinely different finding must change it.
	extra := append(sampleFindings(), Finding{Kind: "Widget", Class: ClassSpecField, Subject: "Extra", NewSincePin: true})
	assert.NotEqual(t, a, fingerprintFindings("demo", extra))
}

// TestFingerprintFormatLock pins the digest layout. Fingerprints live in open
// GitHub issue bodies indefinitely, so this constant is a wire format: if this
// test fails, either the layout changed deliberately — update the constant and
// accept that every open issue will be recreated once — or something changed it
// by accident.
func TestFingerprintFormatLock(t *testing.T) {
	assert.Equal(t,
		"6a33b980472badea0ddd5fc5ec52c1631722aedb28eb0076e953069a149ffaf2",
		fingerprintFindings("demo", sampleFindings()),
	)
}

func TestFingerprintScopedToService(t *testing.T) {
	// The community repo holds issues for every service, so the same finding set
	// under two services must not collide — including the empty set.
	assert.NotEqual(t,
		fingerprintFindings("s3", sampleFindings()),
		fingerprintFindings("ec2", sampleFindings()),
	)
	assert.NotEqual(t,
		fingerprintFindings("s3", nil),
		fingerprintFindings("ec2", nil),
	)
}

func TestFingerprintSensitivity(t *testing.T) {
	base := fingerprintFindings("demo", sampleFindings())

	kindChanged := sampleFindings()
	kindChanged[1].Kind = "Doodad"
	assert.NotEqual(t, base, fingerprintFindings("demo", kindChanged))

	classChanged := sampleFindings()
	classChanged[1].Class = ClassSpecField
	assert.NotEqual(t, base, fingerprintFindings("demo", classChanged))

	// NewSincePin is deliberately excluded; see fingerprintFindings.
	pinChanged := sampleFindings()
	pinChanged[0].NewSincePin = false
	assert.Equal(t, base, fingerprintFindings("demo", pinChanged))
}

func TestFingerprintDedupesFindings(t *testing.T) {
	// lambda really does emit the same (Kind, Class, Subject) twice, from the
	// Input and Output walks of one operation.
	doubled := append(sampleFindings(), sampleFindings()[2])
	assert.Equal(t,
		fingerprintFindings("demo", dedupeFindings(sampleFindings())),
		fingerprintFindings("demo", dedupeFindings(doubled)),
	)
}

func TestFindingClassString(t *testing.T) {
	// These names are hashed into the fingerprint, so they are pinned here too.
	assert.Equal(t, "new-resource", ClassNewResource.String())
	assert.Equal(t, "new-operation", ClassNewOperation.String())
	assert.Equal(t, "unknown-operation", ClassUnknownOperation.String())
	assert.Equal(t, "spec-field", ClassSpecField.String())
	assert.Equal(t, "possible-resource", ClassPossibleResource.String())
	assert.Equal(t, "dropped-operation", ClassDroppedOperation.String())
	assert.Equal(t, "status-field", ClassStatusField.String())
	assert.Equal(t, "lifecycle-field", ClassLifecycleField.String())
	assert.Equal(t, "dropped-field", ClassDroppedField.String())
	assert.Equal(t, "unnamed-class-10", FindingClass(10).String())
}

func TestRenderIssueBody(t *testing.T) {
	// An exact body, not a Contains chain: this pins section ordering, bullet
	// text, the footer, and the region markers that bound the whole lot together.
	want := "<!-- ack-api-change-begin -->\n" +
		"AWS SDK releases since v1.41.5, the version the `demo` controller builds against, add " +
		"resources and fields the controller does not represent. These are candidate additions for " +
		"maintainer review; not every item is necessarily appropriate for the CRD API.\n" +
		"\n" +
		"## Resource: Gizmo\n" +
		"\n" +
		"### New resource\n" +
		"- `Gizmo` — implied by `CreateGizmo`\n" +
		"  - Operations: `CreateGizmo`, `DeleteGizmo`\n" +
		"\n" +
		"## Resource: Widget\n" +
		"\n" +
		"### Spec field candidates\n" +
		"- `Description` — `CreateWidget`\n" +
		"\n" +
		"### Related new operations\n" +
		"- `PutWidgetPolicy`\n" +
		"\n" +
		"## Unattributed\n" +
		"\n" +
		"### Unclassified operations\n" +
		"- `ResetWidget`\n" +
		"\n" +
		"---\n" +
		"Compared aws-sdk-go-v2 v1.41.5 -> v1.44.0\n" +
		"<!-- ack-api-change-fingerprint: 6a33b980472badea0ddd5fc5ec52c1631722aedb28eb0076e953069a149ffaf2 -->\n" +
		"<!-- ack-api-change-end -->\n"

	body, fingerprint := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	assert.Equal(t, want, body)
	assert.Equal(t, fingerprintFindings("demo", sampleFindings()), fingerprint)
}

func TestRenderIssueBodyDedupes(t *testing.T) {
	doubled := append(sampleFindings(), sampleFindings()[2])
	plain, plainFingerprint := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	dupes, dupesFingerprint := renderIssueBody("demo", "v1.41.5", "v1.44.0", doubled)
	assert.Equal(t, plain, dupes)
	assert.Equal(t, plainFingerprint, dupesFingerprint)
}

func TestRenderIssueBodyOmitsEmptyResourceHeader(t *testing.T) {
	// A finding the renderer has no section for must not leave a bare
	// "## Resource:" header, and must still reach the reader somewhere.
	findings := []Finding{
		{Kind: "Widget", Class: ClassUnknownOperation, Subject: "ResetWidget", Detail: "needs review", NewSincePin: true},
	}
	body, _ := renderIssueBody("demo", "v1.41.5", "v1.44.0", findings)
	assert.NotContains(t, body, "## Resource: Widget\n\n---")
	assert.Contains(t, body, "`ResetWidget`")
}

func TestRenderIssueBodyRespectsGitHubLimit(t *testing.T) {
	// ec2 reaches 74% of the cap on a nine-version delta; a longer-stale
	// controller will exceed it, and GitHub answers 422.
	var findings []Finding
	for i := 0; i < 20000; i++ {
		findings = append(findings, Finding{
			Kind:        fmt.Sprintf("Kind%d", i),
			Class:       ClassSpecField,
			Subject:     fmt.Sprintf("CreateThing%d -> SomeReasonablyLongFieldPath%d", i, i),
			Detail:      "not present in the CRD",
			NewSincePin: true,
		})
	}
	body, fingerprint := renderIssueBody("demo", "v1.41.5", "v1.44.0", findings)

	assert.Less(t, len(body), githubMaxIssueBody)
	// A truncated body must still be worth reading.
	assert.Contains(t, body, "## Resource: ")
	assert.Contains(t, body, "### Spec field candidates")
	// Whole resources were dropped here, so the summary names both counts. The
	// wording is pinned because this is the only test that reaches this branch.
	assert.Contains(t, body, "further resource(s) and")
	assert.Contains(t, body, "finding(s) omitted")
	// The footer and the marker must survive truncation — the marker is the
	// lookup key for the next run.
	assert.Contains(t, body, "Compared aws-sdk-go-v2 v1.41.5 -> v1.44.0")
	assert.Equal(t, fingerprint, parseFingerprint(body))
	assert.Equal(t, fingerprintFindings("demo", dedupeFindings(findings)), fingerprint)
}

func TestRenderIssueBodyTruncatesInsideOneOversizedResource(t *testing.T) {
	// Every finding under a single Kind: the whole body is one block, so
	// skipping blocks wholesale would yield an issue with no findings in it —
	// and because the fingerprint covers the full set, Task 14 would report
	// issueUnchanged and never refresh it.
	var findings []Finding
	for i := 0; i < 20000; i++ {
		findings = append(findings, Finding{
			Kind:        "OneBigThing",
			Class:       ClassSpecField,
			Subject:     fmt.Sprintf("CreateThing -> SomeReasonablyLongFieldPath%d", i),
			Detail:      "not present in the CRD",
			NewSincePin: true,
		})
	}
	body, fingerprint := renderIssueBody("demo", "v1.41.5", "v1.44.0", findings)

	assert.Less(t, len(body), githubMaxIssueBody)
	assert.Contains(t, body, "## Resource: OneBigThing")
	assert.Contains(t, body, "### Spec field candidates")
	assert.Contains(t, body, "SomeReasonablyLongFieldPath0")
	assert.Contains(t, body, "omitted")
	assert.Equal(t, fingerprint, parseFingerprint(body))
	// Half the budget or better should be used — a body that bails out early is
	// the bug this test exists to catch. The budget is asked for rather than
	// hardcoded because it is derived from the footer these versions produce.
	budget := issueBodyBudget("v1.41.5", "v1.44.0", fingerprint, 1, len(findings))
	assert.Greater(t, len(body), budget/2)
	// No resource was dropped here, only entries inside one that rendered, so the
	// summary must not report a resource count at all.
	assert.NotContains(t, body, "further resource(s)")
	assert.Contains(t, body, "further finding(s) omitted")
}

func TestRenderIssueBodyDoesNotStarveLaterResources(t *testing.T) {
	// A block too large to fit must not suppress a small one after it.
	var findings []Finding
	for i := 0; i < 20000; i++ {
		findings = append(findings, Finding{
			Kind:        "AAABigKind",
			Class:       ClassSpecField,
			Subject:     fmt.Sprintf("CreateThing -> SomeReasonablyLongFieldPath%d", i),
			Detail:      "not present in the CRD",
			NewSincePin: true,
		})
	}
	findings = append(findings, Finding{
		Kind:        "ZZZTinyKind",
		Class:       ClassSpecField,
		Subject:     "CreateTiny -> Name",
		Detail:      "not present in the CRD",
		NewSincePin: true,
	})
	body, _ := renderIssueBody("demo", "v1.41.5", "v1.44.0", findings)

	assert.Contains(t, body, "## Resource: AAABigKind")
	assert.Contains(t, body, "## Resource: ZZZTinyKind")
	assert.Less(t, len(body), githubMaxIssueBody)
}

func TestParseFingerprint(t *testing.T) {
	body, fingerprint := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	assert.Equal(t, fingerprintFindings("demo", sampleFindings()), parseFingerprint(body))
	assert.Equal(t, fingerprint, parseFingerprint(body))
	assert.Equal(t, "", parseFingerprint("a body with no marker"))
}

func TestParseFingerprintReadsTheLiveRegion(t *testing.T) {
	// renderIssueBody's region starts at byte 0, so the first region is the live
	// one and anything below it is human text. A maintainer quoting another
	// service's complete report — markers and all — must not redirect the read;
	// under last-region selection it did, and the next edit then overwrote the
	// quote with this service's report.
	live, liveFP := renderIssueBody("s3", "v1.41.5", "v1.44.0", sampleFindings())
	quoted, quotedFP := renderIssueBody("ec2", "v1.41.5", "v1.44.0", sampleFindings())
	require.NotEqual(t, liveFP, quotedFP, "the two regions must be distinguishable")

	body := live + "\n\nQuoting the ec2 issue for comparison:\n\n" + quoted
	assert.Equal(t, liveFP, parseFingerprint(body))

	// A bare marker line outside the region is not the live one either, wherever it
	// sits. These two pass under both selection rules, so they are kept as
	// regression cover rather than as the discriminator.
	stale := "<!-- ack-api-change-fingerprint: " + strings.Repeat("0", 64) + " -->\n"
	assert.Equal(t, liveFP, parseFingerprint(stale+live))
	assert.Equal(t, liveFP, parseFingerprint(live+"\nQuoting #7:\n\n"+stale))
}

func TestParseFingerprintReadsAFingerprintlessRegionAsHumanText(t *testing.T) {
	// A maintainer explaining what the bot emits appends a complete but empty
	// region — the marker pair in a fenced code block. Under last-region selection
	// parseFingerprint read that region, returned "", and listAPIChangeIssues then
	// skipped the issue: a duplicate got filed and the orphan never counted against
	// the cap.
	live, liveFP := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	empty := "\n\nFor reference, the bot wraps its report like this:\n\n```\n" +
		generatedRegionBegin + "\n...\n" + generatedRegionEnd + "\n```\n"

	require.NotEmpty(t, liveFP)
	assert.Equal(t, liveFP, parseFingerprint(live+empty))
}

func TestParseFingerprintIgnoresAStrayBeginMarker(t *testing.T) {
	// A trailing bare begin marker used to make the whole body look regionless.
	// parseFingerprint survived that on its whole-body fallback, which is exactly
	// what made the matching replaceGeneratedRegion failure silent: the issue stayed
	// attributed, so the wipe fired the next time the findings changed.
	live, liveFP := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	assert.Equal(t, liveFP,
		parseFingerprint(live+"\n> Note: intentional.\n\n"+generatedRegionBegin+"\n"))
}

func TestParseFingerprintToleratesCRLF(t *testing.T) {
	// GitHub stores a web-UI-edited body with CRLF line endings. The region markers
	// go through the same normalisation as the fingerprint line, so both the region
	// lookup and the marker match have to survive it.
	body, fingerprint := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	crlf := strings.ReplaceAll(body, "\n", "\r\n")
	require.NotEmpty(t, fingerprint)
	assert.Equal(t, fingerprint, parseFingerprint(crlf))
}

func TestParseFingerprintRejectsInlineMarker(t *testing.T) {
	// The pattern is line-anchored, so prose that merely mentions a marker is
	// not mistaken for one — inside a region as much as outside it.
	inline := "see <!-- ack-api-change-fingerprint: " + strings.Repeat("a", 64) + " --> inline"
	assert.Equal(t, "", parseFingerprint(inline))
	assert.Equal(t, "", parseFingerprint(
		generatedRegionBegin+"\n"+inline+"\n"+generatedRegionEnd+"\n"))
}

func TestReplaceGeneratedRegionKeepsHumanText(t *testing.T) {
	// The ownership checks establish that the detector filed the issue, not that
	// nobody has edited it since. A whole-body PATCH silently deleted a
	// maintainer's "this divergence is intentional" note — and the accompanying
	// comment notified every subscriber of the loss.
	region, _ := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	note := "\n> Note: `CreateGizmo` is intentionally unsupported.\n"
	existing := "Filed by the detector.\n\n" + region + note

	refreshed, refreshedFingerprint := renderIssueBody("demo", "v1.44.0", "v1.45.0", sampleFindings()[:2])
	got := replaceGeneratedRegion(existing, refreshed)

	assert.True(t, strings.HasPrefix(got, "Filed by the detector.\n\n"))
	assert.True(t, strings.HasSuffix(got, note))
	assert.Contains(t, got, "Compared aws-sdk-go-v2 v1.44.0 -> v1.45.0")
	assert.NotContains(t, got, "Compared aws-sdk-go-v2 v1.41.5 -> v1.44.0")
	assert.Equal(t, refreshedFingerprint, parseFingerprint(got))

	// Rewriting with an unchanged region must be a no-op, or the body grows a
	// stray line every run.
	assert.Equal(t, existing, replaceGeneratedRegion(existing, region))
}

func TestReplaceGeneratedRegionSurvivesAStrayBeginMarker(t *testing.T) {
	// A maintainer mid-edit, or one quoting the begin marker, leaves a body of
	// `complete region + note + bare begin marker`. Taking the last begin marker
	// found no end after it, so the body looked regionless and the fallback replaced
	// all of it — deleting the note and then commenting about the change, which is
	// the exact failure the region exists to prevent. parseFingerprint's own
	// whole-body fallback still resolved the issue, so nothing made it visible.
	region, _ := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	tail := "\n> Note: `CreateGizmo` is intentionally unsupported.\n\n" + generatedRegionBegin + "\n"
	existing := region + tail

	refreshed, refreshedFingerprint := renderIssueBody("demo", "v1.44.0", "v1.45.0", sampleFindings()[:2])
	got := replaceGeneratedRegion(existing, refreshed)

	assert.True(t, strings.HasSuffix(got, tail), "the note and the stray marker must survive")
	assert.Contains(t, got, "Compared aws-sdk-go-v2 v1.44.0 -> v1.45.0")
	assert.NotContains(t, got, "Compared aws-sdk-go-v2 v1.41.5 -> v1.44.0")
	assert.Equal(t, refreshedFingerprint, parseFingerprint(got))
}

func TestReplaceGeneratedRegionRewritesTheLiveRegion(t *testing.T) {
	// A maintainer quoting another service's complete report below the live one.
	// Taking the last region rewrote the quote with this service's report and left
	// the live region at the top of the issue permanently stale.
	live, _ := renderIssueBody("s3", "v1.41.5", "v1.44.0", sampleFindings())
	quoted, quotedFingerprint := renderIssueBody("ec2", "v1.41.5", "v1.44.0", sampleFindings())
	quote := "\n\nQuoting the ec2 issue for comparison:\n\n" + quoted
	existing := live + quote

	refreshed, refreshedFingerprint := renderIssueBody("s3", "v1.44.0", "v1.45.0", sampleFindings()[:2])
	got := replaceGeneratedRegion(existing, refreshed)

	assert.True(t, strings.HasSuffix(got, quote), "the quoted ec2 region must be left alone")
	assert.True(t, strings.HasPrefix(got, refreshed),
		"the live region is the one that gets refreshed")
	assert.Equal(t, refreshedFingerprint, parseFingerprint(got))
	assert.NotEqual(t, quotedFingerprint, parseFingerprint(got))
}

func TestReplaceGeneratedRegionWithoutARegion(t *testing.T) {
	// Issues filed before the markers existed have nothing to preserve, so they are
	// replaced whole. Doing anything else would leave them un-refreshable.
	region, _ := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	assert.Equal(t, region, replaceGeneratedRegion("an older generated body\n", region))

	// Half a marker pair is a maintainer mid-edit or a quote, not a region.
	assert.Equal(t, region,
		replaceGeneratedRegion(generatedRegionBegin+"\nno end marker\n", region))
}

// issueFor builds the shape listAPIChangeIssues hands to reconcileIssue.
func issueFor(number int, body string) *github.Issue {
	return &github.Issue{Number: github.Int(number), Body: github.String(body)}
}

func TestReconcileIssueCreatesWhenAbsent(t *testing.T) {
	var created bool
	var sentLabels []string
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/o/community/issues" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var payload struct{ Labels []string }
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		sentLabels = payload.Labels
		created = true
		// Echo the labels back, the way the real API does on success.
		// createGithubIssueWithClient refuses an issue that came back without
		// apiChangeLabel, so a bare `{"number": 1}` here would trip that guard
		// instead of exercising the creation path it protects.
		filed := github.Issue{Number: github.Int(1)}
		for _, name := range payload.Labels {
			filed.Labels = append(filed.Labels, &github.Label{Name: github.String(name)})
		}
		require.NoError(t, json.NewEncoder(w).Encode(filed))
	}))

	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", sampleFindings(), nil, nil, 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueCreated, outcome)
	assert.True(t, created)

	// Both labels matter. Without apiChangeLabel the next run's listing cannot
	// see this issue and files a duplicate every day; kind/api-change is what
	// humans filter by.
	assert.Contains(t, sentLabels, apiChangeLabel)
	assert.Contains(t, sentLabels, "kind/api-change")
	assert.Contains(t, sentLabels, "service/demo")
}

func TestReconcileIssueNoopWhenFingerprintMatches(t *testing.T) {
	body, _ := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no write or search expected, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))

	// A different baseline version must not trigger an update, because the
	// fingerprint covers findings only.
	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.42.0", "v1.45.0", sampleFindings(), issueFor(42, body), nil, 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueUnchanged, outcome)
}

func TestReconcileIssueUpdatesWhenFingerprintDiffers(t *testing.T) {
	var calls []string
	var commentBody string
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/o/community/issues/42":
			calls = append(calls, "patch")
			fmt.Fprint(w, `{}`)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/community/issues/42/comments":
			calls = append(calls, "comment")
			var payload struct{ Body string }
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			commentBody = payload.Body
			fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))

	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", sampleFindings(), issueFor(42, "stale body"), nil, 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueUpdated, outcome)

	// The order is asserted, not just the pair. The PATCH writes the new
	// fingerprint, which is also the done-marker, so commenting afterwards means a
	// failed comment is never retried — see
	// TestReconcileIssueFailedCommentLeavesTheFingerprintStale.
	assert.Equal(t, []string{"comment", "patch"}, calls)

	// The comment text is asserted because reverting it to the old wording left the
	// whole suite green. It is the only message that notifies humans, on a public
	// repo, so both halves of the fix are pinned: the service and both versions must
	// be there, and the old "New AWS API changes detected" must not be — that claimed
	// a direction the fingerprint cannot know, and was a false alarm every time
	// findings had in fact been *resolved*.
	assert.Contains(t, commentBody, "`demo`")
	assert.Contains(t, commentBody, "v1.41.5")
	assert.Contains(t, commentBody, "v1.44.0")
	assert.NotContains(t, commentBody, "New AWS API changes detected")
	// Future tense: the PATCH can still fail after this posts, so the comment must
	// not read as a refresh that already landed.
	assert.Contains(t, commentBody, "will be refreshed")
}

func TestReconcileIssueFailedPatchReportsNoOutcome(t *testing.T) {
	// The comment lands, then the PATCH fails. GitHub has been written to and the
	// function still failed, so the outcome must be no-decision: a caller drives
	// bookkeeping off the outcome, and issueUpdated here would record a refresh that
	// never happened while subscribers had already been notified. Mutating this return
	// to issueUpdated previously passed the whole suite — this is the one path that can
	// have already written before failing, so nothing else holds the contract.
	var calls []string
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			calls = append(calls, "comment")
			fmt.Fprint(w, `{}`)
		case r.Method == http.MethodPatch:
			calls = append(calls, "patch")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message": "Forbidden"}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))

	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", sampleFindings(), issueFor(42, "stale body"), nil, 10, 0, false)
	require.Error(t, err)
	assert.Equal(t, issueOutcomeNone, outcome)
	assert.Equal(t, []string{"comment", "patch"}, calls)
}

func TestReconcileIssueRefusesAnOversizedMergedBody(t *testing.T) {
	// A merged body over the limit 422s every run, so the fingerprint never advances
	// and the update path retries daily — notifying every time, since the comment
	// precedes the PATCH. Probed: five consecutive PATCH failures, five comments. It
	// must fail before the comment, not after.
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a body that cannot be PATCHed must not notify first, got %s %s",
			r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))

	// The existing body needs a real generated region, not just prose plus a big note:
	// replaceGeneratedRegion falls back to whole-body replacement when it finds no
	// region, which discards the note and so can never overflow. The overflow only
	// exists when there is a region to keep text *around* — which is exactly the case
	// renderIssueBody's own budget cannot see.
	region, _ := renderIssueBody("demo", "v1.40.0", "v1.41.5", sampleFindings()[:1])
	existingBody := region + "\n\n" + strings.Repeat("x", githubMaxIssueBody)

	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", sampleFindings(), issueFor(42, existingBody), nil, 10, 0, false)
	require.Error(t, err)
	assert.ErrorContains(t, err, "over GitHub's")
	assert.Equal(t, issueOutcomeNone, outcome)
}

func TestReconcileIssueCapBlocksCreationOnly(t *testing.T) {
	// At the cap with no existing issue: creation is blocked, and no request is
	// made at all.
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("cap should have blocked %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))

	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", sampleFindings(), nil, nil, 10, 10, false)
	require.NoError(t, err)
	assert.Equal(t, issueSkippedAtCap, outcome)
}

func TestReconcileIssueCapStillAllowsUpdates(t *testing.T) {
	var patched bool
	// Strict about unexpected requests, as its four siblings are: a creation at the
	// cap would otherwise be served a bland `{}` here and pass.
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/o/community/issues/42":
			patched = true
			fmt.Fprint(w, `{}`)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/community/issues/42/comments":
			fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))

	// At the cap, but the issue already exists — updating does not grow the
	// count, so it must proceed.
	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", sampleFindings(), issueFor(42, "stale body"), nil, 10, 10, false)
	require.NoError(t, err)
	assert.Equal(t, issueUpdated, outcome)
	assert.True(t, patched)
}

func TestReconcileIssueNoFindings(t *testing.T) {
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no findings must mean no API calls, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))

	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", nil, nil, nil, 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueUnchanged, outcome)
}

func TestReconcileIssueNoFindingsButAnOpenIssue(t *testing.T) {
	// A controller that has caught up. Declining to write is the deliberate choice —
	// an unattended job must not retract a report off the absence of evidence, since
	// a detector bug or an empty model fetch looks exactly like this — but the open
	// issue then asserts changes that no longer exist and holds a cap slot, so the
	// outcome has to be distinguishable from a service with simply nothing to say.
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no findings must mean no API calls, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))

	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", nil, issueFor(42, "a report with no current findings"),
		nil, 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueStaleOpenIssue, outcome)
}

func TestReconcileIssueUpdatePreservesMaintainerText(t *testing.T) {
	// Proven necessary by mutation: with this untested, replacing
	// replaceGeneratedRegion's output with the raw rendered body passed the whole
	// suite, which is the regression that silently deletes a maintainer's notes and
	// then comments announcing it.
	region, _ := renderIssueBody("demo", "v1.40.0", "v1.41.5", sampleFindings()[:1])
	existingBody := region + "\n\n`CreateBucketMetadataTableConfiguration` is intentionally unsupported.\n"

	var patchedBody string
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			var payload struct{ Body string }
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			patchedBody = payload.Body
		}
		fmt.Fprint(w, `{}`)
	}))

	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", sampleFindings(), issueFor(42, existingBody), nil, 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueUpdated, outcome)
	assert.Contains(t, patchedBody, "intentionally unsupported")
	// The refreshed region really did land: "### Spec field candidates" is absent from the
	// one-finding body this issue started with.
	assert.Contains(t, patchedBody, "### Spec field candidates")
}

func TestReconcileIssueFailedCommentLeavesTheFingerprintStale(t *testing.T) {
	// The Critical this ordering exists for. Day 1's comment fails; because the body
	// has not been patched, the fingerprint is still the old one, so day 2 sees a
	// mismatch and retries. With the PATCH first, day 2 matched and returned
	// issueUnchanged with zero requests — the notification lost for ever after one
	// transient 403, on a job nobody watches.
	existing := issueFor(42, "stale body")

	var dayOneCalls []string
	dayOne := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dayOneCalls = append(dayOneCalls, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments") {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message": "Resource not accessible by integration"}`)
			return
		}
		fmt.Fprint(w, `{}`)
	}))

	outcome, err := reconcileIssue(context.Background(), dayOne, "o", "community",
		"demo", "v1.41.5", "v1.44.0", sampleFindings(), existing, nil, 10, 0, false)
	require.Error(t, err)
	// No outcome at all, so a caller cannot record a decision that did not happen.
	assert.Equal(t, issueOutcomeNone, outcome)
	assert.Equal(t, []string{"POST /repos/o/community/issues/42/comments"}, dayOneCalls,
		"the body must not be patched once the notification has failed")

	var dayTwoCalls []string
	dayTwo := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dayTwoCalls = append(dayTwoCalls, r.Method+" "+r.URL.Path)
		fmt.Fprint(w, `{}`)
	}))

	// Same issue, unchanged because day 1 never wrote to it.
	outcome, err = reconcileIssue(context.Background(), dayTwo, "o", "community",
		"demo", "v1.41.5", "v1.44.0", sampleFindings(), existing, nil, 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueUpdated, outcome)
	assert.Equal(t, []string{
		"POST /repos/o/community/issues/42/comments",
		"PATCH /repos/o/community/issues/42",
	}, dayTwoCalls, "the next run must retry the notification, not short-circuit")
}

func TestReconcileIssueCreateWithoutTheOwnershipLabelAborts(t *testing.T) {
	// GitHub drops the labels on creation when the token's account lacks push
	// access, and the check for that runs after the POST — so an issue now exists on
	// the public repo. Reporting issueUnchanged here asserted the opposite of what
	// happened, and the error had to be prose a caller could only match on by
	// substring, so the ordinary log-and-continue loop filed one invisible orphan per
	// service per day.
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"number": 7, "labels": []}`)
	}))

	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", sampleFindings(), nil, nil, 10, 0, false)
	require.Error(t, err)
	assert.Equal(t, issueOutcomeNone, outcome)
	assert.True(t, errors.Is(err, errCannotLabelIssues),
		"the caller must be able to tell a credential problem from a per-service one: %v", err)
	assert.Contains(t, err.Error(), "#7", "the orphan's number belongs in the error")
}

func TestReconcileIssueRejectsANonPositiveCap(t *testing.T) {
	// A cap of 0 used to mean "unlimited", so a flag defaulting to 0 or a caller
	// forgetting to pass one removed the only brake on a writer to a public repo
	// across ~74 services. Fail closed instead; an absent cap is set deliberately
	// large.
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a bad cap must be refused before any request, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))

	for _, maxOpen := range []int{0, -1} {
		outcome, err := reconcileIssue(context.Background(), client, "o", "community",
			"demo", "v1.41.5", "v1.44.0", sampleFindings(), nil, nil, maxOpen, 100000, false)
		require.Error(t, err, "maxOpen=%d", maxOpen)
		assert.Equal(t, issueOutcomeNone, outcome)
		assert.Contains(t, err.Error(), "must be positive")
	}

	// Refused even with nothing to file, so the misconfiguration surfaces on the
	// first service rather than on whichever one happens to have findings.
	_, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", nil, nil, nil, 0, 0, false)
	require.Error(t, err)
}

func TestReconcileIssueSuppressedByAClosedIssue(t *testing.T) {
	// Closing is how a maintainer says "I have seen this set and it needs no
	// issue". Without this the run re-filed the same fingerprint daily.
	_, want := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a suppressed finding set must make no request, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))

	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", sampleFindings(), nil,
		map[string]bool{want: true}, 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueSuppressedByClosed, outcome)
}

func TestReconcileIssueOpenIssueBeatsAClosedFingerprint(t *testing.T) {
	// A service with both takes the update path: the open issue is the live one.
	_, want := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	var patched bool
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			patched = true
		}
		fmt.Fprint(w, `{}`)
	}))

	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", sampleFindings(), issueFor(42, "stale body"),
		map[string]bool{want: true}, 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueUpdated, outcome)
	assert.True(t, patched)
}

func TestIssueOutcomeString(t *testing.T) {
	// The job's only output is a log, so `outcome=4` is not something a reader can
	// act on. Every member is covered, the zero value included: that is the one a
	// caller sees alongside an error.
	assert.Equal(t, "no-decision", issueOutcomeNone.String())
	assert.Equal(t, "suppressed-by-closed-issue", issueSuppressedByClosed.String())
	assert.Equal(t, "unchanged", issueUnchanged.String())
	assert.Equal(t, "created", issueCreated.String())
	assert.Equal(t, "updated", issueUpdated.String())
	assert.Equal(t, "skipped-at-cap", issueSkippedAtCap.String())
	assert.Equal(t, "stale-open-issue", issueStaleOpenIssue.String())

	// A member added without a case here still logs something traceable rather than
	// an empty string.
	assert.Equal(t, "unnamed-outcome-99", issueOutcome(99).String())
}

func TestIssueOutcomeValuesAreDistinct(t *testing.T) {
	// The values used to be spread over two const blocks continuing one another's
	// iota by hand, where adding a member to the first block aliased two decisions
	// with nothing for the compiler to object to. They are literals now; this pins
	// that nothing collides regardless.
	seen := map[issueOutcome]bool{}
	for _, o := range []issueOutcome{
		issueOutcomeNone, issueSuppressedByClosed, issueUnchanged,
		issueCreated, issueUpdated, issueSkippedAtCap, issueStaleOpenIssue,
	} {
		assert.False(t, seen[o], "%d is used by two outcomes", int(o))
		seen[o] = true
	}
	// Zero must stay not-a-decision, so that an error's outcome cannot read as one.
	assert.Equal(t, issueOutcome(0), issueOutcomeNone)
}

func TestGetAPINotificationConfig(t *testing.T) {
	services, configuredCap, err := getAPINotificationServices("../../../jobs_config.yaml")
	require.NoError(t, err)
	// Membership and order, not just non-nil: the order of this list decides which
	// services win when the cap allows fewer issues than there are candidates, so it
	// is load-bearing rather than incidental.
	assert.Equal(t, []string{"s3"}, services)
	// Returned only so the command can warn when the flag disagrees with it.
	assert.Equal(t, 1, configuredCap)
}

func TestGetAPINotificationConfigRejectsUnusableConfigs(t *testing.T) {
	// Every case here aborts a run before any GitHub request, and every one of them is
	// reachable from a hand-edited jobs_config.yaml handed to the job through extra_refs
	// or the jobs-config ConfigMap — `make prow-gen` is not on that path.
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
		return path
	}

	for _, tt := range []struct {
		name string
		path string
		want string
	}{
		{
			name: "missing file",
			path: filepath.Join(dir, "does-not-exist.yaml"),
			want: "unable to read",
		},
		{
			name: "malformed yaml",
			path: write("malformed.yaml", "aws_services: [s3\n"),
			want: "unable to unmarshal",
		},
		{
			name: "comments only",
			// yaml.Unmarshal leaves the pointer nil rather than erroring, so without the
			// nil guard this dereferences and panics the job.
			path: write("comments.yaml", "# nothing but a comment\n"),
			want: "parsed to nothing",
		},
		{
			name: "service outside aws_services",
			path: write("unknown-service.yaml",
				"aws_services:\n- s3\napi_notification_services:\n- bogussvc\n"),
			want: "not in aws_services",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			services, configuredCap, err := getAPINotificationServices(tt.path)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.Nil(t, services)
			assert.Zero(t, configuredCap)
		})
	}
}

func TestLatestVersionCacheMemoisesPerTagSeries(t *testing.T) {
	// Every service resolves its own per-service series, core-pinned or not: the
	// newest core tag can trail the newest model for a service. A repeat question
	// for the same series is answered from the cache.
	var listings []string
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		listings = append(listings, r.URL.Path)
		ref := "refs/tags/v1.44.0"
		switch {
		case strings.Contains(r.URL.Path, "service/s3/"):
			ref = "refs/tags/service/s3/v1.80.0"
		case strings.Contains(r.URL.Path, "service/ec2/"):
			ref = "refs/tags/service/ec2/v1.300.0"
		}
		fmt.Fprintf(w, `[{"ref": %q}]`, ref)
	}))

	cache := newLatestVersionCache()
	for range 2 {
		got, err := cache.resolve(context.Background(), client, "s3")
		require.NoError(t, err)
		assert.Equal(t, "v1.80.0", got)
	}
	assert.Len(t, listings, 1, "one series is listed once")

	got, err := cache.resolve(context.Background(), client, "ec2")
	require.NoError(t, err)
	assert.Equal(t, "v1.300.0", got)
	assert.Len(t, listings, 2)
	for _, path := range listings {
		assert.Contains(t, path, "tags/service/", "the core series is never consulted")
	}
}

func TestReconcileServicesAbortsWhenIssuesCannotBeLabelled(t *testing.T) {
	// The request count is the assertion that matters: it pins that services 2 and 3
	// were never attempted, which is the actual harm. errors.Is alone does not.
	logged := captureLog(t)
	var creates int
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		creates++
		// A token without push access silently drops labels.
		fmt.Fprint(w, `{"number": 1, "labels": []}`)
	}))
	analyze := func(_ context.Context, service string) ([]Finding, string, string, error) {
		return sampleFindings(), "v1.41.5", "v1.44.0", nil
	}

	_, _, _, err := reconcileServices(context.Background(), client, "o", "community",
		[]string{"svc1", "svc2", "svc3"}, nil, nil, 10, 0, analyze, false, "")
	require.Error(t, err)
	assert.ErrorIs(t, err, errCannotLabelIssues)
	assert.Equal(t, 1, creates, "the run must stop at the first service, not file an orphan per service")

	// The tally must say how far the run got. Reporting the whole list against all-zero
	// counts reads as a run that did nothing rather than one that stopped.
	assert.Contains(t, logged.String(), "1 of 3 services (run stopped early)")
}

// captureLog redirects the standard logger into a buffer for one test.
//
// Needed because this job's output *is* its log: nobody watches it run, and an
// outcome it decides but never prints is indistinguishable from one it never reached.
// So for reconcileServices a log line is the observable behaviour, not a side effect —
// issueStaleOpenIssue in particular writes nothing and returns nothing, and the line
// is the whole point of the outcome existing.
//
// The writer is saved and restored like the flags and the prefix, rather than restored to
// os.Stderr: hardcoding the destination means an inner capture's cleanup silently
// redirects an outer or nested one to stderr, and the outer test then asserts against a
// buffer nothing is written to any more.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	writer, flags, prefix := log.Writer(), log.Flags(), log.Prefix()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(writer)
		log.SetFlags(flags)
		log.SetPrefix(prefix)
	})
	return &buf
}

func TestReconcileServicesCountsIssuesItCreates(t *testing.T) {
	// reconcileIssue never mutates openCount, so without the caller's increment every
	// service in one run creates past the cap. This is the only possible detector.
	logged := captureLog(t)
	var creates int
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		creates++
		fmt.Fprintf(w, `{"number": %d, "labels": [{"name": %q}]}`, creates, apiChangeLabel)
	}))
	analyze := func(_ context.Context, service string) ([]Finding, string, string, error) {
		return sampleFindings(), "v1.41.5", "v1.44.0", nil
	}

	_, _, skipped, err := reconcileServices(context.Background(), client, "o", "community",
		[]string{"svc1", "svc2", "svc3"}, nil, nil, 2, 0, analyze, false, "")
	require.NoError(t, err)
	assert.Equal(t, 2, creates, "the cap must stop the third create")
	assert.Equal(t, []string{"svc3"}, skipped)

	// The closing tally is the highest-value line in the log of a job nobody watches,
	// and it is the only place the per-outcome counts appear at all. The buckets sum to
	// the 3 services attempted, which is the property that makes the line trustworthy.
	assert.Contains(t, logged.String(),
		"3 services: 2 created, 0 updated, 0 unchanged, 0 stale, 0 suppressed, "+
			"1 skipped at cap, 0 analysis failures, 0 write failures, 0 aborted")
}

func TestReconcileServicesReportsAStaleOpenIssue(t *testing.T) {
	// A service with no findings but an open issue must not be short-circuited before
	// reconcileIssue: that made issueStaleOpenIssue unreachable and its cap slot leak
	// silently, logging "no changes" — byte-identical to a service that never had
	// anything to report — while every other service was refused at a cap of 1.
	logged := captureLog(t)
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a stale open issue must not be written to, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	analyze := func(_ context.Context, service string) ([]Finding, string, string, error) {
		return nil, "v1.41.5", "v1.44.0", nil
	}

	existing := map[string]*github.Issue{"svc1": issueFor(42, "stale body")}
	analysisFailures, writeFailures, skipped, err := reconcileServices(
		context.Background(), client, "o", "community",
		[]string{"svc1"}, existing, nil, 10, 1, analyze, false, "")
	require.NoError(t, err)
	assert.Empty(t, analysisFailures)
	assert.Empty(t, writeFailures)
	assert.Empty(t, skipped)

	// Nothing is written and nothing is returned, so the log line is the entire output
	// of this outcome — assert it, or the case is dead again the moment somebody
	// reinstates the short-circuit.
	assert.Contains(t, logged.String(), "svc1: open issue has no current findings")
	assert.Contains(t, logged.String(), "holding a cap slot")
	assert.NotContains(t, logged.String(), "svc1: no changes")
	assert.Contains(t, logged.String(), "1 stale")
}

func droppedFindings() []Finding {
	return []Finding{
		{Class: ClassDroppedOperation, Subject: "StartFlowCapture", Detail: dropAction, NewSincePin: true},
		{Class: ClassDroppedOperation, Subject: "SearchVectors", Detail: dropQuery, NewSincePin: true},
	}
}

func TestRenderIssueBodyListsDroppedOperations(t *testing.T) {
	plain, plainFingerprint := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	body, fingerprint := renderIssueBody("demo", "v1.41.5", "v1.44.0",
		append(sampleFindings(), droppedFindings()...))

	// Collapsed, sorted, one line per operation with its reason, just above the
	// footer so it never displaces a finding.
	assert.Contains(t, body, "\n<details>\n<summary>2 new operations not reported, each with the reason</summary>\n\n"+
		"- `SearchVectors` — "+dropQuery+"\n"+
		"- `StartFlowCapture` — "+dropAction+"\n"+
		"\n</details>\n\n---\nCompared aws-sdk-go-v2")
	// Shown, never counted: the body is otherwise identical and the fingerprint
	// does not move, so a newly dropped operation cannot trigger a refresh.
	assert.Equal(t, plain, strings.Replace(body, droppedBlock(droppedFindings()), "", 1))
	assert.Equal(t, plainFingerprint, fingerprint)
	assert.NotContains(t, body, "## Unattributed\n\n### Unclassified operations\n- `StartFlowCapture`",
		"a dropped operation must not fall through to the catch-all")
}

func TestRenderIssueBodyListsDroppedFields(t *testing.T) {
	plain, plainFingerprint := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	fields := []Finding{
		{Kind: "Instance", Class: ClassDroppedField, Subject: "IncludeManagedResources",
			Detail: dropReadOption, NewSincePin: true, Evidence: "DescribeInstances"},
	}
	all := append(append(sampleFindings(), droppedFindings()[0]), fields...)
	body, fingerprint := renderIssueBody("demo", "v1.41.5", "v1.44.0", all)

	assert.Contains(t, body, "<summary>1 new operation and 1 new field not reported, each with the reason</summary>\n\n"+
		"- `StartFlowCapture` — "+dropAction+"\n"+
		"- `IncludeManagedResources` on Instance, from `DescribeInstances` — "+dropReadOption+"\n")
	assert.Equal(t, plainFingerprint, fingerprint, "a dropped field is shown, never counted")
	assert.Equal(t, plain, strings.Replace(body, droppedBlock(all), "", 1))
}

func TestFieldRolesClass(t *testing.T) {
	type use struct {
		op       OpType
		isOutput bool
	}
	for _, tc := range []struct {
		name string
		uses []use
		want FindingClass
	}{
		{"sent at Create", []use{{OpTypeCreate, false}}, ClassSpecField},
		{"sent at Create and returned", []use{{OpTypeCreate, false}, {OpTypeGet, true}}, ClassSpecField},
		{"sent at Update and returned", []use{{OpTypeUpdate, false}, {OpTypeList, true}}, ClassSpecField},
		{"set by a hook's Put and read back", []use{{OpTypeUnknown, false}, {OpTypeGet, true}}, ClassSpecField},
		{"only returned", []use{{OpTypeCreate, true}, {OpTypeGet, true}}, ClassStatusField},
		{"returned, and filtered on", []use{{OpTypeList, false}, {OpTypeList, true}}, ClassStatusField},
		// ec2's AcceptModificationTerms, QuoteId, ApplyCancellationCharges.
		{"sent at Update only", []use{{OpTypeUpdate, false}}, ClassLifecycleField},
		{"sent at Update and Delete", []use{{OpTypeUpdate, false}, {OpTypeDelete, false}}, ClassLifecycleField},
		{"sent at Delete only", []use{{OpTypeDelete, false}}, ClassLifecycleField},
		// ec2's IncludeManagedResources.
		{"sent on a read only", []use{{OpTypeList, false}}, ClassDroppedField},
		{"sent on reads only", []use{{OpTypeGet, false}, {OpTypeGetAttributes, false}}, ClassDroppedField},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &fieldRoles{}
			for _, u := range tc.uses {
				r.add(u.op, u.isOutput)
			}
			assert.Equal(t, tc.want, r.class())
		})
	}
}

func TestFoldOperationsIntoResources(t *testing.T) {
	got := foldOperationsIntoResources([]Finding{
		{Kind: "Gizmo", Class: ClassNewResource, Subject: "Gizmo", Evidence: "CreateGizmo,DeleteGizmo"},
		{Kind: "Gizmo", Class: ClassNewOperation, Subject: "AcceptGizmo"},
		{Kind: "Asset", Class: ClassPossibleResource, Subject: "Asset", Evidence: "PutAsset"},
		{Kind: "Asset", Class: ClassNewOperation, Subject: "ArchiveAsset"},
		// Widget has a CRD, so its new operation stays a finding of its own.
		{Kind: "Widget", Class: ClassNewOperation, Subject: "PutWidgetPolicy"},
	})
	assert.Equal(t, []Finding{
		{Kind: "Gizmo", Class: ClassNewResource, Subject: "Gizmo", Evidence: "AcceptGizmo,CreateGizmo,DeleteGizmo"},
		{Kind: "Asset", Class: ClassPossibleResource, Subject: "Asset", Evidence: "ArchiveAsset,PutAsset"},
		{Kind: "Widget", Class: ClassNewOperation, Subject: "PutWidgetPolicy"},
	}, got)
}

func TestRenderIssueBodySeparatesPreexistingFindings(t *testing.T) {
	plain, plainFingerprint := renderIssueBody("demo", "service/demo/v1.290.1", "service/demo/v1.338.1", sampleFindings())
	old := []Finding{
		{Kind: "SecondaryNetwork", Class: ClassNewResource, Subject: "SecondaryNetwork",
			Detail: "implied by `CreateSecondaryNetwork`", Evidence: "CreateSecondaryNetwork,DeleteSecondaryNetwork"},
		{Kind: "Instance", Class: ClassSpecField, Subject: "SecondaryInterfaces", Evidence: "RunInstances"},
	}
	body, fingerprint := renderIssueBody("demo", "service/demo/v1.290.1", "service/demo/v1.338.1",
		append(sampleFindings(), old...))

	assert.Contains(t, body, "<details>\n<summary>2 candidates already in service/demo/v1.290.1 and missing from the controller</summary>\n\n"+
		"These predate the SDK release the controller builds against, so they do not drive this notification.\n\n"+
		"- Instance Spec field `SecondaryInterfaces` — `RunInstances`\n"+
		"- resource `SecondaryNetwork` — `CreateSecondaryNetwork`, `DeleteSecondaryNetwork`\n")
	assert.NotContains(t, body, "## Resource: SecondaryNetwork")
	assert.Equal(t, plainFingerprint, fingerprint, "pre-existing findings do not drive the notification")
	assert.Equal(t, plain, strings.Replace(body, preexistingBlock(append(sampleFindings(), old...), "service/demo/v1.290.1"), "", 1))
	assert.Empty(t, reportable(old))
}

func TestMarkPreexisting(t *testing.T) {
	// The release model has CreateGizmo, the field Name on CreateWidget, and the
	// operation ResetWidget; everything else is new in latest.
	release := opsModel(t, map[string][]string{
		"CreateGizmo":  {"Name"},
		"CreateWidget": {"Name"},
		"ResetWidget":  nil,
	})
	in := &ControllerInputs{Config: &generatorConfig{}, kindsByLower: map[string]string{}}
	got := markPreexisting([]Finding{
		{Kind: "Gizmo", Class: ClassNewResource, Subject: "Gizmo", NewSincePin: true},
		{Kind: "Doodad", Class: ClassNewResource, Subject: "Doodad", NewSincePin: true},
		{Kind: "Widget", Class: ClassSpecField, Subject: "Name", NewSincePin: true, Evidence: "CreateWidget"},
		{Kind: "Widget", Class: ClassSpecField, Subject: "Color", NewSincePin: true, Evidence: "CreateWidget"},
		{Kind: "Widget", Class: ClassNewOperation, Subject: "ResetWidget", NewSincePin: true},
		{Kind: "Widget", Class: ClassNewOperation, Subject: "PolishWidget", NewSincePin: true},
		{Kind: "Asset", Class: ClassPossibleResource, Subject: "Asset", NewSincePin: true, Evidence: "PutAsset"},
		// Its generic read is old; only its own operations decide.
		{Kind: "Gadget", Class: ClassPossibleResource, Subject: "Gadget", NewSincePin: true, Evidence: "AcceptGadget,ResetWidget"},
	}, release, in)
	var stillNew []string
	for _, f := range got {
		if f.NewSincePin {
			stillNew = append(stillNew, f.Subject)
		}
	}
	assert.Equal(t, []string{"Doodad", "Color", "PolishWidget", "Asset", "Gadget"}, stillNew)
	assert.Len(t, markPreexisting(got, nil, in), len(got), "no release model leaves findings alone")
}

func TestReconcileIssueFilesNothingForDroppedOperationsAlone(t *testing.T) {
	// A service whose only change is a new Start* operation has nothing to report.
	// A nil client panics on any request, which is the assertion that none is made.
	outcome, err := reconcileIssue(context.Background(), nil, "o", "community", "demo",
		"v1.41.5", "v1.44.0", droppedFindings(), nil, nil, 1, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueUnchanged, outcome)
}

func TestReconcileServicesTreatsDroppedOperationsAloneAsNoChanges(t *testing.T) {
	logged := captureLog(t)
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("nothing to report, so nothing may be written, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	analyze := func(_ context.Context, service string) ([]Finding, string, string, error) {
		return droppedFindings(), "v1.41.5", "v1.44.0", nil
	}
	outputDir := t.TempDir()

	_, _, _, err := reconcileServices(context.Background(), client, "o", "community",
		[]string{"svc1"}, nil, nil, 1, 0, analyze, true, outputDir)
	require.NoError(t, err)
	assert.Contains(t, logged.String(), "svc1: no changes")
	assert.NotContains(t, logged.String(), "svc1: 2 findings")
	assert.NoFileExists(t, filepath.Join(outputDir, "svc1.md"), "no issue, so no preview of one")
}

func TestRunErrorReportsEveryReasonAtOnce(t *testing.T) {
	// Prow surfaces one error as the job's failure reason and it is the last line of the
	// log, so a run where the cap binds *and* controllers fail — the normal state of a
	// rollout — must not report only the cap. Nor may the abort swallow the failures
	// collected before it.
	assert.NoError(t, runError(nil, 1, nil, nil, nil, false))

	// Pinned whole rather than reason by reason, because the order is itself the finding:
	// Prow's deck truncates the failure description, the cap reason is red by design every
	// day of a rollout, and it used to come first and spend the visible prefix on the one
	// reason that is expected. Failures first, cap last, and its remediation advice last of
	// all. Every reason stays asserted — pinning all of them is what makes deleting any one
	// of them fail here.
	err := runError(nil, 1, []string{"svc1"}, []string{"svc2"}, []string{"svc3"}, false)
	require.Error(t, err)
	assert.Equal(t,
		"failed to analyze services: [svc2]; "+
			"failed to file or refresh issues for: [svc3]; "+
			"open-issue cap of 1 reached; no issue filed for: [svc1]. "+
			"Work down the backlog or raise api_notification_max_open_issues",
		err.Error())

	// All four reasons at once. The abort stays first because it is the only one that means
	// the run did not finish.
	abort := fmt.Errorf("aborting after svc1: %w", errCannotLabelIssues)
	err = runError(abort, 1, []string{"svc4"}, []string{"svc2"}, []string{"svc3"}, false)
	require.Error(t, err)
	assert.Equal(t,
		"aborting after svc1: issues cannot be labelled with ack/api-change-detected; also "+
			"failed to analyze services: [svc2]; "+
			"failed to file or refresh issues for: [svc3]; "+
			"open-issue cap of 1 reached; no issue filed for: [svc4]. "+
			"Work down the backlog or raise api_notification_max_open_issues",
		err.Error())
	// The sentinel must survive the fold, since it is how a caller or a test tells this
	// apart from a per-service failure.
	assert.ErrorIs(t, err, errCannotLabelIssues)

	// An abort carrying failures still reports them; an abort alone is returned untouched.
	err = runError(abort, 1, nil, []string{"svc2"}, nil, false)
	require.Error(t, err)
	assert.ErrorIs(t, err, errCannotLabelIssues)
	assert.Equal(t,
		"aborting after svc1: issues cannot be labelled with ack/api-change-detected; also "+
			"failed to analyze services: [svc2]",
		err.Error())
	assert.Equal(t, abort, runError(abort, 1, nil, nil, nil, false))

	// The remediation sentence belongs to the cap and must not trail a run where only the
	// analysis failed.
	err = runError(nil, 1, nil, []string{"svc2"}, nil, false)
	require.Error(t, err)
	assert.Equal(t, "failed to analyze services: [svc2]", err.Error())
}

func TestReconcileServicesSummaryReportsEveryAbortPosition(t *testing.T) {
	// The summary inferred "did we stop early?" from attempted < len(services), but
	// attempted is already incremented for the aborting service — so an abort on the *last*
	// service reported full scope, omitted that the run stopped, and said "0 write
	// failures", while an orphaned unlabelled issue had just been filed on the public
	// community repo. The aborting service also landed in no bucket at all, leaving the
	// tally one short of the services attempted.
	for _, abortAt := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("abort on service %d of 3", abortAt), func(t *testing.T) {
			logged := captureLog(t)
			var creates int
			client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				creates++
				if creates == abortAt {
					// A token without push access silently drops labels.
					fmt.Fprintf(w, `{"number": %d, "labels": []}`, creates)
					return
				}
				fmt.Fprintf(w, `{"number": %d, "labels": [{"name": %q}]}`, creates, apiChangeLabel)
			}))
			analyze := func(_ context.Context, service string) ([]Finding, string, string, error) {
				return sampleFindings(), "v1.41.5", "v1.44.0", nil
			}

			_, _, _, err := reconcileServices(context.Background(), client, "o", "community",
				[]string{"svc1", "svc2", "svc3"}, nil, nil, 10, 0, analyze, false, "")
			require.ErrorIs(t, err, errCannotLabelIssues)
			assert.Equal(t, abortAt, creates, "the run must stop at the aborting service")

			// Two assertions in one line: the scope names how far the run got, and the
			// buckets — abortAt-1 created plus the one aborted — sum to the services
			// attempted, on the first, middle and last position alike.
			assert.Contains(t, logged.String(), fmt.Sprintf(
				"%d of 3 services (run stopped early): %d created, 0 updated, 0 unchanged, "+
					"0 stale, 0 suppressed, 0 skipped at cap, 0 analysis failures, "+
					"0 write failures, 1 aborted",
				abortAt, abortAt-1))
		})
	}
}

func TestLatestVersionCacheMemoisesFailures(t *testing.T) {
	// The likeliest persistent failure of this listing is a rate-limit or
	// secondary-rate-limit 403, and issuing 73 more listings after the first one has
	// established the series cannot be read is actively counterproductive — it restores the
	// whole request cost the cache exists to remove, in the one case where request budget
	// matters most. Caching the error is not caching a wrong answer: no version is invented,
	// and each service still records its own analysis failure.
	var listings int
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		listings++
		w.WriteHeader(http.StatusInternalServerError)
	}))

	cache := newLatestVersionCache()
	version, err := cache.resolve(context.Background(), client, "s3")
	require.Error(t, err)
	assert.Empty(t, version)

	version, cachedErr := cache.resolve(context.Background(), client, "s3")
	require.Error(t, cachedErr)
	assert.Empty(t, version)
	assert.Equal(t, err, cachedErr, "the second ask of the series must still get the error")
	assert.Equal(t, 1, listings, "one upstream request per tag series, error or not")
}

func TestDetectAPIChangesRejectsNonPositiveCap(t *testing.T) {
	// The guard exists so one config typo costs one message instead of a full run of
	// analysis followed by N misleading per-service errors. It survived mutation until
	// this test existed.
	//
	// captureLog even though nothing here reads the log: detectAPIChanges' first
	// statement is log.SetPrefix, so without this the prefix leaks into every test that
	// runs after this one. Nothing asserts on it today only because every log assertion
	// in this package is an unanchored Contains — an anchored one would fail depending
	// on test order, which -shuffle=on varies.
	_ = captureLog(t)

	defer func(p string, c int) { OptJobsConfigPath, OptMaxOpenIssues = p, c }(
		OptJobsConfigPath, OptMaxOpenIssues)
	OptJobsConfigPath, OptMaxOpenIssues = "../../../jobs_config.yaml", 0

	err := detectAPIChanges(nil, nil)
	require.ErrorContains(t, err, "--max-open-issues must be positive")
}

func TestDetectAPIChangesWarnsWhenTheCapDisagreesWithTheConfig(t *testing.T) {
	// Flag and config are both 1 today, so the drift is invisible; when a maintainer raises
	// api_notification_max_open_issues — the only one of the two values a human edits — the
	// job keeps filing under the flag's cap and logs "cap of 1 reached" against a config
	// that says otherwise, with no diagnostic.
	defer func(p string, c int) { OptJobsConfigPath, OptMaxOpenIssues = p, c }(
		OptJobsConfigPath, OptMaxOpenIssues)
	OptJobsConfigPath, OptMaxOpenIssues = "../../../jobs_config.yaml", 5
	// Cleared so that a machine which happens to have a token in its environment cannot
	// carry this run past newGithubClientFromEnv and into api.github.com.
	t.Setenv("GITHUB_TOKEN", "")

	logged := captureLog(t)
	// The warning is logged before the client is built, so what comes back is the
	// missing-token error. That is deliberate and irrelevant to the assertion below — do not
	// "fix" it into a NoError, which would require a real GitHub call.
	require.Error(t, detectAPIChanges(nil, nil))

	assert.Contains(t, logged.String(),
		"WARNING --max-open-issues is 5 but api_notification_max_open_issues is 1")
}

// lastTallyLine returns the last closing-tally line in a captured log, which is the
// line reconcileServices defers. Matched on " services: " because that separator is
// common to both modes' wording, and returning the *last* match is what makes this
// usable in a test that captures two runs into one buffer.
func lastTallyLine(logged string) string {
	last := ""
	for _, line := range strings.Split(logged, "\n") {
		if strings.Contains(line, " services: ") {
			last = line
		}
	}
	return last
}

// tallyNumbers extracts every integer from a tally line, in order. The two modes word
// the buckets differently — "1 created" against "1 would create" — so the numbers are
// the only part of the line on which the two can be compared.
func tallyNumbers(line string) []int {
	var out []int
	for i := 0; i < len(line); {
		if line[i] < '0' || line[i] > '9' {
			i++
			continue
		}
		n := 0
		for i < len(line) && line[i] >= '0' && line[i] <= '9' {
			n = n*10 + int(line[i]-'0')
			i++
		}
		out = append(out, n)
	}
	return out
}

// dryRunAnalyzer is a fixture with enough shape to drive every reconcile branch.
func dryRunAnalyzer(findingsFor map[string][]Finding) serviceAnalyzer {
	return func(_ context.Context, service string) ([]Finding, string, string, error) {
		return findingsFor[service], "v1.41.5", "v1.44.0", nil
	}
}

func TestReconcileServicesDryRunMakesNoWrites(t *testing.T) {
	// The whole contract. A dry run that writes is worse than no dry run, because
	// somebody will trust it before pointing the job at a public repo.
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("dry run must not write, got %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, `{}`)
	}))

	analyze := dryRunAnalyzer(map[string][]Finding{
		"svc1": sampleFindings(), // would create
		"svc2": sampleFindings(), // would update (has an existing issue)
		"svc3": nil,              // stale: existing issue, no findings
	})
	existing := map[string]*github.Issue{
		"svc2": issueFor(42, "stale body"),
		"svc3": issueFor(43, "whatever"),
	}

	analysisFailures, writeFailures, skipped, err := reconcileServices(
		context.Background(), client, "o", "community",
		[]string{"svc1", "svc2", "svc3"}, existing, nil, 10, 0, analyze, true, "")
	require.NoError(t, err)
	assert.Empty(t, analysisFailures)
	assert.Empty(t, writeFailures)
	assert.Empty(t, skipped)
}

// dryRunEveryOutcomeFixture is one service per issueOutcome, plus the two the loop
// decides without reaching reconcileIssue at all. Every invariant that makes a preview
// worth reading is a decision *some* service in here depends on, which is the point: a
// fixture reaching only create, update, stale and cap lets four of them be gated on
// dryRun without a test noticing.
//
// The order is load-bearing. svccreate comes first so that it takes the single cap slot,
// which is what leaves svccapped to be refused; reversing them would still tally 1
// created and 1 skipped and would stop testing that the cap binds in run order.
func dryRunEveryOutcomeFixture() (
	services []string,
	existing map[string]*github.Issue,
	closed map[string]map[string]bool,
	analyze serviceAnalyzer,
) {
	services = []string{
		"svccreate", "svcupdate", "svcunchanged", "svcstale",
		"svcnothing", "svcsuppressed", "svcoversized", "svccapped",
	}

	// The body reconcileIssue would render for svcunchanged, used as that service's
	// existing issue body so the *embedded* fingerprint matches. Recomputing
	// fingerprintFindings here instead would not do: renderIssueBody dedupes before
	// hashing. Get this wrong and the service lands in `update`, the pinned tally
	// changes, and the fingerprint-comparison mutation survives again.
	unchangedBody, _ := renderIssueBody("svcunchanged", "v1.41.5", "v1.44.0", sampleFindings())

	// A real generated region plus a maintainer note too large to merge back. It needs
	// the region: replaceGeneratedRegion falls back to whole-body replacement when it
	// finds none, which discards the note and so can never overflow. A different
	// finding subset, so the region's fingerprint does not match and the run reaches
	// the size guard rather than short-circuiting to unchanged.
	oversizedRegion, _ := renderIssueBody("svcoversized", "v1.40.0", "v1.41.5", sampleFindings()[:1])
	oversizedBody := oversizedRegion + "\n\n" + strings.Repeat("x", githubMaxIssueBody)

	existing = map[string]*github.Issue{
		"svcupdate":    issueFor(42, "stale body"),
		"svcunchanged": issueFor(43, unchangedBody),
		"svcstale":     issueFor(44, "whatever"),
		"svcoversized": issueFor(45, oversizedBody),
	}

	_, suppressedFingerprint := renderIssueBody("svcsuppressed", "v1.41.5", "v1.44.0", sampleFindings())
	closed = map[string]map[string]bool{
		"svcsuppressed": {suppressedFingerprint: true},
	}

	analyze = dryRunAnalyzer(map[string][]Finding{
		"svccreate":     sampleFindings(),
		"svcupdate":     sampleFindings(),
		"svcunchanged":  sampleFindings(),
		"svcstale":      nil,
		"svcnothing":    nil,
		"svcsuppressed": sampleFindings(),
		"svcoversized":  sampleFindings(),
		"svccapped":     sampleFindings(),
	})
	return services, existing, closed, analyze
}

func TestReconcileServicesDryRunReachesTheSameDecisions(t *testing.T) {
	// A preview that disagrees with the real run is misinformation. Run both over
	// identical inputs, across every outcome the reconcile can reach, and compare
	// everything the summary and the caller are built from.
	services, existing, closed, analyze := dryRunEveryOutcomeFixture()

	liveClient := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues") {
			fmt.Fprintf(w, `{"number": 1, "labels": [{"name": %q}]}`, apiChangeLabel)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	dryClient := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("dry run must not write, got %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{}`)
	}))

	// Cap of 1: svccreate takes the slot, svccapped is refused. The cap must bind
	// identically in both modes, which it only does if every decision ahead of it does.
	liveLog := captureLog(t)
	liveAnalysis, liveWrites, liveSkipped, liveErr := reconcileServices(
		context.Background(), liveClient, "o", "community",
		services, existing, closed, 1, 0, analyze, false, "")
	liveSummary := lastTallyLine(liveLog.String())

	dryLog := captureLog(t)
	dryAnalysis, dryWrites, drySkipped, dryErr := reconcileServices(
		context.Background(), dryClient, "o", "community",
		services, existing, closed, 1, 0, analyze, true, "")
	drySummary := lastTallyLine(dryLog.String())

	require.NoError(t, liveErr)
	require.NoError(t, dryErr)

	// All three returned slices, not just the cap one. writeFailures is where the
	// oversized-merge guard lands, so gating that guard — or replaceGeneratedRegion,
	// which is what makes the merged body oversized in the first place — on dryRun
	// shows up here and nowhere else.
	assert.Equal(t, liveSkipped, drySkipped, "the cap must bind identically")
	assert.Equal(t, liveWrites, dryWrites, "the oversize guard must fire identically")
	assert.Equal(t, liveAnalysis, dryAnalysis)
	assert.Equal(t, []string{"svccapped"}, liveSkipped)
	assert.Equal(t, []string{"svcoversized"}, liveWrites)

	// Pinned against the decisions this fixture implies, and not only against each
	// other. Comparing the two tallies alone is vacuous the moment either line goes
	// missing — two empty lines are equal, and two empty number slices are equal —
	// and a dry run that reaches no decisions at all is exactly the regression this
	// test exists for. 8 services: svccreate created, svcupdate refreshed,
	// svcunchanged and svcnothing unchanged, svcstale stale, svcsuppressed
	// suppressed, svccapped refused at the cap, svcoversized a write failure. The
	// buckets sum to the 8 attempted.
	require.NotEmpty(t, liveSummary, "no tally line in the live log")
	require.NotEmpty(t, drySummary, "no tally line in the dry-run log")
	assert.Equal(t, []int{8, 1, 1, 2, 1, 1, 1, 0, 1, 0}, tallyNumbers(liveSummary))
	// The dry summary says "would" where the live one says what it did, so compare the
	// numbers rather than the prose.
	assert.Equal(t, tallyNumbers(liveSummary), tallyNumbers(drySummary))

	// The wording, asserted on the tally line itself rather than on the whole log:
	// dryRunCaveat also begins "DRY RUN ", so a whole-log Contains for that prefix
	// passes even with the prefix stripped from the tally — it cannot fail, and the
	// tally is the line somebody quotes.
	assert.Contains(t, drySummary, "DRY RUN ")
	assert.Contains(t, drySummary, "1 would create")
	assert.Contains(t, drySummary, "1 would update")
	assert.NotContains(t, liveSummary, "DRY RUN")
	assert.Contains(t, liveSummary, "1 created")
	assert.Contains(t, liveSummary, "1 updated")

	// The per-service lines, which are the ones a reader quotes back as evidence the
	// job filed something. A dry run must claim nothing in the past tense.
	assert.Contains(t, dryLog.String(), "svccreate: would file a new issue")
	assert.Contains(t, dryLog.String(), "svcupdate: would refresh the existing issue")
	assert.NotContains(t, dryLog.String(), ": issue created")
	assert.NotContains(t, dryLog.String(), ": issue updated")
	assert.Contains(t, liveLog.String(), "svccreate: issue created")
	assert.Contains(t, liveLog.String(), "svcupdate: issue updated")

	// The fixture is only worth its pinned tally if each service really takes the branch
	// it is named for, and the tally alone cannot show that: svcunchanged landing in
	// `update` and svcnothing landing in `unchanged` would move one number between two
	// buckets that the same total still adds up for. These lines are also the only
	// evidence for the four outcomes that write nothing at all. Asserted against both
	// logs, since none of these five is worded differently by mode.
	for _, log := range []string{liveLog.String(), dryLog.String()} {
		assert.Contains(t, log, "svcunchanged: issue already up to date")
		assert.Contains(t, log, "svcstale: open issue has no current findings")
		assert.Contains(t, log, "svcnothing: no changes")
		assert.Contains(t, log, "svcsuppressed: suppressed, a closed issue already covers")
		assert.Contains(t, log, "svccapped: SKIPPED, open-issue cap of 1 reached")
		assert.Contains(t, log, "ERROR svcoversized: refreshing issue o/community#45 would produce a ")
	}
}

func TestDryRunStillRejectsANonPositiveCap(t *testing.T) {
	// The cap rejection is a decision, so a preview must reach the same verdict a real
	// run would: gating it on dryRun makes `--max-open-issues 0 --dry-run` report a
	// clean preview of a configuration a real run refuses outright. Guarded in two
	// places, and both are mutable independently — detectAPIChanges rejects it before
	// any analysis, reconcileIssue rejects it per service — so both are asserted.
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a bad cap must be refused before any request, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	for _, maxOpen := range []int{0, -1} {
		outcome, err := reconcileIssue(context.Background(), client, "o", "community",
			"demo", "v1.41.5", "v1.44.0", sampleFindings(), nil, nil, maxOpen, 0, true)
		require.Error(t, err, "maxOpen=%d under dry-run", maxOpen)
		assert.Equal(t, issueOutcomeNone, outcome)
		assert.Contains(t, err.Error(), "must be positive")
	}

	// See TestDetectAPIChangesRejectsNonPositiveCap for why the log is captured even
	// though nothing here reads it: detectAPIChanges' first statement sets the logger's
	// prefix, which would otherwise leak into every test running after this one.
	_ = captureLog(t)
	defer func(p string, c int, d bool) { OptJobsConfigPath, OptMaxOpenIssues, OptDryRun = p, c, d }(
		OptJobsConfigPath, OptMaxOpenIssues, OptDryRun)
	OptJobsConfigPath, OptMaxOpenIssues, OptDryRun = "../../../jobs_config.yaml", 0, true

	require.ErrorContains(t, detectAPIChanges(nil, nil), "--max-open-issues must be positive")
}

func TestDetectAPIChangesRejectsAnOutputDirWithoutDryRun(t *testing.T) {
	// Silently ignoring the flag is the failure: somebody passes only
	// --dry-run-output-dir to preview, the job files issues on the public community
	// repo for real, and no files appear — so they conclude the detector found
	// nothing. The guard survived mutation until this test existed.
	//
	// captureLog for the reason TestDetectAPIChangesRejectsNonPositiveCap gives: the
	// command sets a log prefix before doing anything else.
	_ = captureLog(t)
	defer func(p string, c int, d bool, o string) {
		OptJobsConfigPath, OptMaxOpenIssues, OptDryRun, OptDryRunOutputDir = p, c, d, o
	}(OptJobsConfigPath, OptMaxOpenIssues, OptDryRun, OptDryRunOutputDir)
	OptJobsConfigPath, OptMaxOpenIssues = "../../../jobs_config.yaml", 1
	OptDryRun, OptDryRunOutputDir = false, t.TempDir()

	err := detectAPIChanges(nil, nil)
	require.ErrorContains(t, err, "--dry-run-output-dir is only meaningful with --dry-run")

	// With --dry-run it is accepted, so the guard is about the combination and not
	// about the flag. The run gets as far as newGithubClientFromEnv and stops there for
	// want of a token — which is the point: it is past the guard. GITHUB_TOKEN is
	// cleared so a machine that happens to have one cannot carry this to api.github.com.
	t.Setenv("GITHUB_TOKEN", "")
	OptDryRun = true
	err = detectAPIChanges(nil, nil)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "--dry-run-output-dir")
}

func TestReconcileServicesDryRunWritesBodiesForReview(t *testing.T) {
	// The reason to have a preview at all: read the issue body before a bot posts it.
	// Both paths, because they post different bytes — a create posts the rendered body,
	// a refresh posts that region spliced back into the existing issue. Covering only
	// the create left the file wrong for every refresh: measured at 729 bytes in the
	// file against 797 PATCHed, with the maintainer's note missing from the file. An
	// operator reading that would conclude the refresh was about to delete the note.
	dir := t.TempDir()
	logged := captureLog(t)
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("dry run must not write, got %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{}`)
	}))

	// A region carrying a different finding set, so the fingerprint does not match and
	// the run takes the refresh path, plus the note this is all about.
	const maintainerNote = "CreateGizmo is intentionally unsupported; see #1234."
	oldRegion, _ := renderIssueBody("svcrefresh", "v1.40.0", "v1.41.5", sampleFindings()[:1])
	existingBody := oldRegion + "\n" + maintainerNote + "\n"

	analyze := dryRunAnalyzer(map[string][]Finding{
		"svc1": sampleFindings(), "svc2": nil, "svcrefresh": sampleFindings(),
	})
	existing := map[string]*github.Issue{"svcrefresh": issueFor(42, existingBody)}
	_, _, _, err := reconcileServices(
		context.Background(), client, "o", "community",
		[]string{"svc1", "svc2", "svcrefresh"}, existing, nil, 10, 0, analyze, true, dir)
	require.NoError(t, err)

	body, err := os.ReadFile(filepath.Join(dir, "svc1.md"))
	require.NoError(t, err)
	want, _ := renderIssueBody("svc1", "v1.41.5", "v1.44.0", sampleFindings())
	assert.Equal(t, want, string(body), "a create posts the rendered body")

	// The refresh: what lands in the file is what updateGithubIssueBody would be handed,
	// computed here the same way reconcileIssue computes it.
	refreshed, err := os.ReadFile(filepath.Join(dir, "svcrefresh.md"))
	require.NoError(t, err)
	region, _ := renderIssueBody("svcrefresh", "v1.41.5", "v1.44.0", sampleFindings())
	assert.Equal(t, replaceGeneratedRegion(existingBody, region), string(refreshed),
		"a refresh posts the region spliced into the existing body")
	// Stated separately, because the equality above would also hold if
	// replaceGeneratedRegion itself ever stopped preserving human text. This is the
	// thing an operator reads the file for.
	assert.Contains(t, string(refreshed), maintainerNote,
		"the preview must show the maintainer's note the refresh preserves")
	assert.Greater(t, len(refreshed), len(region),
		"the merged body is larger than the region alone; a preview of the region "+
			"understates what would be posted")

	// svc2 has no findings, so there is no body to preview and no file.
	_, err = os.Stat(filepath.Join(dir, "svc2.md"))
	assert.True(t, os.IsNotExist(err))

	// The log has to say which of the two each file is, since their contents are not
	// comparable: one is a whole new issue, the other a merge into an existing one.
	assert.Contains(t, logged.String(), "svc1: wrote the would-be new issue body to ")
	assert.Contains(t, logged.String(), "svcrefresh: wrote the would-be refreshed issue body to ")
}

func TestReconcileServicesDryRunPreviewsAnOversizedRefresh(t *testing.T) {
	// The oversized merge is the case a preview matters most for, and the one a
	// region-only file misled worst: the file looked comfortably small while the body
	// that would actually be PATCHed was over GitHub's limit, so a reader concluded the
	// refresh was safe. The service still fails the run — the refresh genuinely cannot
	// land — but the file must show the merged body that could not be posted, which is
	// why it is written before reconcileIssue decides rather than after.
	dir := t.TempDir()
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("dry run must not write, got %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{}`)
	}))

	oldRegion, _ := renderIssueBody("svcoversized", "v1.40.0", "v1.41.5", sampleFindings()[:1])
	existingBody := oldRegion + "\n\n" + strings.Repeat("x", githubMaxIssueBody)
	existing := map[string]*github.Issue{"svcoversized": issueFor(42, existingBody)}

	analyze := dryRunAnalyzer(map[string][]Finding{"svcoversized": sampleFindings()})
	_, writeFailures, _, err := reconcileServices(
		context.Background(), client, "o", "community",
		[]string{"svcoversized"}, existing, nil, 10, 0, analyze, true, dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"svcoversized"}, writeFailures,
		"the oversize guard is a pure computation and must fire in a dry run too")

	preview, err := os.ReadFile(filepath.Join(dir, "svcoversized.md"))
	require.NoError(t, err)
	assert.Greater(t, len(preview), githubMaxIssueBody,
		"the file must show the over-limit merged body, not the region that fits inside it")
}

func TestDryRunDoesNotFailTheRunAtTheCap(t *testing.T) {
	// A preview exists to report, not to go red. The cap binding is the expected
	// answer on a rollout, so in dry-run it is information; an analysis failure is
	// still a failure.
	assert.NoError(t, runError(nil, 1, []string{"svc2"}, nil, nil, true))
	assert.ErrorContains(t,
		runError(nil, 1, []string{"svc2"}, []string{"svc3"}, nil, true),
		"svc3")
	// Live mode is unchanged: the cap is still an error.
	assert.ErrorContains(t, runError(nil, 1, []string{"svc2"}, nil, nil, false), "cap")
}

// TestDryRunStatesWhatItCannotCheck pins the two caveats, which are limits of the
// feature rather than pleasantries: errCannotLabelIssues is raised inside
// createGithubIssueWithClient *after* the POST, by re-reading the created issue, so a
// dry run cannot reach that check at all, and a clean preview is therefore no
// evidence the token can label. Left unsaid, a green dry run reads as a rehearsal of
// a real one.
func TestDryRunStatesWhatItCannotCheck(t *testing.T) {
	logged := captureLog(t)
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("dry run must not write, got %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{}`)
	}))

	analyze := dryRunAnalyzer(map[string][]Finding{"svc1": sampleFindings(), "svc2": sampleFindings()})
	_, _, _, err := reconcileServices(
		context.Background(), client, "o", "community",
		[]string{"svc1", "svc2"}, nil, nil, 10, 0, analyze, true, "")
	require.NoError(t, err)
	assert.Contains(t, logged.String(), dryRunCaveat)
	assert.Equal(t, 1, strings.Count(logged.String(), dryRunCaveat),
		"the caveats belong once at the end of the run, not once per service")

	// Live mode must not carry them: a run that really wrote has checked both.
	liveLog := captureLog(t)
	liveClient := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"number": 1, "labels": [{"name": %q}]}`, apiChangeLabel)
	}))
	_, _, _, err = reconcileServices(
		context.Background(), liveClient, "o", "community",
		[]string{"svc1"}, nil, nil, 10, 0, analyze, false, "")
	require.NoError(t, err)
	assert.NotContains(t, liveLog.String(), dryRunCaveat)
}

func TestReconcileIssueRewordsSilentlyWhenOnlyTheTextChanged(t *testing.T) {
	// Observed live: annotations a reviewer asked for never reached the open ec2
	// issue, because its finding set — all the fingerprint covers — was unchanged.
	findings := sampleFindings()
	filed, _ := renderIssueBody("demo", "v1.41.5", "v1.44.0", findings)
	findings[0].Detail = "create-only: no operation changes it after creation, so it is immutable"

	var patched bool
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == "/repos/o/community/issues/42" {
			patched = true
			fmt.Fprint(w, `{}`)
			return
		}
		t.Errorf("only a silent PATCH expected, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))

	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.42.0", "v1.45.0", findings, issueFor(42, filed), nil, 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueUpdated, outcome)
	assert.True(t, patched)
}
