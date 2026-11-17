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

package sdkbump

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/issuefinder"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

var bumpKey = issuefinder.BumpKey(svc, to)

func bumpItem(state ledger.State) *ledger.WorkItem {
	return &ledger.WorkItem{
		Schema: ledger.Schema, Key: bumpKey, KeyHash: ledger.KeyHash(bumpKey), Generation: 1,
		Source: "api-change", Service: svc, Repo: "aws-controllers-k8s/s3-controller",
		GapClass: "regen", ChangeClass: "SDK-bump", AutonomyLevel: "human-merge", PriorityBand: 3,
		Evidence: ledger.Evidence{SDKFrom: "v1.41.5", SDKTo: to}, State: state,
		History: []ledger.Transition{{TS: "2026-11-16T00:00:00Z", To: state, By: "issue-finder"}},
	}
}

func dependent(kind, field, work string, state ledger.State) *ledger.WorkItem {
	key := "api-change|s3|" + kind + "|spec:" + field + "|needs-config"
	return &ledger.WorkItem{
		Schema: ledger.Schema, Key: key, KeyHash: ledger.KeyHash(key), Generation: 1,
		Source: "api-change", Service: svc, Repo: "aws-controllers-k8s/s3-controller",
		Resource: kind, FieldPath: field, GapClass: "needs-config", ChangeClass: "config-only",
		AutonomyLevel: "human-merge", PriorityBand: 4, DetectorWork: work,
		DependsOn: []string{bumpKey}, State: state,
		History: []ledger.Transition{{TS: "2026-11-16T00:00:00Z", To: state, By: "issue-finder"}},
	}
}

func itemsOf(ws ...*ledger.WorkItem) map[string]*ledger.WorkItem {
	m := map[string]*ledger.WorkItem{}
	for _, w := range ws {
		m[w.Key] = w
	}
	return m
}

func TestFindDependents(t *testing.T) {
	gen := []byte("resources:\n  bucket:\n    fields:\n      OwnershipControls.Rules:\n        late_initialize: {}\n" +
		"      Versioning.MFADelete:\n        compare:\n          is_ignored: true\n      Logging.Prefix:\n        is_immutable: true\n")
	items := itemsOf(bumpItem(ledger.HumanReview),
		dependent("Bucket", "ownershipControls.rules", "server-default", ledger.Blocked),
		dependent("Bucket", "versioning.mfaDelete", "server-default", ledger.Blocked),
		dependent("Bucket", "logging.prefix", "server-default", ledger.Blocked),
		dependent("Bucket", "notification.topic", "server-default", ledger.WontFix),
		dependent("Bucket", "website.index", "server-default", ledger.Escalated),
		dependent("Bucket", "tags.extra", "create-only", ledger.Blocked), // not server-default
	)
	d := FindDependents(items, bumpKey, gen)
	require.True(t, d.Known)
	got := map[string]string{}
	for _, x := range d.Items {
		got[x.FieldPath] = x.How
		assert.Equal(t, x.How != "", x.Resolved, x.FieldPath)
	}
	assert.Equal(t, map[string]string{
		"logging.prefix":          "",
		"notification.topic":      "closed as won't fix in the ledger",
		"ownershipControls.rules": "generator.yaml sets late_initialize",
		"versioning.mfaDelete":    "generator.yaml sets compare.is_ignored",
		"website.index":           "",
	}, got)
	assert.Equal(t, 2, d.Unresolved())
	assert.True(t, d.Blocked())
	assert.Equal(t, "logging.prefix", d.Items[0].FieldPath, "sorted by resource, then field")
}

func TestDependentsBlockWhenUnknown(t *testing.T) {
	assert.True(t, Dependents{}.Blocked())
	assert.False(t, Dependents{Known: true}.Blocked())
	d := FindDependents(itemsOf(bumpItem(ledger.Queued)), bumpKey, []byte("not: [yaml"))
	assert.True(t, d.Known)
	assert.Empty(t, d.Items)
}

func TestFromLedger(t *testing.T) {
	gotTo, gotWI, err := FromLedger(itemsOf(bumpItem(ledger.Queued)), svc)
	require.NoError(t, err)
	assert.Equal(t, [2]string{to, wi}, [2]string{gotTo, gotWI})

	_, _, err = FromLedger(itemsOf(bumpItem(ledger.Superseded)), svc)
	assert.True(t, errors.Is(err, ErrNoBumpItem))
	_, _, err = FromLedger(itemsOf(bumpItem(ledger.Queued)), "ec2")
	assert.True(t, errors.Is(err, ErrNoBumpItem))

	other := bumpItem(ledger.Queued)
	other.Key = issuefinder.BumpKey(svc, "service/s3/v1.98.0")
	other.KeyHash = ledger.KeyHash(other.Key)
	_, _, err = FromLedger(itemsOf(bumpItem(ledger.Queued), other), svc)
	assert.Error(t, err)
	assert.False(t, errors.Is(err, ErrNoBumpItem))
}
