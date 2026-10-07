package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
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
	// go-version rejects a leading "v", so it is stripped to compare but kept
	// in the result: callers build model URLs from it.
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
	// Nested sub-modules (service/s3/internal/configtesting/vX) share the prefix;
	// only a single-segment remainder is a service version.
	got := refsAtPrefixDepth([]string{
		"refs/tags/service/s3/v1.113.4",
		"refs/tags/service/s3/internal/configtesting/v0.1.0",
		"refs/tags/service/s3control/v1.0.0",
	}, "tags/service/s3/")

	assert.Equal(t, []string{"refs/tags/service/s3/v1.113.4"}, got,
		"nested sub-module tags must be discarded")

	// A non-version tag beginning with v is dropped too.
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

// withNoNetwork makes any HTTP attempt fail the test, so a cache-path bug
// cannot silently fall through to a live fetch.
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
	// httpGet is tested directly because fetchModel's URLs are hardcoded. Covers a
	// successful read and the non-200 error naming URL and status.
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

// TestFindNewResourcesMultiRoleOperation: an operation that both creates and
// deletes the resource (route53's ChangeResourceRecordSets) is not transient.
func TestFindNewResourcesMultiRoleOperation(t *testing.T) {
	m := opsModel(t, map[string][]string{"ChangeGadgetSettings": {"GadgetName"}})
	baseline := opsModel(t, map[string][]string{"ListThings": {"ThingName"}})
	in := &ControllerInputs{Config: &generatorConfig{Operations: map[string]operationOverride{
		"ChangeGadgetSettings": {OperationType: stringArray{"Create", "Delete"}, ResourceName: stringArray{"Gadget"}},
	}}}

	got := findNewResources(m, baseline, in)
	require.Len(t, got, 1)
	assert.Equal(t, ClassNewResource, got[0].Class, "the Delete role must count")
	assert.Equal(t, "Gadget", got[0].Subject)
}

func TestFindNewOperations(t *testing.T) {
	baseline := loadTestModel(t, "smithy_basic.json")
	latest := loadTestModel(t, "smithy_latest.json")
	in, err := ReadControllerInputs("../../../testdata", "fake")
	require.NoError(t, err)

	got := findNewOperations(latest, baseline, in)

	// ListWidgets classifies but is uncalled; PutWidgetPolicy and ResetWidget do
	// not classify and are placed on Widget by name.
	assert.Equal(t, []string{"ListWidgets", "PutWidgetPolicy", "ResetWidget"}, subjects(got, ClassNewOperation))
	assert.Empty(t, subjects(got, ClassUnknownOperation))

	// TagResource is denylisted, PutWidgetTagging ignored. AbortSession is the only
	// case reaching namesIgnoredResource through findNewOperations, so it is checked
	// against every reportable finding; it is still listed as dropped.
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
	// Same model on both sides: operations older than the pin are not news, even
	// when uncalled (as sns's ListTopics was).
	latest := loadTestModel(t, "smithy_latest.json")
	in, err := ReadControllerInputs("../../../testdata", "fake")
	require.NoError(t, err)

	got := findNewOperations(latest, latest, in)
	assert.Empty(t, subjects(got, ClassNewOperation))
	assert.Empty(t, subjects(got, ClassUnknownOperation))
}

func TestNamesIgnoredResource(t *testing.T) {
	// s3's real ignore.resource_names. These ops classify to OpTypeUnknown, so this
	// filter is all that keeps them out of the unclassified bucket.
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
	assert.True(t, exposedInCRD(in, "Widget", "Name", false))
	assert.True(t, exposedInCRD(in, "Widget", "Config.Size", false))
	assert.True(t, exposedInCRD(in, "Widget", "Rules.Prefix", false))

	// A Status field covers an output-only candidate, not a settable one; a
	// Spec field covers both.
	assert.True(t, exposedInCRD(in, "Widget", "WidgetID", true))
	assert.False(t, exposedInCRD(in, "Widget", "WidgetID", false))
	assert.True(t, exposedInCRD(in, "Widget", "Name", true))

	// Renamed: generator.yaml maps WidgetName -> Name, and the CRD has "name".
	assert.True(t, exposedInCRD(in, "Widget", "WidgetName", false))

	assert.False(t, exposedInCRD(in, "Widget", "Description", true))
	assert.False(t, exposedInCRD(in, "Widget", "Config.Color", true))

	assert.False(t, exposedInCRD(in, "Gizmo", "Name", true))
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
	// Only the short name of the target ID (com.amazonaws.s3#...) is compared.
	declined := []string{"BlockedEncryptionTypes", "Ignored"}

	assert.True(t, declinedShapeName("com.amazonaws.s3#BlockedEncryptionTypes", declined))
	assert.True(t, declinedShapeName("com.amazonaws.demo#ignored", declined),
		"comparison is case-insensitive")
	assert.False(t, declinedShapeName("com.amazonaws.s3#EncryptionRule", declined))
	assert.False(t, declinedShapeName("", declined))
	assert.False(t, declinedShapeName("com.amazonaws.s3#Anything", nil))
}

func TestDeclinedShapeAncestor(t *testing.T) {
	// Declining a shape also declines what hangs beneath it, even when the child's
	// own target shape is not declined (the real s3 case).
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
	assert.False(t, sourcedAsCRDField(in, "Gizmo", "PutBucketAbac", "AbacStatus"))

	// applicationautoscaling's `..` steps into a list element.
	in.Config.Resources["ScalingPolicy"] = resourceConfig{Fields: map[string]resourceFieldConfig{
		"CreationTime": {From: &resourceFieldFrom{
			Operation: "DescribeScalingPolicies", Path: "ScalingPolicies..CreationTime"}},
	}}
	assert.True(t, sourcedAsCRDField(in, "ScalingPolicy", "DescribeScalingPolicies", "ScalingPolicies.CreationTime"))
}

// sourcedModel has s3's PutObjectLockConfiguration/GetObjectLockConfiguration and
// elbv2's RegisterTargets/DeregisterTargets; latest adds DefaultEventHold and
// QuicServerId.
func sourcedModel(t *testing.T, latest bool) *SmithyModel {
	t.Helper()
	retention, target := `"Days": {"target": "smithy.api#Integer"}`, `"Id": {"target": "smithy.api#String"}`
	if latest {
		retention += `, "DefaultEventHold": {"target": "smithy.api#Boolean"}`
		target += `, "QuicServerId": {"target": "smithy.api#String"}`
	}
	m, err := LoadSmithyModel([]byte(`{"shapes": {
		"demo#PutLock": {"type": "operation", "input": {"target": "demo#PutLockIn"}},
		"demo#PutLockIn": {"type": "structure", "members": {
			"Bucket": {"target": "smithy.api#String"}, "ObjectLockConfiguration": {"target": "demo#Lock"}}},
		"demo#GetLock": {"type": "operation", "output": {"target": "demo#GetLockOut"}},
		"demo#GetLockOut": {"type": "structure", "members": {"ObjectLockConfiguration": {"target": "demo#Lock"}}},
		"demo#Lock": {"type": "structure", "members": {"Rule": {"target": "demo#Rule"}}},
		"demo#Rule": {"type": "structure", "members": {"DefaultRetention": {"target": "demo#Retention"}}},
		"demo#Retention": {"type": "structure", "members": {` + retention + `}},
		"demo#RegisterTargets": {"type": "operation", "input": {"target": "demo#TargetsIn"}},
		"demo#DeregisterTargets": {"type": "operation", "input": {"target": "demo#TargetsIn"}},
		"demo#TargetsIn": {"type": "structure", "members": {
			"TargetGroupArn": {"target": "smithy.api#String"}, "Targets": {"target": "demo#TargetList"}}},
		"demo#TargetList": {"type": "list", "member": {"target": "demo#Target"}},
		"demo#Target": {"type": "structure", "members": {` + target + `}}
	}}`))
	require.NoError(t, err)
	return m
}

// TestFindAddedFieldsKeysSourcedDescendantsUnderTheirField: a new member below a
// `from:` field is that Spec field's member, whatever else carries it.
func TestFindAddedFieldsKeysSourcedDescendantsUnderTheirField(t *testing.T) {
	in := &ControllerInputs{
		Config: &generatorConfig{Resources: map[string]resourceConfig{
			"Bucket": {Fields: map[string]resourceFieldConfig{
				"ObjectLockConfiguration": {From: &resourceFieldFrom{Operation: "PutLock", Path: "ObjectLockConfiguration"}},
			}},
			"TargetGroup": {Fields: map[string]resourceFieldConfig{
				"Members": {From: &resourceFieldFrom{Operation: "RegisterTargets", Path: "Targets"}},
			}},
		}},
		CRDFields: map[string]map[string]bool{
			"Bucket": {
				"objectlockconfiguration":                            true,
				"objectlockconfiguration.rule":                       true,
				"objectlockconfiguration.rule.defaultretention":      true,
				"objectlockconfiguration.rule.defaultretention.days": true,
			},
			"TargetGroup": {"members": true, "members.id": true},
		},
		UsedOps: map[string]map[string]bool{
			"bucket":      {"GetLock": true, "PutLock": true},
			"targetgroup": {"DeregisterTargets": true, "RegisterTargets": true},
		},
	}

	got := findAddedFields(sourcedModel(t, true), sourcedModel(t, false), in)
	assert.Equal(t, []Finding{
		{Kind: "Bucket", Class: ClassSpecField, NewSincePin: true,
			Subject: "ObjectLockConfiguration.Rule.DefaultRetention.DefaultEventHold", Evidence: "GetLock,PutLock"},
		// Keyed by the configured field's name. DeregisterTargets' Targets is not
		// in the CRD, so its QuicServerId is declined.
		{Kind: "TargetGroup", Class: ClassSpecField, NewSincePin: true,
			Subject: "Members.QuicServerId", Evidence: "RegisterTargets",
			SDKPaths: "RegisterTargets=Targets.QuicServerId"},
	}, got, "the source operation's role counts, not only the read-back")

	// A read-only `from:` field is in Status, so only the operations' roles count.
	in.Config.Resources["TargetGroup"].Fields["Members"] = resourceFieldConfig{
		IsReadOnly: true, From: &resourceFieldFrom{Operation: "RegisterTargets", Path: "Targets"}}
	got = findAddedFields(sourcedModel(t, true), sourcedModel(t, false), in)
	require.Len(t, got, 2)
	assert.Equal(t, ClassLifecycleField, got[1].Class)
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

// wrapperModel builds responses that wrap the resource as iam's and s3's do.
// withNewField adds Role.SourceRoleTemplate and DefaultRetention.DefaultEventHold.
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

	// One finding per new field per resource; children and extra operations go in
	// Evidence. Returned-only fields are Status candidates; Role's is also sent on
	// CreateRole, so Spec.
	got := findAddedFields(wrapperModel(t, true), wrapperModel(t, false), in)
	assert.Equal(t, []Finding{
		{Kind: "Bucket", Class: ClassStatusField, NewSincePin: true,
			Subject: "ObjectLockConfiguration.Rule.DefaultRetention.DefaultEventHold", Evidence: "GetLock"},
		{Kind: "Group", Class: ClassStatusField, NewSincePin: true,
			Subject: "SourceRoleTemplate", Evidence: "GetGroup",
			SDKPaths: "GetGroup=Group.SourceRoleTemplate"},
		{Kind: "Role", Class: ClassSpecField, NewSincePin: true,
			Subject: "SourceRoleTemplate", Evidence: "CreateRole,GetRole,UpdateRole",
			SDKPaths: "CreateRole=Role.SourceRoleTemplate,GetRole=Role.SourceRoleTemplate"},
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

// inputWrapperModel has backup's CreateBackupPlan shape, with the plan's fields
// under a BackupPlan request member. withNewField adds a member inside the
// wrapper, one inside an existing field under it, and one outside it.
func inputWrapperModel(t *testing.T, withNewField bool) *SmithyModel {
	t.Helper()
	request := `"BackupPlan": {"target": "demo#BackupPlanInput"},
		"CreatorRequestId": {"target": "smithy.api#String"}`
	plan := `"BackupPlanName": {"target": "smithy.api#String"},
		"Rules": {"target": "demo#Rule"}`
	rule := `"RuleName": {"target": "smithy.api#String"}`
	if withNewField {
		request += `, "OuterOption": {"target": "smithy.api#String"}`
		plan += `, "ScanSettings": {"target": "smithy.api#String"}`
		rule += `, "IndexActions": {"target": "smithy.api#String"}`
	}
	m, err := LoadSmithyModel([]byte(`{"shapes": {
		"demo#CreateBackupPlan": {"type": "operation", "input": {"target": "demo#CreateBackupPlanInput"}},
		"demo#CreateBackupPlanInput": {"type": "structure", "members": {` + request + `}},
		"demo#BackupPlanInput": {"type": "structure", "members": {` + plan + `}},
		"demo#Rule": {"type": "structure", "members": {` + rule + `}}
	}}`))
	require.NoError(t, err)
	return m
}

func TestFindAddedFieldsFollowsCodegenInputWrapper(t *testing.T) {
	in := &ControllerInputs{
		Config: &generatorConfig{Operations: map[string]operationOverride{
			"CreateBackupPlan": {InputWrapperFieldPath: "BackupPlan"},
		}},
		// Codegen flattened BackupPlan's members into Spec.
		CRDFields: map[string]map[string]bool{
			"BackupPlan": {"backupplanname": true, "rules": true, "rules.rulename": true},
		},
		UsedOps: map[string]map[string]bool{"backupplan": {"CreateBackupPlan": true}},
	}

	got := findAddedFields(inputWrapperModel(t, true), inputWrapperModel(t, false), in)
	assert.Equal(t, []Finding{
		{Kind: "BackupPlan", Class: ClassSpecField, NewSincePin: true,
			Subject: "Rules.IndexActions", Evidence: "CreateBackupPlan",
			SDKPaths: "CreateBackupPlan=BackupPlan.Rules.IndexActions"},
		{Kind: "BackupPlan", Class: ClassSpecField, NewSincePin: true,
			Subject: "ScanSettings", Evidence: "CreateBackupPlan",
			SDKPaths: "CreateBackupPlan=BackupPlan.ScanSettings"},
	}, got,
		"members inside the wrapper are reported without it, and OuterOption, which "+
			"codegen leaves out of Spec, not at all")

	m := inputWrapperModel(t, true)
	assert.True(t, requestCarries(m, in, "CreateBackupPlan", "ScanSettings"),
		"a finding's Subject is spelled relative to the input wrapper")
	assert.True(t, requestCarries(m, in, "CreateBackupPlan", "OuterOption"))
	assert.False(t, requestCarries(m, nil, "CreateBackupPlan", "ScanSettings"))
}

// readManyModel has applicationautoscaling's DescribeScalingPolicies. latest
// adds a policy member and the paginated trait's members; Alarms sorts before
// the resource list, so only the trait's `items` picks ScalingPolicies.
func readManyModel(t *testing.T, latest bool) *SmithyModel {
	t.Helper()
	request, response := `"ServiceNamespace": {"target": "smithy.api#String"}`, `"Alarms": {"target": "demo#Alarms"}, "ScalingPolicies": {"target": "demo#Policies"}`
	policy := `"PolicyName": {"target": "smithy.api#String"}`
	if latest {
		request += `, "NextToken": {"target": "smithy.api#String"}, "MaxResults": {"target": "smithy.api#Integer"}`
		response += `, "NextToken": {"target": "smithy.api#String"}`
		policy += `, "NewField": {"target": "smithy.api#String"}`
	}
	m, err := LoadSmithyModel([]byte(`{"shapes": {
		"demo#DescribeScalingPolicies": {"type": "operation",
			"input": {"target": "demo#DescribeIn"}, "output": {"target": "demo#DescribeOut"},
			"traits": {"smithy.api#paginated": {"inputToken": "NextToken", "outputToken": "NextToken",
				"pageSize": "MaxResults", "items": "ScalingPolicies"}}},
		"demo#DescribeIn": {"type": "structure", "members": {` + request + `}},
		"demo#DescribeOut": {"type": "structure", "members": {` + response + `}},
		"demo#Alarms": {"type": "list", "member": {"target": "demo#Alarm"}},
		"demo#Alarm": {"type": "structure", "members": {"AlarmName": {"target": "smithy.api#String"}}},
		"demo#Policies": {"type": "list", "member": {"target": "demo#Policy"}},
		"demo#Policy": {"type": "structure", "members": {` + policy + `}}
	}}`))
	require.NoError(t, err)
	return m
}

func TestFindAddedFieldsUnwrapsReadManyList(t *testing.T) {
	in := &ControllerInputs{
		Config:    &generatorConfig{},
		CRDFields: map[string]map[string]bool{"ScalingPolicy": {"policyname": true}},
		UsedOps:   map[string]map[string]bool{"scalingpolicy": {"DescribeScalingPolicies": true}},
	}
	latest := readManyModel(t, true)

	op, _ := latest.Operation("DescribeScalingPolicies")
	assert.Equal(t, "ScalingPolicies", outputWrapper(latest, in, "DescribeScalingPolicies", op.Output))

	got := findAddedFields(latest, readManyModel(t, false), in)
	assert.Equal(t, []Finding{
		{Kind: "ScalingPolicy", Class: ClassStatusField, NewSincePin: true,
			Subject: "NewField", Evidence: "DescribeScalingPolicies",
			SDKPaths: "DescribeScalingPolicies=ScalingPolicies.NewField"},
	}, got, "the policy list is not a declined parent, and pagination members are plumbing")
}

func TestPaginationMembersFallBackToTheService(t *testing.T) {
	// lambda's: the service trait names the tokens, ListAliases only its items.
	m, err := LoadSmithyModel([]byte(`{"shapes": {
		"demo#Lambda": {"type": "service", "traits": {"smithy.api#paginated":
			{"inputToken": "Marker", "outputToken": "NextMarker", "pageSize": "MaxItems"}}},
		"demo#ListAliases": {"type": "operation", "traits": {"smithy.api#paginated": {"items": "Aliases"}}},
		"demo#GetAlias": {"type": "operation"}
	}}`))
	require.NoError(t, err)
	svc := servicePagination(m)

	assert.Equal(t, []string{"Marker", "MaxItems"}, paginationMembers(m, "ListAliases", svc, false))
	assert.Equal(t, []string{"NextMarker"}, paginationMembers(m, "ListAliases", svc, true))
	assert.Empty(t, paginationMembers(m, "GetAlias", svc, true), "an unpaginated operation pages with nothing")
}

// widgetModel has a GetWidget response wrapping the resource beside another
// member, so only output_wrapper_field_path unwraps it. latest adds Color and
// Shade inside the wrapper.
func widgetModel(t *testing.T, latest bool) *SmithyModel {
	t.Helper()
	widget := `"Name": {"target": "smithy.api#String"}`
	if latest {
		widget += `, "Color": {"target": "smithy.api#String"}, "Shade": {"target": "smithy.api#String"}`
	}
	m, err := LoadSmithyModel([]byte(`{"shapes": {
		"demo#GetWidget": {"type": "operation", "output": {"target": "demo#GetWidgetOut"}},
		"demo#GetWidgetOut": {"type": "structure", "members": {
			"Widget": {"target": "demo#Widget"}, "RequestId": {"target": "smithy.api#String"}}},
		"demo#Widget": {"type": "structure", "members": {` + widget + `}}
	}}`))
	require.NoError(t, err)
	return m
}

func TestFindAddedFieldsChecksTheCRDBehindAConfiguredWrapper(t *testing.T) {
	in := &ControllerInputs{
		Config: &generatorConfig{Operations: map[string]operationOverride{
			"GetWidget": {OutputWrapperFieldPath: "Widget"},
		}},
		CRDFields: map[string]map[string]bool{"Widget": {"name": true, "color": true}},
		UsedOps:   map[string]map[string]bool{"widget": {"GetWidget": true}},
	}

	got := findAddedFields(widgetModel(t, true), widgetModel(t, false), in)
	assert.Equal(t, []Finding{
		{Kind: "Widget", Class: ClassStatusField, NewSincePin: true,
			Subject: "Shade", Evidence: "GetWidget", SDKPaths: "GetWidget=Widget.Shade"},
	}, got, "Widget.Color is the CRD's existing color field")

	// emrcontainers configures `VirtualCluster` for Smithy's `virtualCluster`.
	in.Config.Operations["GetWidget"] = operationOverride{OutputWrapperFieldPath: "widget"}
	assert.Equal(t, got, findAddedFields(widgetModel(t, true), widgetModel(t, false), in),
		"wrapper paths match the model case-insensitively, as in codegen")
	in.Config.Operations["CreateWidget"] = operationOverride{InputWrapperFieldPath: "WIDGET"}
	m := widgetModel(t, true)
	assert.Equal(t, "Widget", inputWrapper(m, in, "CreateWidget", &SmithyMemberRef{Target: "demo#GetWidgetOut"}))
	assert.Equal(t, "Widget.Name", modelSpelling(m, &SmithyMemberRef{Target: "demo#GetWidgetOut"}, "widget.NAME"))
	assert.Equal(t, "Nope", modelSpelling(m, &SmithyMemberRef{Target: "demo#GetWidgetOut"}, "Nope"))
}

func TestCRDFieldPathKeepsAModelledWrapper(t *testing.T) {
	m := wrapperModel(t, true)
	ref := &SmithyMemberRef{Target: "demo#GetRoleOut"}
	path := func(fields ...string) string {
		crd := map[string]bool{}
		for _, f := range fields {
			crd[f] = true
		}
		in := &ControllerInputs{CRDFields: map[string]map[string]bool{"Role": crd}}
		got, ok := crdFieldPath(m, in, "Role", "GetRole", ref, true, "Role.SourceRoleTemplate")
		require.True(t, ok)
		return got
	}

	assert.Equal(t, "SourceRoleTemplate", path())
	assert.Equal(t, "Role.SourceRoleTemplate", path("role"), "s3: the CRD models the wrapper")
	assert.Equal(t, "SourceRoleTemplate", path("role", "sourceroletemplate"),
		"acm: the CRD's certificate is another field; its options are the unwrapped member")
}

// accountModel has organizations' CreateAccount and DescribeAccount; latest adds
// an `Id` to both responses, which generator.yaml renames differently per operation.
func accountModel(t *testing.T, latest bool) *SmithyModel {
	t.Helper()
	create, describe := `"State": {"target": "smithy.api#String"}`, `"Name": {"target": "smithy.api#String"}`
	if latest {
		create += `, "Id": {"target": "smithy.api#String"}`
		describe += `, "Id": {"target": "smithy.api#String"}`
	}
	m, err := LoadSmithyModel([]byte(`{"shapes": {
		"demo#CreateAccount": {"type": "operation", "output": {"target": "demo#CreateOut"}},
		"demo#CreateOut": {"type": "structure", "members": {` + create + `}},
		"demo#DescribeAccount": {"type": "operation", "output": {"target": "demo#DescribeOut"}},
		"demo#DescribeOut": {"type": "structure", "members": {` + describe + `}}
	}}`))
	require.NoError(t, err)
	return m
}

func TestFindAddedFieldsAppliesEachOperationsRenames(t *testing.T) {
	in := &ControllerInputs{
		Config: &generatorConfig{Resources: map[string]resourceConfig{
			"Account": {Renames: resourceRenames{Operations: map[string]operationRenames{
				"CreateAccount":   {OutputFields: map[string]string{"Id": "CreateAccountRequestId"}},
				"DescribeAccount": {OutputFields: map[string]string{"Id": "AccountID"}},
			}}},
		}},
		CRDFields: map[string]map[string]bool{"Account": {"accountid": true, "name": true, "state": true}},
		UsedOps:   map[string]map[string]bool{"account": {"CreateAccount": true, "DescribeAccount": true}},
	}

	got := findAddedFields(accountModel(t, true), accountModel(t, false), in)
	want := Finding{Kind: "Account", Class: ClassStatusField, NewSincePin: true,
		Subject: "CreateAccountRequestId", Evidence: "CreateAccount", SDKPaths: "CreateAccount=Id"}
	assert.Equal(t, []Finding{want}, got,
		"DescribeAccount's Id is the CRD's accountID; CreateAccount's is a field the CRD lacks")
}

// thingModel reaches one CRD field through an input wrapper on Create, an
// inferred output wrapper on the read and a rename on Update.
func thingModel(t *testing.T, latest bool) *SmithyModel {
	t.Helper()
	thing, update := `"Name": {"target": "smithy.api#String"}`, `"Name": {"target": "smithy.api#String"}`
	if latest {
		thing += `, "Mode": {"target": "smithy.api#String"}`
		update += `, "ThingMode": {"target": "smithy.api#String"}`
	}
	m, err := LoadSmithyModel([]byte(`{"shapes": {
		"demo#CreateThing": {"type": "operation", "input": {"target": "demo#CreateIn"}},
		"demo#CreateIn": {"type": "structure", "members": {
			"Thing": {"target": "demo#Thing"}, "ClientToken": {"target": "smithy.api#String"}}},
		"demo#GetThing": {"type": "operation", "output": {"target": "demo#GetOut"}},
		"demo#GetOut": {"type": "structure", "members": {"Thing": {"target": "demo#Thing"}}},
		"demo#UpdateThing": {"type": "operation", "input": {"target": "demo#UpdateIn"}},
		"demo#UpdateIn": {"type": "structure", "members": {` + update + `}},
		"demo#Thing": {"type": "structure", "members": {` + thing + `}}
	}}`))
	require.NoError(t, err)
	return m
}

func TestFindAddedFieldsGroupsByCRDPath(t *testing.T) {
	in := &ControllerInputs{
		Config: &generatorConfig{
			Operations: map[string]operationOverride{"CreateThing": {InputWrapperFieldPath: "Thing"}},
			Resources: map[string]resourceConfig{
				"Thing": {Renames: resourceRenames{Operations: map[string]operationRenames{
					"UpdateThing": {InputFields: map[string]string{"ThingMode": "Mode"}},
				}}},
			},
		},
		CRDFields: map[string]map[string]bool{"Thing": {"name": true}},
		UsedOps: map[string]map[string]bool{
			"thing": {"CreateThing": true, "GetThing": true, "UpdateThing": true},
		},
	}

	got := findAddedFields(thingModel(t, true), thingModel(t, false), in)
	assert.Equal(t, []Finding{
		{Kind: "Thing", Class: ClassSpecField, NewSincePin: true,
			Subject: "Mode", Evidence: "CreateThing,GetThing,UpdateThing",
			SDKPaths: "CreateThing=Thing.Mode,GetThing=Thing.Mode,UpdateThing=ThingMode"},
	}, got, "one CRD field, however each operation spells it")
}

// readBackModel: GetThing already returns Mode; only UpdateThing's request gains
// it, when latest.
func readBackModel(t *testing.T, latest bool) *SmithyModel {
	t.Helper()
	update := `"Name": {"target": "smithy.api#String"}`
	if latest {
		update += `, "Mode": {"target": "smithy.api#String"}`
	}
	m, err := LoadSmithyModel([]byte(`{"shapes": {
		"demo#GetThing": {"type": "operation", "output": {"target": "demo#GetOut"}},
		"demo#GetOut": {"type": "structure", "members": {"Thing": {"target": "demo#Thing"}}},
		"demo#UpdateThing": {"type": "operation", "input": {"target": "demo#UpdateIn"}},
		"demo#UpdateIn": {"type": "structure", "members": {` + update + `}},
		"demo#Thing": {"type": "structure", "members": {
			"Name": {"target": "smithy.api#String"}, "Mode": {"target": "smithy.api#String"}}}
	}}`))
	require.NoError(t, err)
	return m
}

func TestFindAddedFieldsCountsPreexistingReadBack(t *testing.T) {
	in := &ControllerInputs{
		Config:    &generatorConfig{},
		CRDFields: map[string]map[string]bool{"Thing": {"name": true}},
		UsedOps:   map[string]map[string]bool{"thing": {"GetThing": true, "UpdateThing": true}},
	}

	got := findAddedFields(readBackModel(t, true), readBackModel(t, false), in)
	assert.Equal(t, []Finding{
		{Kind: "Thing", Class: ClassSpecField, NewSincePin: true, Subject: "Mode", Evidence: "UpdateThing"},
	}, got, "GetThing already reading Mode back makes the newly settable field Spec")

	// An ignored read-back is not one: codegen never reads it.
	in.Config.Ignore.FieldPaths = []string{"GetThingOutput.Thing.Mode"}
	got = findAddedFields(readBackModel(t, true), readBackModel(t, false), in)
	require.Len(t, got, 1)
	assert.Equal(t, ClassLifecycleField, got[0].Class)
}

func TestRoleScansHonourIgnoredMembers(t *testing.T) {
	m := readBackModel(t, true)
	ignoring := &ControllerInputs{
		Config: &generatorConfig{Ignore: ignoreConfig{FieldPaths: []string{"GetThingOutput.Thing.Mode"}}},
	}
	plain := &ControllerInputs{Config: &generatorConfig{}}

	assert.True(t, returnedNames(m, plain, "Thing", []string{"GetThing"})["mode"])
	assert.False(t, returnedNames(m, ignoring, "Thing", []string{"GetThing"})["mode"],
		"an ignored response member is not read back")
	assert.True(t, returnedNames(m, ignoring, "Thing", []string{"GetThing"})["name"])

	finding := func() []Finding {
		return []Finding{{Kind: "Thing", Class: ClassLifecycleField, Subject: "Mode",
			NewSincePin: true, Evidence: "UpdateThing"}}
	}
	ignoring.UsedOps = map[string]map[string]bool{"thing": {"GetThing": true, "UpdateThing": true}}
	plain.UsedOps = ignoring.UsedOps
	assert.Equal(t, "GetThing", annotateFields(m, plain, finding())[0].ReadBy)
	got := annotateFields(m, ignoring, finding())[0]
	assert.Empty(t, got.ReadBy, "an ignored response member is not guidance")
	assert.Equal(t, "UpdateThing", got.SetBy)
}

func TestConfigLookupsIgnoreKindCase(t *testing.T) {
	// ec2's VPCEndpoint CRD is configured under generator.yaml's VpcEndpoint.
	in := &ControllerInputs{
		Config: &generatorConfig{Resources: map[string]resourceConfig{
			"VpcEndpoint": {
				Renames: resourceRenames{Operations: map[string]operationRenames{
					"CreateVpcEndpoint": {InputFields: map[string]string{"VpcId": "VPCID"}},
				}},
				Fields: map[string]resourceFieldConfig{
					"Policy": {From: &resourceFieldFrom{Operation: "ModifyVpcEndpoint", Path: "PolicyDocument"}},
				},
			},
		}},
		CRDFields: map[string]map[string]bool{"VPCEndpoint": {"vpcid": true, "policy": true}},
	}

	assert.True(t, exposedInCRD(in, "VPCEndpoint", "VpcId", false))
	assert.True(t, sourcedAsCRDField(in, "VPCEndpoint", "ModifyVpcEndpoint", "PolicyDocument"))
}

func TestFindAddedFields(t *testing.T) {
	baseline := loadTestModel(t, "smithy_basic.json")
	latest := loadTestModel(t, "smithy_latest.json")
	in, err := ReadControllerInputs("../../../testdata", "fake")
	require.NoError(t, err)

	got := findAddedFields(latest, baseline, in)

	// End-to-end: each suppression mechanism has a member only it can suppress.
	// Reported: Config.Color, Description, input-side WidgetArn. Suppressed:
	// WidgetName (rename), DeclinedField, IgnoredThing(.Inner), SourcedMember,
	// output-side WidgetArn. The cycle guard stops Config.Nested.Color.
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
		// As producers emit them: no Detail and NewSincePin true, which the renderer
		// must not annotate for these classes.
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

	// Nor must a different Detail, which carries version strings and prose.
	detailChanged := sampleFindings()
	detailChanged[0].Detail = "completely different prose"
	assert.Equal(t, a, fingerprintFindings("demo", detailChanged))

	// Nor must Evidence: a resource or field gaining an operation is not new work,
	// so it only rewords the body (TestReconcileIssueEvidenceGrowthRewordsSilently).
	evidenceChanged := sampleFindings()
	evidenceChanged[0].Evidence = "CreateGizmo,DeleteGizmo,DescribeGizmos"
	evidenceChanged[2].Evidence = "CreateWidget,DescribeGizmos"
	assert.Equal(t, a, fingerprintFindings("demo", evidenceChanged))

	// Two findings with one identity are one entry, whatever their Evidence.
	splitEvidence := append(sampleFindings(), sampleFindings()[2])
	splitEvidence[len(splitEvidence)-1].Evidence = "UpdateWidget"
	assert.Equal(t, a, fingerprintFindings("demo", splitEvidence))

	// A genuinely different finding must change it.
	extra := append(sampleFindings(), Finding{Kind: "Widget", Class: ClassSpecField, Subject: "Extra", NewSincePin: true})
	assert.NotEqual(t, a, fingerprintFindings("demo", extra))

	// So must a field moving from Spec to Status: the work it implies differs.
	moved := sampleFindings()
	moved[2].Class = ClassStatusField
	assert.NotEqual(t, a, fingerprintFindings("demo", moved))
}

// TestFingerprintFormatLock pins the digest layout. Fingerprints live in open
// issue bodies, so a change recreates every open issue once; make it on purpose.
func TestFingerprintFormatLock(t *testing.T) {
	assert.Equal(t,
		"5f55652de53c20d6de431b6bd956aa4921ff4e489e747d981f76f9df731af393",
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
	// Exact body: pins section order, bullet text, footer and region markers.
	want := "<!-- ack-api-change-begin -->\n" +
		"AWS SDK releases since aws-sdk-go-v2 v1.41.5, the version the `demo` controller was generated from, add " +
		"resources and fields the controller does not represent. These are candidate additions for " +
		"maintainer review; not every item is necessarily appropriate for the CRD API. Fields under " +
		"\"Regenerate is enough\" are wired end to end by the SDK bump and `make build-controller`; each " +
		"under \"Needs config or code\" says what to add and why.\n" +
		"\n" +
		"## Resource: Gizmo\n" +
		"\n" +
		"### New resource\n" +
		"- `Gizmo` — implied by `CreateGizmo`\n" +
		"  - Operations: `CreateGizmo`, `DeleteGizmo`\n" +
		"\n" +
		"## Resource: Widget\n" +
		"\n" +
		"### Regenerate is enough\n" +
		"- `Description` (Spec) — `CreateWidget`\n" +
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
		"<!-- ack-api-change-fingerprint: 03b60f4f7efc4e51e585d73b1e52f70ada4e24ab62be85f278db34e53975de49 -->\n" +
		"<!-- ack-api-change-end -->\n"

	body, fingerprint := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	assert.Equal(t, want, body)
	assert.Equal(t, fingerprintFindings("demo", actionable(sampleFindings())), fingerprint)
}

func TestNeedsWork(t *testing.T) {
	field := func(class FindingClass, work fieldWork, detail string) Finding {
		return Finding{Kind: "Widget", Class: class, Subject: "Color", Work: work, Detail: detail, NewSincePin: true}
	}
	for _, tc := range []struct {
		name   string
		f      Finding
		reason string
		ok     bool
	}{
		{"plain spec", field(ClassSpecField, workNone, ""), "", false},
		{"plain status", field(ClassStatusField, workNone, ""), "", false},
		{"lifecycle", field(ClassLifecycleField, workNone, ""), lifecycleReason, true},
		{"lifecycle change list", field(ClassLifecycleField, workNone, "change list"), "change list", true},
		{"dedicated setter", field(ClassSpecField, workDedicatedSetter, "hook"), "hook", true},
		{"list diff", field(ClassSpecField, workListDiff, "diff"), "diff", true},
		{"custom update", field(ClassSpecField, workCustomUpdate, "custom"), "custom", true},
		{"from sibling", field(ClassSpecField, workFromSibling, "sibling"), "sibling", true},
		{"change list field", field(ClassSpecField, workChangeList, "list"), "list", true},
		{"change list entry member", field(ClassStatusField, workChangeList, "entry"), "entry", true},
		{"secondary read", field(ClassStatusField, workSecondaryRead, secondaryReadDetail), secondaryReadDetail, true},
		{"create only", field(ClassSpecField, workCreateOnly, createOnlyDetail), createOnlyDetail, true},
		// A Detail alone is not custom work, and resources are not fields.
		{"detail without work", field(ClassSpecField, workNone, "not present in the CRD"), "", false},
		{"new resource", Finding{Kind: "Gizmo", Class: ClassNewResource, Subject: "Gizmo", Work: workCustomUpdate}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reason, ok := needsWork(tc.f)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.reason, reason)
		})
	}
}

func TestBuildResourceBlockGroupsFieldsByWork(t *testing.T) {
	blk := buildResourceBlock("Table", []Finding{
		{Kind: "Table", Class: ClassStatusField, Subject: "TableStatus", NewSincePin: true,
			Evidence: "DescribeTable", ReadBy: "DescribeTable"},
		{Kind: "Table", Class: ClassSpecField, Subject: "BillingMode", NewSincePin: true,
			Evidence: "CreateTable", SetBy: "CreateTable"},
		{Kind: "Table", Class: ClassSpecField, Subject: "Replicas", NewSincePin: true, Work: workCustomUpdate,
			Detail: "hand-written update", Evidence: "UpdateTable", SetBy: "UpdateTable"},
		{Kind: "Table", Class: ClassLifecycleField, Subject: "QuoteId", NewSincePin: true,
			Evidence: "DeleteTable", SetBy: "DeleteTable"},
		{Kind: "Table", Class: ClassNewOperation, Subject: "GetTablePolicy", NewSincePin: true},
	})
	assert.Equal(t, "## Resource: Table\n\n"+
		"### Needs config or code\n"+
		"- `QuoteId` (request-only) — "+lifecycleReason+" — set by `DeleteTable`\n"+
		"- `Replicas` (Spec) — hand-written update — set by `UpdateTable`\n"+
		"\n"+
		"### Regenerate is enough\n"+
		"- `BillingMode` (Spec) — set by `CreateTable`\n"+
		"- `TableStatus` (Status) — read back by `DescribeTable`\n"+
		"\n"+
		"### Related new operations\n"+
		"- `GetTablePolicy`\n"+
		"\n", blk.renderWhole())
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
	assert.Contains(t, body, "### Regenerate is enough")
	// Whole resources were dropped, so both counts appear. Only this test reaches
	// this branch.
	assert.Contains(t, body, "further resource(s) and")
	assert.Contains(t, body, "finding(s) omitted")
	// The footer and the marker must survive truncation — the marker is the
	// lookup key for the next run.
	assert.Contains(t, body, "Compared aws-sdk-go-v2 v1.41.5 -> v1.44.0")
	assert.Equal(t, fingerprint, parseFingerprint(body))
	assert.Equal(t, fingerprintFindings("demo", dedupeFindings(findings)), fingerprint)
}

func TestRenderIssueBodyTruncatesInsideOneOversizedResource(t *testing.T) {
	// One Kind for every finding: skipping whole blocks would leave an empty issue
	// whose fingerprint never changes, so it would never be refreshed.
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
	assert.Contains(t, body, "### Regenerate is enough")
	assert.Contains(t, body, "SomeReasonablyLongFieldPath0")
	assert.Contains(t, body, "omitted")
	assert.Equal(t, fingerprint, parseFingerprint(body))
	// At least half the budget must be used; bailing out early is the bug. The
	// budget is computed because it depends on the footer.
	budget := issueBodyBudget(githubMaxIssueBody, "v1.41.5", "v1.44.0", fingerprint, 1, len(findings))
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
	assert.Equal(t, fingerprintFindings("demo", actionable(sampleFindings())), parseFingerprint(body))
	assert.Equal(t, fingerprint, parseFingerprint(body))
	assert.Equal(t, "", parseFingerprint("a body with no marker"))
}

func TestParseFingerprintReadsTheLiveRegion(t *testing.T) {
	// The first region is the live one. A quoted copy of another service's report
	// below it must not redirect the read.
	live, liveFP := renderIssueBody("s3", "v1.41.5", "v1.44.0", sampleFindings())
	quoted, quotedFP := renderIssueBody("ec2", "v1.41.5", "v1.44.0", sampleFindings())
	require.NotEqual(t, liveFP, quotedFP, "the two regions must be distinguishable")

	body := live + "\n\nQuoting the ec2 issue for comparison:\n\n" + quoted
	assert.Equal(t, liveFP, parseFingerprint(body))

	// A bare marker outside the region is not live either. Regression cover only:
	// these pass under both selection rules.
	stale := "<!-- ack-api-change-fingerprint: " + strings.Repeat("0", 64) + " -->\n"
	assert.Equal(t, liveFP, parseFingerprint(stale+live))
	assert.Equal(t, liveFP, parseFingerprint(live+"\nQuoting #7:\n\n"+stale))
}

func TestParseFingerprintReadsAFingerprintlessRegionAsHumanText(t *testing.T) {
	// A maintainer's empty example region (in a code fence) must not be read as
	// live, or the issue looks unowned and a duplicate is filed.
	live, liveFP := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	empty := "\n\nFor reference, the bot wraps its report like this:\n\n```\n" +
		generatedRegionBegin + "\n...\n" + generatedRegionEnd + "\n```\n"

	require.NotEmpty(t, liveFP)
	assert.Equal(t, liveFP, parseFingerprint(live+empty))
}

func TestParseFingerprintIgnoresAStrayBeginMarker(t *testing.T) {
	// A trailing bare begin marker must not make the body look regionless.
	live, liveFP := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	assert.Equal(t, liveFP,
		parseFingerprint(live+"\n> Note: intentional.\n\n"+generatedRegionBegin+"\n"))
}

func TestParseFingerprintToleratesCRLF(t *testing.T) {
	// Web-UI edits store CRLF; region lookup and marker match must survive it.
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
	// Only the generated region is rewritten; a whole-body PATCH would delete
	// maintainer notes.
	region, _ := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	note := "\n> Note: `CreateGizmo` is intentionally unsupported.\n"
	existing := "Filed by the detector.\n\n" + region + note

	refreshed, refreshedFingerprint := renderIssueBody("demo", "v1.44.0", "v1.45.0", sampleFindings()[:2])
	got, err := replaceGeneratedRegion(existing, refreshed)
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(got, "Filed by the detector.\n\n"))
	assert.True(t, strings.HasSuffix(got, note))
	assert.Contains(t, got, "Compared aws-sdk-go-v2 v1.44.0 -> v1.45.0")
	assert.NotContains(t, got, "Compared aws-sdk-go-v2 v1.41.5 -> v1.44.0")
	assert.Equal(t, refreshedFingerprint, parseFingerprint(got))

	// Rewriting with an unchanged region must be a no-op, or the body grows a
	// stray line every run.
	same, err := replaceGeneratedRegion(existing, region)
	require.NoError(t, err)
	assert.Equal(t, existing, same)

	// A body edited in the web UI comes back CRLF-terminated, and its markers must
	// still count as complete lines.
	crlf := strings.ReplaceAll(existing, "\n", "\r\n")
	_, err = replaceGeneratedRegion(crlf, refreshed)
	assert.NoError(t, err)
}

func TestReplaceGeneratedRegionRefusesMalformedOrAmbiguousBodies(t *testing.T) {
	// Each ambiguous shape is refused rather than guessed at: a guess rewrites or
	// deletes text outside the bot's report.
	live, _ := renderIssueBody("s3", "v1.41.5", "v1.44.0", sampleFindings())
	quoted, _ := renderIssueBody("ec2", "v1.41.5", "v1.44.0", sampleFindings())
	note := "\n> Note: `CreateGizmo` is intentionally unsupported.\n"
	// The live region with its end marker removed, which is what an accidental edit
	// of the bot's text most often looks like.
	noEnd := strings.Replace(live, generatedRegionEnd, "", 1)

	for name, body := range map[string]string{
		// No markers at all (the legacy format).
		"no markers":       "an older generated body\n" + note,
		"only a begin":     generatedRegionBegin + "\nno end marker\n" + note,
		"end marker gone":  noEnd + note,
		"stray begin":      live + note + "\n" + generatedRegionBegin + "\n",
		"quoted pair":      live + "\n\nQuoting the ec2 issue for comparison:\n\n" + quoted,
		"pair spliced in":  strings.Replace(live, "\n", "\n"+generatedRegionBegin+"\nx\n"+generatedRegionEnd+"\n", 1),
		"reversed":         generatedRegionEnd + "\n" + note + generatedRegionBegin + "\n",
		"begin inline":     "see " + live,
		"end inline":       strings.Replace(live, "\n"+generatedRegionEnd, "\nnote "+generatedRegionEnd, 1) + note,
		"trailing on line": strings.Replace(live, generatedRegionBegin+"\n", generatedRegionBegin+" hi\n", 1),
	} {
		t.Run(name, func(t *testing.T) {
			refreshed, _ := renderIssueBody("s3", "v1.44.0", "v1.45.0", sampleFindings()[:2])
			got, err := replaceGeneratedRegion(body, refreshed)
			require.Error(t, err)
			assert.ErrorIs(t, err, errUnmanageableRegion)
			assert.Empty(t, got)
		})
	}
}

func TestParseFingerprintStillAttributesAMalformedBody(t *testing.T) {
	// Refusing to rewrite a damaged body must not also disown it: an issue whose
	// fingerprint reads as "" is treated as somebody else's and a duplicate is filed.
	live, liveFP := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	noEnd := strings.Replace(live, generatedRegionEnd, "", 1)
	assert.Equal(t, liveFP, parseFingerprint(noEnd))
}

// issueFor builds an open bot-filed issue as listAPIChangeIssues returns it.
// serveListedIssues adds the labels, since only the refetch checks them.
func issueFor(number int, body string) *github.Issue {
	return &github.Issue{
		Number: github.Int(number),
		Body:   github.String(body),
		State:  github.String("open"),
		User:   &github.User{Login: github.String("ack-bot")},
	}
}

// staleIssueBody is a generated region for a different finding set from
// sampleFindings(), so reconcileIssue takes the refresh path.
func staleIssueBody() string {
	body, _ := renderIssueBody("demo", "v1.40.0", "v1.41.5", sampleFindings()[:1])
	return body
}

// serveListedIssues answers refetchManagedIssue's GET for each issue in existing with
// a copy that passes revalidation — open, authored by ack-bot, carrying the ownership
// label and service/<key> — and hands every other request to next.
func serveListedIssues(t *testing.T, existing map[string]*github.Issue, next http.Handler) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			for service, issue := range existing {
				if r.URL.Path != fmt.Sprintf("/repos/o/community/issues/%d", issue.GetNumber()) {
					continue
				}
				served := *issue
				served.State = github.String("open")
				served.User = &github.User{Login: github.String("ack-bot")}
				served.Labels = []*github.Label{
					{Name: github.String(apiChangeLabel)},
					{Name: github.String("service/" + service)},
				}
				require.NoError(t, json.NewEncoder(w).Encode(served))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
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
		// Echo the labels back as the real API does; createGithubIssueWithClient
		// refuses an issue returned without apiChangeLabel.
		filed := github.Issue{Number: github.Int(1)}
		for _, name := range payload.Labels {
			filed.Labels = append(filed.Labels, &github.Label{Name: github.String(name)})
		}
		require.NoError(t, json.NewEncoder(w).Encode(filed))
	}))

	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", sampleFindings(), nil, nil, testKnownServices("demo"), 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueCreated, outcome)
	assert.True(t, created)

	// Without apiChangeLabel the next run cannot find this issue and files a
	// duplicate; kind/api-change is what humans filter by.
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
		"demo", "v1.42.0", "v1.45.0", sampleFindings(), issueFor(42, body), nil, testKnownServices("demo"), 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueUnchanged, outcome)
}

func TestReconcileIssueUpdatesWhenFingerprintDiffers(t *testing.T) {
	var calls []string
	var commentBody string
	client := newTestGitHubClient(t, serveListedIssues(t, map[string]*github.Issue{"demo": issueFor(42, staleIssueBody())}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	})))

	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", sampleFindings(), issueFor(42, staleIssueBody()), nil, testKnownServices("demo"), 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueUpdated, outcome)

	// Comment before PATCH: the PATCH writes the fingerprint, so a failed comment
	// is retried (see TestReconcileIssueFailedCommentLeavesTheFingerprintStale).
	assert.Equal(t, []string{"comment", "patch"}, calls)

	// The comment notifies humans on a public repo: it names the service and both
	// versions, and must not claim new changes, since findings may have been resolved.
	assert.Contains(t, commentBody, "`demo`")
	assert.Contains(t, commentBody, "v1.41.5")
	assert.Contains(t, commentBody, "v1.44.0")
	assert.NotContains(t, commentBody, "New AWS API changes detected")
	// Future tense: the PATCH can still fail after this posts, so the comment must
	// not read as a refresh that already landed.
	assert.Contains(t, commentBody, "will be refreshed")
}

func TestReconcileIssueFailedPatchReportsNoOutcome(t *testing.T) {
	// Comment posted, then the PATCH fails: the outcome must be no-decision, or the
	// caller records a refresh that never happened.
	var calls []string
	client := newTestGitHubClient(t, serveListedIssues(t, map[string]*github.Issue{"demo": issueFor(42, staleIssueBody())}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	})))

	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", sampleFindings(), issueFor(42, staleIssueBody()), nil, testKnownServices("demo"), 10, 0, false)
	require.Error(t, err)
	assert.Equal(t, issueOutcomeNone, outcome)
	assert.Equal(t, []string{"comment", "patch"}, calls)
}

func TestReconcileIssueRefusesAnOversizedMergedBody(t *testing.T) {
	// An oversized merged body 422s every run and would re-notify daily, so it must
	// fail before the comment.
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a body that cannot be PATCHed must not notify first, got %s %s",
			r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))

	// Needs a real region, which replaceGeneratedRegion requires. The overflow
	// comes from text kept around the region, which renderIssueBody cannot see.
	region, _ := renderIssueBody("demo", "v1.40.0", "v1.41.5", sampleFindings()[:1])
	existingBody := region + "\n\n" + strings.Repeat("x", githubMaxIssueBody)

	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", sampleFindings(), issueFor(42, existingBody), nil, testKnownServices("demo"), 10, 0, false)
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
		"demo", "v1.41.5", "v1.44.0", sampleFindings(), nil, nil, testKnownServices("demo"), 10, 10, false)
	require.NoError(t, err)
	assert.Equal(t, issueSkippedAtCap, outcome)
}

func TestReconcileIssueCapStillAllowsUpdates(t *testing.T) {
	var patched bool
	// Strict about unexpected requests, so a create at the cap cannot pass.
	client := newTestGitHubClient(t, serveListedIssues(t, map[string]*github.Issue{"demo": issueFor(42, staleIssueBody())}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	})))

	// At the cap, but the issue already exists — updating does not grow the
	// count, so it must proceed.
	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", sampleFindings(), issueFor(42, staleIssueBody()), nil, testKnownServices("demo"), 10, 10, false)
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
		"demo", "v1.41.5", "v1.44.0", nil, nil, nil, testKnownServices("demo"), 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueUnchanged, outcome)
}

func TestReconcileIssueNoFindingsButAnOpenIssue(t *testing.T) {
	// No findings but an open issue: nothing is written (a detector bug looks the
	// same), but the outcome must be distinct because the issue holds a cap slot.
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no findings must mean no API calls, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))

	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", nil, issueFor(42, "a report with no current findings"),
		nil, testKnownServices("demo"), 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueStaleOpenIssue, outcome)
}

func TestReconcileIssueUpdatePreservesMaintainerText(t *testing.T) {
	// Guards that a refresh preserves text outside the generated region.
	region, _ := renderIssueBody("demo", "v1.40.0", "v1.41.5", sampleFindings()[:1])
	existingBody := region + "\n\n`CreateBucketMetadataTableConfiguration` is intentionally unsupported.\n"

	var patchedBody string
	client := newTestGitHubClient(t, serveListedIssues(t, map[string]*github.Issue{"demo": issueFor(42, existingBody)}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			var payload struct{ Body string }
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			patchedBody = payload.Body
		}
		fmt.Fprint(w, `{}`)
	})))

	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", sampleFindings(), issueFor(42, existingBody), nil, testKnownServices("demo"), 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueUpdated, outcome)
	assert.Contains(t, patchedBody, "intentionally unsupported")
	// The refreshed region really did land: "### Regenerate is enough" is absent from the
	// one-finding body this issue started with.
	assert.Contains(t, patchedBody, "### Regenerate is enough")
}

func TestReconcileIssueFailedCommentLeavesTheFingerprintStale(t *testing.T) {
	// A failed comment leaves the old fingerprint, so the next run retries. With the
	// PATCH first, one transient error would lose the notification.
	existing := issueFor(42, staleIssueBody())

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
		"demo", "v1.41.5", "v1.44.0", sampleFindings(), existing, nil, testKnownServices("demo"), 10, 0, false)
	require.Error(t, err)
	// No outcome at all, so a caller cannot record a decision that did not happen.
	assert.Equal(t, issueOutcomeNone, outcome)
	assert.Equal(t, []string{"POST /repos/o/community/issues/42/comments"}, dayOneCalls,
		"the body must not be patched once the notification has failed")

	var dayTwoCalls []string
	dayTwo := newTestGitHubClient(t, serveListedIssues(t, map[string]*github.Issue{"demo": existing}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dayTwoCalls = append(dayTwoCalls, r.Method+" "+r.URL.Path)
		fmt.Fprint(w, `{}`)
	})))

	// Same issue, unchanged because day 1 never wrote to it.
	outcome, err = reconcileIssue(context.Background(), dayTwo, "o", "community",
		"demo", "v1.41.5", "v1.44.0", sampleFindings(), existing, nil, testKnownServices("demo"), 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueUpdated, outcome)
	assert.Equal(t, []string{
		"POST /repos/o/community/issues/42/comments",
		"PATCH /repos/o/community/issues/42",
	}, dayTwoCalls, "the next run must retry the notification, not short-circuit")
}

func TestReconcileIssueCreateWithoutTheOwnershipLabelAborts(t *testing.T) {
	// Labels dropped on create (token lacks push access) leave an orphan issue. The
	// error must wrap errCannotLabelIssues and name it, so the caller can abort.
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"number": 7, "labels": []}`)
	}))

	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", sampleFindings(), nil, nil, testKnownServices("demo"), 10, 0, false)
	require.Error(t, err)
	assert.Equal(t, issueOutcomeNone, outcome)
	assert.True(t, errors.Is(err, errCannotLabelIssues),
		"the caller must be able to tell a credential problem from a per-service one: %v", err)
	assert.Contains(t, err.Error(), "#7", "the orphan's number belongs in the error")
}

func TestReconcileIssueRejectsANonPositiveCap(t *testing.T) {
	// A cap of 0 is refused rather than meaning unlimited: it is the only brake on
	// writes to a public repo.
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a bad cap must be refused before any request, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))

	for _, maxOpen := range []int{0, -1} {
		outcome, err := reconcileIssue(context.Background(), client, "o", "community",
			"demo", "v1.41.5", "v1.44.0", sampleFindings(), nil, nil, testKnownServices("demo"), maxOpen, 100000, false)
		require.Error(t, err, "maxOpen=%d", maxOpen)
		assert.Equal(t, issueOutcomeNone, outcome)
		assert.Contains(t, err.Error(), "must be positive")
	}

	// Refused even with nothing to file, so the misconfiguration surfaces on the
	// first service rather than on whichever one happens to have findings.
	_, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", nil, nil, nil, testKnownServices("demo"), 0, 0, false)
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
		map[string]bool{want: true}, testKnownServices("demo"), 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueSuppressedByClosed, outcome)
}

func TestReconcileIssueOpenIssueBeatsAClosedFingerprint(t *testing.T) {
	// A service with both takes the update path: the open issue is the live one.
	_, want := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	var patched bool
	client := newTestGitHubClient(t, serveListedIssues(t, map[string]*github.Issue{"demo": issueFor(42, staleIssueBody())}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			patched = true
		}
		fmt.Fprint(w, `{}`)
	})))

	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", sampleFindings(), issueFor(42, staleIssueBody()),
		map[string]bool{want: true}, testKnownServices("demo"), 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueUpdated, outcome)
	assert.True(t, patched)
}

func TestIssueOutcomeString(t *testing.T) {
	// Outcomes are logged, so each needs a readable name, the zero value included.
	assert.Equal(t, "no-decision", issueOutcomeNone.String())
	assert.Equal(t, "suppressed-by-closed-issue", issueSuppressedByClosed.String())
	assert.Equal(t, "unchanged", issueUnchanged.String())
	assert.Equal(t, "created", issueCreated.String())
	assert.Equal(t, "updated", issueUpdated.String())
	assert.Equal(t, "skipped-at-cap", issueSkippedAtCap.String())
	assert.Equal(t, "stale-open-issue", issueStaleOpenIssue.String())
	assert.Equal(t, "relabelled", issueRelabelled.String())

	// A member added without a case here still logs something traceable rather than
	// an empty string.
	assert.Equal(t, "unnamed-outcome-99", issueOutcome(99).String())
}

func TestIssueOutcomeValuesAreDistinct(t *testing.T) {
	// Outcome values must not collide.
	seen := map[issueOutcome]bool{}
	for _, o := range []issueOutcome{
		issueOutcomeNone, issueSuppressedByClosed, issueUnchanged,
		issueCreated, issueUpdated, issueSkippedAtCap, issueStaleOpenIssue, issueRelabelled,
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
	// Order matters: it decides which services win when the cap binds.
	assert.Equal(t, []string{"s3"}, services)
	// Returned only so the command can warn when the flag disagrees with it.
	assert.Equal(t, 1, configuredCap)
}

func TestGetAPINotificationConfigRejectsUnusableConfigs(t *testing.T) {
	// Each case aborts before any GitHub request. All are reachable from a
	// hand-edited jobs_config.yaml, since `make prow-gen` is not on that path.
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
		assert.Equal(t, []string{"v1.80.0"}, got)
	}
	assert.Len(t, listings, 1, "one series is listed once")

	got, err := cache.resolve(context.Background(), client, "ec2")
	require.NoError(t, err)
	assert.Equal(t, []string{"v1.300.0"}, got)
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

	// The tally must say the run stopped early, not read as a run that did nothing.
	assert.Contains(t, logged.String(), "1 of 3 services (run stopped early)")
}

// captureLog redirects the standard logger into a buffer for one test. The log
// is this job's only output, so log lines are the behaviour under test. The
// previous writer is restored, not os.Stderr, so nested captures work.
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
	// reconcileIssue never mutates openCount; only the caller's increment keeps
	// later services under the cap.
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

	// The tally is the only place per-outcome counts appear; its buckets must sum
	// to the services attempted.
	assert.Contains(t, logged.String(),
		"3 services: 2 created, 0 updated, 0 unchanged, 0 stale, 0 suppressed, "+
			"1 skipped at cap, 0 analysis failures, 0 write failures, 0 aborted")
}

func TestReconcileServicesReportsAStaleOpenIssue(t *testing.T) {
	// No findings but an open issue must still reach reconcileIssue, or
	// issueStaleOpenIssue is unreachable and its cap slot leaks silently.
	logged := captureLog(t)
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a stale open issue must not be written to, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	analyze := func(_ context.Context, service string) ([]Finding, string, string, error) {
		return nil, "v1.41.5", "v1.44.0", nil
	}

	existing := map[string]*github.Issue{"svc1": issueFor(42, staleIssueBody())}
	analysisFailures, writeFailures, skipped, err := reconcileServices(
		context.Background(), client, "o", "community",
		[]string{"svc1"}, existing, nil, 10, 1, analyze, false, "")
	require.NoError(t, err)
	assert.Empty(t, analysisFailures)
	assert.Empty(t, writeFailures)
	assert.Empty(t, skipped)

	// This outcome's only output is the log line.
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
	assert.Equal(t, plain, strings.Replace(body, droppedBlock(droppedFindings(), math.MaxInt), "", 1))
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
	assert.Equal(t, plain, strings.Replace(body, droppedBlock(all, math.MaxInt), "", 1))
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
		// An Update's response is not durable read-back: the controller never
		// observes the value again, so it cannot reconcile it.
		{"sent at Update and echoed only by it", []use{{OpTypeUpdate, false}, {OpTypeUpdate, true}}, ClassLifecycleField},
		{"sent at Update, echoed by it, and read", []use{{OpTypeUpdate, false}, {OpTypeUpdate, true}, {OpTypeGet, true}}, ClassSpecField},
		{"sent at Update and returned by Create", []use{{OpTypeUpdate, false}, {OpTypeCreate, true}}, ClassLifecycleField},
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
				r.add(OpTypes{u.op}, "", u.isOutput)
			}
			assert.Equal(t, tc.want, r.class())
		})
	}
}

func TestFieldRolesMultiRoleOperation(t *testing.T) {
	// operation_type: [Create, Update] sends the field in both roles.
	r := &fieldRoles{}
	r.add(OpTypes{OpTypeCreate, OpTypeUpdate}, "PutWidget", false)
	assert.True(t, r.createInput)
	assert.True(t, r.updateInput)

	// An unclassified operation's response is a read when its verb says so.
	r = &fieldRoles{}
	r.add(OpTypes{OpTypeUpdate}, "ModifyWidget", false)
	r.add(OpTypes{OpTypeUnknown}, "DescribeWidgetSettings", true)
	assert.Equal(t, ClassSpecField, r.class())
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
		"These predate the model the controller was generated from, so they do not drive this notification.\n\n"+
		"- Instance Spec field `SecondaryInterfaces` — `RunInstances`\n"+
		"- resource `SecondaryNetwork` — `CreateSecondaryNetwork`, `DeleteSecondaryNetwork`\n")
	assert.NotContains(t, body, "## Resource: SecondaryNetwork")
	assert.Equal(t, plainFingerprint, fingerprint, "pre-existing findings do not drive the notification")
	assert.Equal(t, plain, strings.Replace(body, preexistingBlock(append(sampleFindings(), old...), "service/demo/v1.290.1", math.MaxInt), "", 1))
	assert.Empty(t, reportable(old))
}

func TestCollectFindingsCountsGapsSinceTheGenerationPin(t *testing.T) {
	// ec2: generated at core v1.41.1 while go.mod requires a newer service/ec2.
	// Color is in that newer release but not the generation model, so it is new.
	generation := opsModel(t, map[string][]string{"CreateWidget": {"Name"}})
	latest := opsModel(t, map[string][]string{"CreateWidget": {"Name", "Color", "Size"}})
	in := &ControllerInputs{
		Config:       &generatorConfig{},
		CRDFields:    map[string]map[string]bool{"Widget": {"name": true}},
		UsedOps:      map[string]map[string]bool{"widget": {"CreateWidget": true}},
		kindsByLower: map[string]string{"widget": "Widget"},
	}
	got := collectFindings(latest, generation, in)
	require.Len(t, got, 2)
	for _, f := range got {
		assert.True(t, f.NewSincePin, f.Subject)
	}
	assert.Equal(t, []string{"Color", "Size"}, subjects(got, ClassSpecField))
}

func TestReconcileIssueFilesNothingForDroppedOperationsAlone(t *testing.T) {
	// A service whose only change is a new Start* operation has nothing to report.
	// A nil client panics on any request, which is the assertion that none is made.
	outcome, err := reconcileIssue(context.Background(), nil, "o", "community", "demo",
		"v1.41.5", "v1.44.0", droppedFindings(), nil, nil, testKnownServices("demo"), 1, 0, false)
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
	// Prow shows one error as the failure reason, so it must carry every reason: a
	// binding cap must not hide controller failures, nor an abort earlier ones.
	assert.NoError(t, runError(nil, 1, nil, nil, nil, false))

	// Pinned whole because order matters: deck truncates the description, so
	// failures come first and the expected cap reason and its advice last.
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
	// An abort on any service, including the last, must report that the run
	// stopped and count the aborting service in a bucket.
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

			// The scope names how far the run got, and the buckets (abortAt-1 created plus
			// one aborted) sum to the services attempted.
			assert.Contains(t, logged.String(), fmt.Sprintf(
				"%d of 3 services (run stopped early): %d created, 0 updated, 0 unchanged, "+
					"0 stale, 0 suppressed, 0 skipped at cap, 0 analysis failures, "+
					"0 write failures, 1 aborted",
				abortAt, abortAt-1))
		})
	}
}

func TestLatestVersionCacheMemoisesFailures(t *testing.T) {
	// A failed listing (likely a rate-limit 403) is cached per series, so it is not
	// repeated for every service. Each service still records its own failure.
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
	// A config typo must fail once, up front, not after a full analysis.
	// captureLog because detectAPIChanges sets the log prefix, which would otherwise
	// leak into later tests.
	_ = captureLog(t)

	defer func(p string, c int) { OptJobsConfigPath, OptMaxOpenIssues = p, c }(
		OptJobsConfigPath, OptMaxOpenIssues)
	OptJobsConfigPath, OptMaxOpenIssues = "../../../jobs_config.yaml", 0

	err := detectAPIChanges(nil, nil)
	require.ErrorContains(t, err, "--max-open-issues must be positive")
}

func TestDetectAPIChangesWarnsWhenTheCapDisagreesWithTheConfig(t *testing.T) {
	// A maintainer may raise api_notification_max_open_issues without the flag;
	// the mismatch must be logged.
	defer func(p string, c int) { OptJobsConfigPath, OptMaxOpenIssues = p, c }(
		OptJobsConfigPath, OptMaxOpenIssues)
	OptJobsConfigPath, OptMaxOpenIssues = "../../../jobs_config.yaml", 5
	// Cleared so a token in the environment cannot reach api.github.com.
	t.Setenv("GITHUB_TOKEN", "")

	logged := captureLog(t)
	// The warning precedes client creation, so the missing-token error is expected.
	require.Error(t, detectAPIChanges(nil, nil))

	assert.Contains(t, logged.String(),
		"WARNING --max-open-issues is 5 but api_notification_max_open_issues is 1")
}

// lastTallyLine returns the last closing-tally line in a captured log, so a
// test can capture two runs in one buffer.
func lastTallyLine(logged string) string {
	last := ""
	for _, line := range strings.Split(logged, "\n") {
		if strings.Contains(line, " services: ") {
			last = line
		}
	}
	return last
}

// tallyNumbers extracts the integers from a tally line, the only part that is
// comparable across live and dry-run wording.
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
	// A dry run must make no writes.
	existing := map[string]*github.Issue{
		"svc2": issueFor(42, staleIssueBody()),
		"svc3": issueFor(43, "whatever"),
	}
	client := newTestGitHubClient(t, serveListedIssues(t, existing, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("dry run must make no request beyond re-reading the issue, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	})))

	analyze := dryRunAnalyzer(map[string][]Finding{
		"svc1": sampleFindings(), // would create
		"svc2": sampleFindings(), // would update (has an existing issue)
		"svc3": nil,              // stale: existing issue, no findings
	})

	analysisFailures, writeFailures, skipped, err := reconcileServices(
		context.Background(), client, "o", "community",
		[]string{"svc1", "svc2", "svc3"}, existing, nil, 10, 0, analyze, true, "")
	require.NoError(t, err)
	assert.Empty(t, analysisFailures)
	assert.Empty(t, writeFailures)
	assert.Empty(t, skipped)
}

// dryRunEveryOutcomeFixture has one service per issueOutcome plus the two the
// loop decides before reconcileIssue. Order matters: svccreate comes first so
// it takes the single cap slot and svccapped is refused.
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

	// Rendered rather than hashed with fingerprintFindings, because renderIssueBody
	// dedupes before hashing; otherwise svcunchanged lands in update.
	unchangedBody, _ := renderIssueBody("svcunchanged", "v1.41.5", "v1.44.0", sampleFindings())

	// A real region (replaceGeneratedRegion requires one) for a different finding
	// subset, plus a note too large to merge, so the run reaches the size guard.
	oversizedRegion, _ := renderIssueBody("svcoversized", "v1.40.0", "v1.41.5", sampleFindings()[:1])
	oversizedBody := oversizedRegion + "\n\n" + strings.Repeat("x", githubMaxIssueBody)

	existing = map[string]*github.Issue{
		"svcupdate":    issueFor(42, staleIssueBody()),
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
	// A dry run must reach the same decisions as a live run over the same inputs.
	services, existing, closed, analyze := dryRunEveryOutcomeFixture()

	liveClient := newTestGitHubClient(t, serveListedIssues(t, existing, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues") {
			fmt.Fprintf(w, `{"number": 1, "labels": [{"name": %q}]}`, apiChangeLabel)
			return
		}
		fmt.Fprint(w, `{}`)
	})))
	dryClient := newTestGitHubClient(t, serveListedIssues(t, existing, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("dry run must not write, got %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{}`)
	})))

	// Cap of 1: svccreate takes the slot and svccapped is refused, in both modes.
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

	// All three slices: writeFailures is the only place the oversized-merge guard
	// shows up.
	assert.Equal(t, liveSkipped, drySkipped, "the cap must bind identically")
	assert.Equal(t, liveWrites, dryWrites, "the oversize guard must fire identically")
	assert.Equal(t, liveAnalysis, dryAnalysis)
	assert.Equal(t, []string{"svccapped"}, liveSkipped)
	assert.Equal(t, []string{"svcoversized"}, liveWrites)

	// Pinned to expected values, since two missing tallies would compare equal. 8
	// services: created, refreshed, 2 unchanged, stale, suppressed, capped, and a
	// write failure.
	require.NotEmpty(t, liveSummary, "no tally line in the live log")
	require.NotEmpty(t, drySummary, "no tally line in the dry-run log")
	assert.Equal(t, []int{8, 1, 1, 2, 1, 1, 1, 0, 1, 0}, tallyNumbers(liveSummary))
	// The dry summary says "would" where the live one says what it did, so compare the
	// numbers rather than the prose.
	assert.Equal(t, tallyNumbers(liveSummary), tallyNumbers(drySummary))

	// Checked on the tally line: dryRunCaveat also starts with "DRY RUN ", so a
	// whole-log check could not fail.
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

	// Each service must take the branch it is named for; the tally alone cannot
	// show one moving between buckets. Same wording in both modes.
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
	// A non-positive cap is rejected in dry-run too, both in detectAPIChanges and
	// per service in reconcileIssue.
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a bad cap must be refused before any request, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	for _, maxOpen := range []int{0, -1} {
		outcome, err := reconcileIssue(context.Background(), client, "o", "community",
			"demo", "v1.41.5", "v1.44.0", sampleFindings(), nil, nil, testKnownServices("demo"), maxOpen, 0, true)
		require.Error(t, err, "maxOpen=%d under dry-run", maxOpen)
		assert.Equal(t, issueOutcomeNone, outcome)
		assert.Contains(t, err.Error(), "must be positive")
	}

	// Captured so detectAPIChanges' log prefix does not leak into later tests.
	_ = captureLog(t)
	defer func(p string, c int, d bool) { OptJobsConfigPath, OptMaxOpenIssues, OptDryRun = p, c, d }(
		OptJobsConfigPath, OptMaxOpenIssues, OptDryRun)
	OptJobsConfigPath, OptMaxOpenIssues, OptDryRun = "../../../jobs_config.yaml", 0, true

	require.ErrorContains(t, detectAPIChanges(nil, nil), "--max-open-issues must be positive")
}

func TestDetectAPIChangesRejectsAnOutputDirWithoutDryRun(t *testing.T) {
	// --dry-run-output-dir without --dry-run must be rejected, or a would-be
	// preview writes to the public repo. captureLog: see
	// TestDetectAPIChangesRejectsNonPositiveCap.
	_ = captureLog(t)
	defer func(p string, c int, d bool, o string) {
		OptJobsConfigPath, OptMaxOpenIssues, OptDryRun, OptDryRunOutputDir = p, c, d, o
	}(OptJobsConfigPath, OptMaxOpenIssues, OptDryRun, OptDryRunOutputDir)
	OptJobsConfigPath, OptMaxOpenIssues = "../../../jobs_config.yaml", 1
	OptDryRun, OptDryRunOutputDir = false, t.TempDir()

	err := detectAPIChanges(nil, nil)
	require.ErrorContains(t, err, "--dry-run-output-dir is only meaningful with --dry-run")

	// With --dry-run it passes the guard and stops at the missing token.
	// GITHUB_TOKEN is cleared so this cannot reach api.github.com.
	t.Setenv("GITHUB_TOKEN", "")
	OptDryRun = true
	err = detectAPIChanges(nil, nil)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "--dry-run-output-dir")
}

func TestReconcileServicesDryRunWritesBodiesForReview(t *testing.T) {
	// The preview file must hold the bytes that would be posted: the rendered body
	// on create, and the merged body (region plus maintainer text) on refresh.
	dir := t.TempDir()
	logged := captureLog(t)

	// A region carrying a different finding set, so the fingerprint does not match and
	// the run takes the refresh path, plus the note this is all about.
	const maintainerNote = "CreateGizmo is intentionally unsupported; see #1234."
	oldRegion, _ := renderIssueBody("svcrefresh", "v1.40.0", "v1.41.5", sampleFindings()[:1])
	existingBody := oldRegion + "\n" + maintainerNote + "\n"

	analyze := dryRunAnalyzer(map[string][]Finding{
		"svc1": sampleFindings(), "svc2": nil, "svcrefresh": sampleFindings(),
	})
	existing := map[string]*github.Issue{"svcrefresh": issueFor(42, existingBody)}
	client := newTestGitHubClient(t, serveListedIssues(t, existing, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("dry run must make no request beyond re-reading the issue, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	})))
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
	merged, err := replaceGeneratedRegion(existingBody, region)
	require.NoError(t, err)
	assert.Equal(t, merged, string(refreshed),
		"a refresh posts the region spliced into the existing body")
	// Checked separately in case replaceGeneratedRegion stops preserving human text.
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
	// For an oversized merge the file shows the merged body that could not be
	// posted, so it is written before reconcileIssue decides. The service still
	// fails the run.
	dir := t.TempDir()

	oldRegion, _ := renderIssueBody("svcoversized", "v1.40.0", "v1.41.5", sampleFindings()[:1])
	existingBody := oldRegion + "\n\n" + strings.Repeat("x", githubMaxIssueBody)
	existing := map[string]*github.Issue{"svcoversized": issueFor(42, existingBody)}
	client := newTestGitHubClient(t, serveListedIssues(t, existing, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("dry run must make no request beyond re-reading the issue, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	})))

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

// TestDryRunStatesWhatItCannotCheck: the dry-run caveats are logged once per
// run, and not in live mode. Label permission is checked only after a real POST,
// so a clean dry run does not prove the token can label.
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
	// Text-only changes (e.g. new annotations) must reach the open issue even when
	// the finding set, and so the fingerprint, is unchanged.
	findings := sampleFindings()
	filed, _ := renderIssueBody("demo", "v1.41.5", "v1.44.0", findings)
	findings[0].Detail, findings[0].Work = createOnlyDetail, workCreateOnly

	var patched bool
	listed := map[string]*github.Issue{"demo": issueFor(42, filed)}
	client := newTestGitHubClient(t, serveListedIssues(t, listed, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == "/repos/o/community/issues/42" {
			patched = true
			fmt.Fprint(w, `{}`)
			return
		}
		t.Errorf("only a silent PATCH expected, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	})))

	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.42.0", "v1.45.0", findings, issueFor(42, filed), nil, testKnownServices("demo"), 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueUpdated, outcome)
	assert.True(t, patched)
}

func TestReconcileIssueRefusesAMalformedRegion(t *testing.T) {
	// A region with its end marker removed must fail before any request, on both
	// the refresh and the reword path.
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a malformed body must not be written to, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	note := "\n> Note: `CreateGizmo` is intentionally unsupported.\n"

	// Refresh: a different finding set.
	damaged := strings.Replace(staleIssueBody(), generatedRegionEnd, "", 1) + note
	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", sampleFindings(), issueFor(42, damaged), nil, testKnownServices("demo"), 10, 0, false)
	require.Error(t, err)
	assert.ErrorIs(t, err, errUnmanageableRegion)
	assert.Equal(t, issueOutcomeNone, outcome)

	// Reword: the same finding set, so the fingerprint still matches.
	filed, _ := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	damaged = strings.Replace(filed, generatedRegionEnd, "", 1) + note
	outcome, err = reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", sampleFindings(), issueFor(42, damaged), nil, testKnownServices("demo"), 10, 0, false)
	require.Error(t, err)
	assert.ErrorIs(t, err, errUnmanageableRegion)
	assert.Equal(t, issueOutcomeNone, outcome)
}

func TestReconcileServicesMergesIntoTheIssueAsItIsNow(t *testing.T) {
	// A note added after the listing must survive: the merge uses the issue as it
	// is at write time.
	const lateNote = "Added by a maintainer while the run was analysing models."
	listed := map[string]*github.Issue{"demo": issueFor(42, staleIssueBody())}
	current := map[string]*github.Issue{"demo": issueFor(42, staleIssueBody()+"\n"+lateNote+"\n")}

	var patchedBody string
	var calls []string
	client := newTestGitHubClient(t, serveListedIssues(t, current, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodPatch {
			var payload struct{ Body string }
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			patchedBody = payload.Body
		}
		fmt.Fprint(w, `{}`)
	})))
	analyze := dryRunAnalyzer(map[string][]Finding{"demo": sampleFindings()})

	_, writeFailures, _, err := reconcileServices(context.Background(), client, "o", "community",
		[]string{"demo"}, listed, nil, 10, 1, analyze, false, "")
	require.NoError(t, err)
	assert.Empty(t, writeFailures)
	assert.Equal(t, []string{
		"POST /repos/o/community/issues/42/comments",
		"PATCH /repos/o/community/issues/42",
	}, calls)
	assert.Contains(t, patchedBody, lateNote, "the note added after the listing must survive")
}

func TestReconcileServicesRevalidatesTheIssueBeforeRefreshing(t *testing.T) {
	// Each of these happened between the listing and the write, and each is a reason
	// the listing would not have handed the issue over. None may be written to.
	for name, tweak := range map[string]func(*github.Issue){
		"closed":              func(i *github.Issue) { i.State = github.String("closed") },
		"ownership unlabeled": func(i *github.Issue) { i.Labels = i.Labels[1:] },
		"service relabeled":   func(i *github.Issue) { i.Labels = i.Labels[:1] },
		"other author":        func(i *github.Issue) { i.User = &github.User{Login: github.String("someone")} },
		"fingerprint removed": func(i *github.Issue) {
			i.Body = github.String(fingerprintRE.ReplaceAllString(i.GetBody(), ""))
		},
	} {
		t.Run(name, func(t *testing.T) {
			listed := issueFor(42, staleIssueBody())
			client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/repos/o/community/issues/42" {
					t.Errorf("only the re-read is allowed, got %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				fresh := *listed
				fresh.Labels = []*github.Label{
					{Name: github.String(apiChangeLabel)}, {Name: github.String("service/demo")},
				}
				tweak(&fresh)
				require.NoError(t, json.NewEncoder(w).Encode(fresh))
			}))
			analyze := dryRunAnalyzer(map[string][]Finding{"demo": sampleFindings()})

			_, writeFailures, _, err := reconcileServices(context.Background(), client, "o", "community",
				[]string{"demo"}, map[string]*github.Issue{"demo": listed}, nil, 10, 1, analyze, false, "")
			require.NoError(t, err)
			assert.Equal(t, []string{"demo"}, writeFailures)
		})
	}
}

func TestReconcileServicesCountsAnIndeterminateCreateAgainstTheCap(t *testing.T) {
	// A create whose response is lost may still have been committed, so it counts
	// toward the cap.
	var creates int
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		creates++
		if creates == 1 {
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprint(w, `{"message": "Bad Gateway"}`)
			return
		}
		fmt.Fprintf(w, `{"number": %d, "labels": [{"name": %q}]}`, creates, apiChangeLabel)
	}))
	analyze := func(_ context.Context, service string) ([]Finding, string, string, error) {
		return sampleFindings(), "v1.41.5", "v1.44.0", nil
	}

	_, writeFailures, skipped, err := reconcileServices(context.Background(), client, "o", "community",
		[]string{"svc1", "svc2", "svc3"}, nil, nil, 2, 0, analyze, false, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"svc1"}, writeFailures)
	assert.Equal(t, 2, creates, "the lost create holds a slot, so only one more may be filed")
	assert.Equal(t, []string{"svc3"}, skipped)
}

func TestReconcileServicesDoesNotCountARejectedCreate(t *testing.T) {
	// A 4xx is sent before anything is committed, so it holds no slot.
	var creates int
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		creates++
		if creates == 1 {
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"message": "Validation Failed"}`)
			return
		}
		fmt.Fprintf(w, `{"number": %d, "labels": [{"name": %q}]}`, creates, apiChangeLabel)
	}))
	analyze := func(_ context.Context, service string) ([]Finding, string, string, error) {
		return sampleFindings(), "v1.41.5", "v1.44.0", nil
	}

	_, writeFailures, skipped, err := reconcileServices(context.Background(), client, "o", "community",
		[]string{"svc1", "svc2", "svc3"}, nil, nil, 2, 0, analyze, false, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"svc1"}, writeFailures)
	assert.Equal(t, 3, creates)
	assert.Empty(t, skipped)
}

// refreshAfterComment runs a fingerprint-changing refresh of issue 42 against a
// server whose GET serves what edit makes of the issue, as if changed during the
// comment. It returns the request methods in order and the PATCHed body, or "".
func refreshAfterComment(t *testing.T, edit func(*github.Issue)) ([]string, string, error) {
	t.Helper()
	listed := issueFor(42, staleIssueBody())
	var calls []string
	var patchedBody string
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method)
		switch r.Method {
		case http.MethodGet:
			served := *listed
			served.Labels = []*github.Label{
				{Name: github.String(apiChangeLabel)},
				{Name: github.String("service/demo")},
			}
			edit(&served)
			require.NoError(t, json.NewEncoder(w).Encode(served))
		case http.MethodPatch:
			var payload struct{ Body string }
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			patchedBody = payload.Body
			fmt.Fprint(w, `{}`)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	_, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", sampleFindings(), listed, nil, testKnownServices("demo"), 10, 0, false)
	return calls, patchedBody, err
}

func TestReconcileIssueMergesIntoTheBodyAsItIsAfterTheComment(t *testing.T) {
	// A maintainer note added while the comment was posted must survive the PATCH.
	calls, patched, err := refreshAfterComment(t, func(issue *github.Issue) {
		issue.Body = github.String(issue.GetBody() + "\n\nNote added during the comment.\n")
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"POST", "GET", "PATCH"}, calls)
	assert.Contains(t, patched, "Note added during the comment.")
	assert.Contains(t, patched, "### Regenerate is enough", "the refreshed region must land")
}

func TestReconcileIssueSkipsThePatchWhenTheIssueChangedDuringTheComment(t *testing.T) {
	// No PATCH may overwrite a maintainer's decision; closing in particular must not
	// leave the closed issue carrying the new fingerprint, which would suppress it.
	for name, edit := range map[string]func(*github.Issue){
		"closed": func(issue *github.Issue) { issue.State = github.String("closed") },
		"ownership label removed": func(issue *github.Issue) {
			issue.Labels = []*github.Label{{Name: github.String("service/demo")}}
		},
		"fingerprint edited": func(issue *github.Issue) {
			other, _ := renderIssueBody("demo", "v1.40.0", "v1.41.5", sampleFindings()[2:3])
			issue.Body = github.String(other)
		},
	} {
		t.Run(name, func(t *testing.T) {
			calls, patched, err := refreshAfterComment(t, edit)
			require.Error(t, err)
			assert.ErrorContains(t, err, "commented on issue o/community#42 but left its body as is")
			assert.Equal(t, []string{"POST", "GET"}, calls)
			assert.Empty(t, patched)
		})
	}
}

func TestReconcileServicesStopsOnceTheContextIsDone(t *testing.T) {
	// The run deadline must bound the loop itself: analysis scans a checkout before
	// any request could notice the cancellation.
	logged := captureLog(t)
	ctx, cancel := context.WithCancel(context.Background())
	var analyzed []string
	analyze := func(_ context.Context, service string) ([]Finding, string, string, error) {
		analyzed = append(analyzed, service)
		cancel()
		return nil, "v1.41.5", "v1.44.0", nil
	}

	_, _, _, err := reconcileServices(ctx, nil, "o", "community",
		[]string{"svc1", "svc2", "svc3"}, nil, nil, 10, 0, analyze, false, "")
	require.ErrorIs(t, err, context.Canceled)
	assert.ErrorContains(t, err, "run cut short while analysing svc1, 2 of 3 services not attempted")
	assert.Equal(t, []string{"svc1"}, analyzed)
	assert.Contains(t, logged.String(), "1 of 3 services (run cut short: context canceled): ")

	// Cancelled while analysing the last service: there is no next iteration to
	// notice, so the run must still fail rather than return nil.
	logged.Reset()
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	analyzed = nil
	_, _, _, err = reconcileServices(ctx, nil, "o", "community",
		[]string{"svc1"}, nil, nil, 10, 0, analyze, false, "")
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []string{"svc1"}, analyzed)
	assert.Contains(t, logged.String(), "1 of 1 services (run cut short: context canceled): ")

	// Already cancelled: nothing is analyzed.
	analyzed = nil
	_, _, _, err = reconcileServices(ctx, nil, "o", "community",
		[]string{"svc1"}, nil, nil, 10, 0, analyze, false, "")
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, analyzed)
}

// orphanSearchHandler serves findUnlabelledIssue's search with page and records
// every other request in calls, answering it with respond.
func orphanSearchHandler(t *testing.T, page string, calls *[]string, respond http.HandlerFunc) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isUnlabelledIssueSearch(r) {
			fmt.Fprint(w, page)
			return
		}
		*calls = append(*calls, r.Method+" "+r.URL.Path)
		respond(w, r)
	})
}

// orphanPage is a search page holding one open, unlabelled issue with body.
func orphanPage(body string) string {
	return fmt.Sprintf(`{"total_count": 1, "incomplete_results": false, "items": [
		{"number": 7, "state": "open", "body": %q, "labels": []}]}`, body)
}

func TestReconcileIssueLabelsAnUnlabelledIssueInsteadOfFilingAnother(t *testing.T) {
	// A retry after GitHub dropped the labels must find the orphan and label it,
	// not file a second issue the listing cannot see either.
	filed, _ := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	var calls []string
	var sentLabels []string
	client := newRawTestGitHubClient(t, orphanSearchHandler(t, orphanPage(filed), &calls,
		func(w http.ResponseWriter, r *http.Request) {
			require.NoError(t, json.NewDecoder(r.Body).Decode(&sentLabels))
			fmt.Fprintf(w, `[{"name": %q}, {"name": "service/demo"}]`, apiChangeLabel)
		}))

	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", sampleFindings(), nil, nil, testKnownServices("demo"), 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueRelabelled, outcome)
	assert.Equal(t, []string{"POST /repos/o/community/issues/7/labels"}, calls, "no second create")
	assert.Contains(t, sentLabels, apiChangeLabel)
	assert.Contains(t, sentLabels, "service/demo")
}

func TestReconcileIssueAbortsWhenAnUnlabelledIssueCannotBeLabelled(t *testing.T) {
	// The token still cannot label: abort with errCannotLabelIssues and file nothing.
	filed, _ := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	for name, respond := range map[string]http.HandlerFunc{
		"refused": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message": "Must have push access"}`)
		},
		"silently dropped": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `[]`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			var calls []string
			client := newRawTestGitHubClient(t, orphanSearchHandler(t, orphanPage(filed), &calls, respond))
			outcome, err := reconcileIssue(context.Background(), client, "o", "community",
				"demo", "v1.41.5", "v1.44.0", sampleFindings(), nil, nil, testKnownServices("demo"), 10, 0, false)
			require.ErrorIs(t, err, errCannotLabelIssues)
			assert.Equal(t, issueOutcomeNone, outcome)
			assert.Equal(t, []string{"POST /repos/o/community/issues/7/labels"}, calls, "no second create")
		})
	}
}

func TestReconcileIssueFilesWhenTheSearchHitIsNotTheOrphan(t *testing.T) {
	// Search matches tokens, so hits are re-checked locally: another fingerprint,
	// or an issue that has the label, does not block the create.
	other, _ := renderIssueBody("demo", "v1.40.0", "v1.41.5", sampleFindings()[:1])
	filed, _ := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	page := fmt.Sprintf(`{"total_count": 2, "incomplete_results": false, "items": [
		{"number": 7, "state": "open", "body": %q, "labels": []},
		{"number": 8, "state": "open", "body": %q, "labels": [{"name": %q}]}]}`, other, filed, apiChangeLabel)
	var calls []string
	client := newRawTestGitHubClient(t, orphanSearchHandler(t, page, &calls, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"number": 9, "labels": [{"name": %q}]}`, apiChangeLabel)
	}))

	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", sampleFindings(), nil, nil, testKnownServices("demo"), 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueCreated, outcome)
	assert.Equal(t, []string{"POST /repos/o/community/issues"}, calls)
}

func TestReconcileServicesCountsARelabelledIssueAgainstTheCap(t *testing.T) {
	// The orphan was missing from the listing, so labelling it takes a cap slot.
	svc1Body, svc1FP := renderIssueBody("svc1", "v1.41.5", "v1.44.0", sampleFindings())
	var calls []string
	client := newRawTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isUnlabelledIssueSearch(r) {
			// Only svc1 has an orphan.
			if strings.Contains(r.URL.Query().Get("q"), svc1FP) {
				fmt.Fprint(w, orphanPage(svc1Body))
			} else {
				fmt.Fprint(w, emptySearchPage)
			}
			return
		}
		calls = append(calls, r.Method+" "+r.URL.Path)
		fmt.Fprintf(w, `[{"name": %q}]`, apiChangeLabel)
	}))
	analyze := func(_ context.Context, service string) ([]Finding, string, string, error) {
		return sampleFindings(), "v1.41.5", "v1.44.0", nil
	}

	_, writeFailures, skipped, err := reconcileServices(context.Background(), client, "o", "community",
		[]string{"svc1", "svc2"}, nil, nil, 1, 0, analyze, false, "")
	require.NoError(t, err)
	assert.Empty(t, writeFailures)
	assert.Equal(t, []string{"svc2"}, skipped)
	assert.Equal(t, []string{"POST /repos/o/community/issues/7/labels"}, calls)
}

func TestReconcileIssueRewordRevalidatesBeforePatching(t *testing.T) {
	// The same-fingerprint rewrite re-reads the issue right before its PATCH, like
	// the refresh path: a maintainer note added since the listing survives, and a
	// close since then stops the write.
	filed, _ := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	withOp := append(sampleFindings(),
		Finding{Kind: "Widget", Class: ClassNewOperation, Subject: "TagWidget", NewSincePin: true})
	noted := filed + "\nmaintainer note added after the listing\n"

	t.Run("keeps an edit made since the listing", func(t *testing.T) {
		var patchedBody string
		client := newTestGitHubClient(t, serveListedIssues(t, map[string]*github.Issue{"demo": issueFor(42, noted)},
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload struct{ Body string }
				require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
				patchedBody = payload.Body
				fmt.Fprint(w, `{}`)
			})))
		outcome, err := reconcileIssue(context.Background(), client, "o", "community",
			"demo", "v1.41.5", "v1.44.0", withOp, issueFor(42, filed), nil, testKnownServices("demo"), 10, 0, false)
		require.NoError(t, err)
		assert.Equal(t, issueUpdated, outcome)
		assert.Contains(t, patchedBody, "maintainer note added after the listing")
		assert.Contains(t, patchedBody, "TagWidget")
	})

	t.Run("refuses an issue closed since the listing", func(t *testing.T) {
		client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Errorf("a closed issue must not be patched, got %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			closed := issueFor(42, filed)
			closed.State = github.String("closed")
			closed.Labels = []*github.Label{{Name: github.String(apiChangeLabel)}, {Name: github.String("service/demo")}}
			require.NoError(t, json.NewEncoder(w).Encode(closed))
		}))
		outcome, err := reconcileIssue(context.Background(), client, "o", "community",
			"demo", "v1.41.5", "v1.44.0", withOp, issueFor(42, filed), nil, testKnownServices("demo"), 10, 0, false)
		require.Error(t, err)
		assert.ErrorContains(t, err, "not rewording")
		assert.Equal(t, issueOutcomeNone, outcome)
	})
}

func TestParseComparedVersionsReadsOnlyTheGeneratedRegion(t *testing.T) {
	region, _ := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	quote := "\nCompared aws-sdk-go-v2 v0.0.1 -> v0.0.2\n"

	baseline, latest, ok := parseComparedVersions(quote + region + quote)
	require.True(t, ok)
	assert.Equal(t, "v1.41.5", baseline, "a maintainer's quote outside the region is not the footer")
	assert.Equal(t, "v1.44.0", latest)

	_, _, ok = parseComparedVersions(quote)
	assert.False(t, ok, "no region, no footer")

	twice := strings.Replace(region, generatedRegionEnd, strings.TrimPrefix(quote, "\n")+generatedRegionEnd, 1)
	_, _, ok = parseComparedVersions(twice)
	assert.False(t, ok, "two comparison lines in the region are ambiguous")

	_, _, ok = parseComparedVersions(strings.ReplaceAll(region, "\n", "\r\n"))
	assert.True(t, ok, "a body saved from the web UI is CRLF")

	// A core generation pin against a per-service latest tag.
	mixed, _ := renderIssueBody("ec2", "v1.41.1", "service/ec2/v1.338.1", sampleFindings())
	baseline, latest, ok = parseComparedVersions(mixed)
	require.True(t, ok)
	assert.Equal(t, "v1.41.1", baseline)
	assert.Equal(t, "service/ec2/v1.338.1", latest)
}

func TestReconcileIssueIgnoresAQuotedFooterOutsideTheRegion(t *testing.T) {
	// A quoted comparison line above the region used to be read as the footer, so
	// every run re-rendered at the wrong versions and PATCHed silently.
	region, _ := renderIssueBody("demo", "v1.41.5", "v1.44.0", sampleFindings())
	body := "Last triaged against\nCompared aws-sdk-go-v2 v0.0.1 -> v0.0.2\n\n" + region
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("an up-to-date issue must not be written, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))

	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.42.0", "v1.45.0", sampleFindings(), issueFor(42, body), nil, testKnownServices("demo"), 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueUnchanged, outcome)
}

func TestReconcileIssueRefreshTruncatesToFitTextOutsideTheRegion(t *testing.T) {
	// The region is rendered into what the text outside it leaves, measured on the
	// final GET, so a long maintainer note truncates the report instead of failing.
	var findings []Finding
	for i := 0; i < 300; i++ {
		findings = append(findings, Finding{Kind: "Widget", Class: ClassSpecField,
			Subject: fmt.Sprintf("Field%03d", i), NewSincePin: true, Evidence: "CreateWidget"})
	}
	full, _ := renderIssueBody("demo", "v1.41.5", "v1.44.0", findings)
	stale := staleIssueBody()
	note := "\n" + strings.Repeat("x", githubMaxIssueBody-len(full)+2000) + "\n"
	require.Greater(t, len(note)+len(full), githubMaxIssueBody, "the untruncated report must not fit")

	var patchedBody string
	// The listed copy has no note; it appears only on the final GET.
	client := newTestGitHubClient(t, serveListedIssues(t, map[string]*github.Issue{"demo": issueFor(42, stale+note)},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPatch {
				var payload struct{ Body string }
				require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
				patchedBody = payload.Body
			}
			fmt.Fprint(w, `{}`)
		})))

	outcome, err := reconcileIssue(context.Background(), client, "o", "community",
		"demo", "v1.41.5", "v1.44.0", findings, issueFor(42, stale), nil, testKnownServices("demo"), 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, issueUpdated, outcome)
	assert.LessOrEqual(t, len(patchedBody), githubMaxIssueBody)
	assert.Contains(t, patchedBody, note, "the maintainer text is kept whole")
	assert.Contains(t, patchedBody, "omitted to keep this issue body within GitHub's size limit")
	assert.Equal(t, fingerprintFindings("demo", actionable(findings)), parseFingerprint(patchedBody))
}

func TestReconcileServicesSkipsTheRefetchForAStaleIssue(t *testing.T) {
	// Operation findings alone leave an open issue stale, which writes nothing, so
	// a failed GET must not turn it into a write failure.
	client := newTestGitHubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a stale issue needs no request, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	analyze := func(_ context.Context, service string) ([]Finding, string, string, error) {
		return operationFindings(), "v1.41.5", "v1.44.0", nil
	}
	existing := map[string]*github.Issue{"svc1": issueFor(42, staleIssueBody())}

	_, writeFailures, _, err := reconcileServices(context.Background(), client, "o", "community",
		[]string{"svc1"}, existing, nil, 10, 1, analyze, false, "")
	require.NoError(t, err)
	assert.Empty(t, writeFailures)
}
