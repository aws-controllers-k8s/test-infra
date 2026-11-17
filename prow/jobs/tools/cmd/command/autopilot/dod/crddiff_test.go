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

package dod

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const mb = "1111111111111111111111111111111111111111"

// doc builds a document whose buckets hold the given changes; Breaking is the
// OR of the changes, as the producer sets it.
func doc(changes ...crdChange) *crdCompat {
	d := &crdCompat{Schema: crdCompatSchema, BaseRef: mb, Buckets: map[string][]crdChange{}}
	for _, b := range crdBuckets {
		d.Buckets[b] = []crdChange{}
	}
	for _, c := range changes {
		d.Buckets[c.Bucket] = append(d.Buckets[c.Bucket], c)
		d.Breaking = d.Breaking || c.Breaking
	}
	return d
}

func ch(bucket, changeType string, breaking bool) crdChange {
	return crdChange{File: "config/crd/bases/x.yaml", Kind: "Widget", Bucket: bucket, Field: "spec.a", ChangeType: changeType, Breaking: breaking}
}

var (
	addedOpt    = ch("added_optional_spec", "PROPERTY_ADDED", false)
	descOnly    = ch("description_only", "DESCRIPTION_CHANGED", false)
	celRule     = ch("other", changeUncompared, false)
	defaultCh   = ch("default_changed", changeUncompared, false)
	enumShrunk  = ch("enum_changed", "ENUM_VALUE_REMOVED", true)
	removed     = ch("removed", "PROPERTY_REMOVED", true)
	crdAdded    = ch("crd_added", "CRD_ADDED", false)
	tightenedCE = ch("validation_tightened", changeUncompared, false)
)

func TestCRDDiffAdditive(t *testing.T) {
	assert.Equal(t, StatusPass, checkCRDDiff(ClassSDKBump, doc(addedOpt, descOnly), mb, mb).Status)
	for name, c := range map[string]crdChange{"other": celRule, "uncompared default": defaultCh, "breaking enum": enumShrunk, "crd added": crdAdded, "removed": removed} {
		got := checkCRDDiff(ClassSDKBump, doc(addedOpt, c), mb, mb)
		assert.Equal(t, []string{"CRD_NOT_ADDITIVE"}, got.Reasons, name)
		assert.Equal(t, ClassBreakingAPI, got.Reclassify, name)
	}
	unknown := doc(addedOpt)
	unknown.Buckets["from_the_future"] = []crdChange{ch("from_the_future", "NEW", false)}
	assert.Equal(t, StatusFail, checkCRDDiff(ClassSDKBump, unknown, mb, mb).Status)
}

func TestCRDDiffDocsOnly(t *testing.T) {
	assert.Equal(t, StatusPass, checkCRDDiff(ClassDocsOnly, doc(descOnly), mb, mb).Status)
	got := checkCRDDiff(ClassDocsOnly, doc(descOnly, addedOpt), mb, mb)
	assert.Equal(t, []string{"CRD_NOT_DESCRIPTION_ONLY"}, got.Reasons)
	assert.Equal(t, ClassSDKBump, got.Reclassify)
	assert.Equal(t, ClassBreakingAPI, checkCRDDiff(ClassDocsOnly, doc(removed), mb, mb).Reclassify)
}

func TestCRDDiffReview(t *testing.T) {
	got := checkCRDDiff(ClassConfigOnly, doc(addedOpt, celRule, tightenedCE), mb, mb)
	assert.Equal(t, StatusPass, got.Status)
	assert.Len(t, got.Findings, 2)
	assert.Equal(t, "CRD_NEEDS_JUDGEMENT", got.Findings[0].Signature)

	got = checkCRDDiff(ClassHandWritten, doc(removed), mb, mb)
	assert.Equal(t, []string{"CRD_BREAKING"}, got.Reasons)
	assert.Equal(t, ClassBreakingAPI, got.Reclassify)

	got = checkCRDDiff(ClassConfigOnly, doc(crdAdded), mb, mb)
	assert.Equal(t, []string{"CRD_ADDED"}, got.Reasons)
	assert.Equal(t, ClassNewResource, got.Reclassify)
	assert.Equal(t, StatusPass, checkCRDDiff(ClassNewResource, doc(crdAdded), mb, mb).Status)
}

func TestCRDDiffDocumentMustMatchTheRun(t *testing.T) {
	got := checkCRDDiff(ClassSDKBump, doc(addedOpt), "", mb)
	assert.Equal(t, []string{"CRD_COMPAT_BASE_MISMATCH"}, got.Reasons)

	lying := doc(removed)
	lying.Breaking = false
	assert.Contains(t, checkCRDDiff(ClassSDKBump, lying, mb, mb).Reasons, "CRD_COMPAT_INCONSISTENT")

	misfiled := doc()
	misfiled.Buckets["description_only"] = []crdChange{removed}
	misfiled.Breaking = true
	assert.Contains(t, checkCRDDiff(ClassSDKBump, misfiled, mb, mb).Reasons, "CRD_COMPAT_INCONSISTENT")
}

func TestParseCRDCompat(t *testing.T) {
	data, err := os.ReadFile("testdata/crd-compat-s3-v1.0.0.json")
	require.NoError(t, err)
	d, err := parseCRDCompat(data)
	require.NoError(t, err)
	assert.Equal(t, "v1.0.0", d.BaseRef)
	assert.Len(t, d.Buckets["added_optional_spec"], 16)
	got := checkCRDDiff(ClassSDKBump, d, mb, mb)
	assert.Equal(t, []string{"CRD_NOT_ADDITIVE"}, got.Reasons)
	assert.Len(t, got.Findings, 2, "crd_added and crd_removed")

	for name, bad := range map[string]string{
		"schema":   `{"schema":"ack.crd-compat/v0","base_ref":"x","breaking":false,"files":[],"buckets":{},"sensitive_candidates":[]}`,
		"unknown":  `{"schema":"ack.crd-compat/v1","base_ref":"x","breaking":false,"files":[],"buckets":{},"sensitive_candidates":[],"x":1}`,
		"trailing": `{"schema":"ack.crd-compat/v1","base_ref":"x","breaking":false,"files":[],"buckets":{},"sensitive_candidates":[]} {}`,
	} {
		_, err := parseCRDCompat([]byte(bad))
		assert.Error(t, err, name)
	}
}

// A document without Plan 4's bucket keys (truncated or from another tool)
// would otherwise pass every CRD rule with nothing to judge.
func TestParseCRDCompatNeedsEveryBucket(t *testing.T) {
	_, err := parseCRDCompat([]byte(`{"schema":"ack.crd-compat/v1","base_ref":"x","breaking":false,"files":[],"buckets":{},"sensitive_candidates":[]}`))
	assert.ErrorContains(t, err, "added_optional_spec")

	d := doc()
	delete(d.Buckets, "removed")
	b, err := json.Marshal(d)
	require.NoError(t, err)
	_, err = parseCRDCompat(b)
	assert.ErrorContains(t, err, `"removed"`)
}
