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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

const crdDocJSON = `{"schema":"ack.crd-compat/v1","base_ref":"b","breaking":false,
"files":[],"sensitive_candidates":[{"file":"f","kind":"Bucket","field":"spec.secretToken"}],
"buckets":{"added_optional_spec":[{"file":"f","kind":"Bucket","bucket":"added_optional_spec","field":"spec.logging.prefix","path":"p","change_type":"PROPERTY_ADDED","old":"","new":"","breaking":false},
{"file":"f","kind":"Bucket","bucket":"added_optional_spec","field":"spec.tags.extra","path":"p","change_type":"PROPERTY_ADDED","old":"","new":"","breaking":false}],
"added_status":[{"file":"f","kind":"Bucket","bucket":"added_status","field":"status.arn2","path":"p","change_type":"PROPERTY_ADDED","old":"","new":"","breaking":false}],
"description_only":[{"file":"f","kind":"Bucket","bucket":"description_only","field":"spec.name","path":"p","change_type":"DESCRIPTION_CHANGED","old":"","new":"","breaking":false}],
"removed":[]}}`

func cardInput(t *testing.T) CardInput {
	t.Helper()
	doc, err := ParseCRDDoc([]byte(crdDocJSON))
	require.NoError(t, err)
	before := Pins{CoreSDK: "v1.41.5", GoModService: "v1.90.0", GoModCore: "v1.41.5", GoModSmithy: "v1.24.2",
		CodegenVersion: "v0.64.0", CodegenHash: sha, RuntimeVersion: "v0.68.0"}
	after := before
	after.ServiceSDK, after.GoModService, after.GoModCore = "v1.97.3", "v1.97.3", "v1.41.7"
	return CardInput{
		Service: svc, Target: Target{Package: "s3", Version: "v1.97.3"}, Before: before, After: after, CRD: doc,
		Deps: Dependents{Known: true, Items: []Dependent{
			{KeyHash: "sha256:1111111111111111", Resource: "Bucket", FieldPath: "logging.prefix", State: ledger.Blocked},
			{KeyHash: "sha256:2222222222222222", Resource: "Bucket", FieldPath: "website.index", State: ledger.WontFix,
				Resolved: true, How: "closed as won't fix in the ledger"},
		}},
		JobName: "autopilot-sdk-bump-s3 #7",
	}
}

func TestRenderCard(t *testing.T) {
	in := cardInput(t)
	card := RenderCard(in)
	assert.Equal(t, card, RenderCard(in), "deterministic")
	for _, want := range []string{
		bodyMarker,
		"**Class:** SDK-bump",
		"- [ ] `Bucket` `logging.prefix` (`sha256:1111111111111111`, ledger state `BLOCKED`)",
		"/autopilot wontfix sha256:1111111111111111",
		"- [x] `Bucket` `website.index` (`sha256:2222222222222222`): closed as won't fix in the ledger",
		"| `added_optional_spec` | 2 |",
		"| `Bucket` | `spec.logging.prefix` | `added_optional_spec` | yes |",
		"| `Bucket` | `spec.tags.extra` | `added_optional_spec` | no |",
		"`Bucket` `spec.secretToken`",
		"Decide `late_initialize`, `compare.is_ignored` or \"no server default\" for 1 new Spec field(s)",
		"`github.com/aws/aws-sdk-go-v2` moved `v1.41.5` → `v1.41.7`",
		"| `aws_service_sdk_version` | (none) | `v1.97.3` |",
		"LLM tokens: 0",
	} {
		assert.Contains(t, card, want)
	}
	assert.NotContains(t, card, "ack-private")
	judge := card[strings.Index(card, "### What you must judge"):strings.Index(card, "### Server-default")]
	assert.Equal(t, 3, strings.Count(judge, "\n- "), "at most three judgement bullets")
}

func TestRenderCardUnknownDependents(t *testing.T) {
	in := cardInput(t)
	in.Deps = Dependents{}
	card := RenderCard(in)
	assert.Contains(t, card, "Unknown: this run could not read the ledger")
	assert.Contains(t, card, "could not be read from the ledger")
}

func TestReplaceSection(t *testing.T) {
	card := RenderCard(cardInput(t))
	got, ok := ReplaceSection(card, sectionServerDefault, RenderDependents(Dependents{Known: true}))
	require.True(t, ok)
	assert.Contains(t, got, "None. No field in this bump is a server-default dependent.")
	assert.NotContains(t, got, "logging.prefix` (`sha256")
	_, ok = ReplaceSection(card+sectionBegin(sectionServerDefault), sectionServerDefault, "x\n")
	assert.False(t, ok, "a second marker is refused")
	_, ok = ReplaceSection("no markers", sectionServerDefault, "x\n")
	assert.False(t, ok)
}

func TestValuesCannotForgeMarkup(t *testing.T) {
	in := cardInput(t)
	in.Deps.Items[0].FieldPath = "a|b`c-->" + sectionEnd(sectionServerDefault)
	card := RenderCard(in)
	assert.Equal(t, 1, strings.Count(card, sectionEnd(sectionServerDefault)))
	assert.Contains(t, card, "a\\|b'c--&gt;&lt;!-- autopilot:server-default:end --&gt;")
}

func TestGateSectionAndTrailers(t *testing.T) {
	v := &dod.Verdict{
		Verdict: dod.StatusFail, Reasons: []string{"path-allowlist:PATH_NOT_ALLOWED"}, ReclassifyTo: "hand-written-code",
		NotCovered: []string{"real-aws-e2e", "upgrade-probe"},
		Checks: []dod.Check{
			{ID: "path-allowlist", Binding: true, Status: dod.StatusFail, Reasons: []string{"PATH_NOT_ALLOWED"},
				Findings: []dod.Finding{{Signature: "PATH_NOT_ALLOWED", File: "pkg/resource/bucket/hook.go", Fails: true}}},
			{ID: "crd-diff", Binding: true, Status: dod.StatusPass, Reasons: []string{},
				Findings: []dod.Finding{{Signature: "CRD_NEEDS_JUDGEMENT", File: "config/crd/bases/b.yaml", Detail: "spec.x other"}, {Signature: "INVENTORY_ONLY"}}},
		},
	}
	digest := "sha256:" + strings.Repeat("ab", 32)
	gate := RenderGateSection(v, digest)
	for _, want := range []string{
		sectionBegin(sectionGate), sectionEnd(sectionGate),
		"**Verdict:** `fail`. **Reclassify to:** `hand-written-code`.",
		"| `path-allowlist` | yes | `fail` | `PATH_NOT_ALLOWED` |",
		"- `PATH_NOT_ALLOWED` (`path-allowlist`) in `pkg/resource/bucket/hook.go`",
		"- `CRD_NEEDS_JUDGEMENT` (`crd-diff`) in `config/crd/bases/b.yaml`: spec.x other",
		"**Not covered by these gates:** `real-aws-e2e`, `upgrade-probe`",
	} {
		assert.Contains(t, gate, want)
	}
	assert.NotContains(t, gate, "INVENTORY_ONLY", "non-failing inventory stays in verdict.json")

	body := ComposeBody(RenderCard(cardInput(t)), gate, Trailers(wi, digest))
	got, ok := dod.TrailerValue([]byte(body))
	require.True(t, ok)
	assert.Equal(t, digest, got)
	got, ok = trailerValue(body, "Work-Item")
	require.True(t, ok)
	assert.Equal(t, wi, got)
	assert.True(t, strings.HasSuffix(body, "Change-Class: SDK-bump\nGate-Verdict: "+digest+"\n"))
	assert.Less(t, len(body), maxBodyBytes)
}

func TestCommitMessage(t *testing.T) {
	msg := commitMessage(wi, Target{Package: "s3", Version: "v1.97.3"}, Pins{CodegenVersion: "v0.64.0", CodegenHash: sha, RuntimeVersion: "v0.68.0"})
	assert.True(t, strings.HasPrefix(msg, "Update aws-sdk-go-v2/service/s3 to v1.97.3\n\n"))
	for key, want := range map[string]string{"Producer": "autopilot/sdk-bump", "Work-Item": wi, "Change-Class": "SDK-bump"} {
		got, ok := trailerValue(msg, key)
		assert.True(t, ok, key)
		assert.Equal(t, want, got, key)
	}
	noWI := commitMessage("", Target{Package: "s3", Version: "v1.97.3"}, Pins{})
	_, ok := trailerValue(noWI, "Work-Item")
	assert.False(t, ok)
}

// dod refuses a crd-compat document given as a file unless its head_ref
// resolves to the run's head tree; Plan 4 does not write head_ref, so the run
// sets it (Task 10) and the card reads it back from the same bytes.
func TestParseCRDDocReadsHeadRef(t *testing.T) {
	doc, err := ParseCRDDoc([]byte(`{"schema":"ack.crd-compat/v1","base_ref":"b","head_ref":"` + sha + `","buckets":{}}`))
	require.NoError(t, err)
	assert.Equal(t, sha, doc.HeadRef)
	_, err = ParseCRDDoc([]byte(`{"schema":"ack.crd-compat/v2"}`))
	assert.Error(t, err)
}
