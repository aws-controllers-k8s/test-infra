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
	"testing"

	"github.com/stretchr/testify/assert"
)

// allPass is one passing check per ID, in CheckOrder.
func allPass() []Check {
	out := make([]Check, 0, len(CheckOrder))
	for _, id := range CheckOrder {
		out = append(out, newCheck(id, nil))
	}
	return out
}

func with(checks []Check, c Check) []Check {
	for i := range checks {
		if checks[i].ID == c.ID {
			checks[i] = c
		}
	}
	return checks
}

func TestDecideAllPass(t *testing.T) {
	v := decide(ClassSDKBump, true, allPass())
	assert.Equal(t, StatusPass, v.Verdict)
	assert.Empty(t, v.Reasons)
	assert.Empty(t, v.ReclassifyTo)
	assert.Contains(t, v.NotCovered, "real-aws-e2e")
	for _, c := range v.Checks {
		assert.Equal(t, c.ID != CheckIgnore, c.Binding, c.ID)
	}
}

func TestDecideBindingFailAndSkip(t *testing.T) {
	fail := newCheck(CheckPathAllowlist, []Finding{{Signature: "PATH_NOT_ALLOWED", Fails: true}})
	fail.Reclassify = ClassHandWritten
	v := decide(ClassSDKBump, false, with(with(allPass(), fail), skipCheck(CheckRegenClean, "CODEGEN_DIR_NOT_SET")))
	assert.Equal(t, StatusFail, v.Verdict)
	assert.ElementsMatch(t, []string{"path-allowlist:PATH_NOT_ALLOWED", "regen-clean:CODEGEN_DIR_NOT_SET"}, v.Reasons)
	assert.Equal(t, ClassHandWritten, v.ReclassifyTo)
}

func TestDecideIgnoresNonBindingFailures(t *testing.T) {
	// ignore-effective is not in the SDK-bump gate set; provenance binds only
	// with a work item or an autopilot head.
	ig := newCheck(CheckIgnore, []Finding{{Signature: "IGNORE_FIELD_PATH_NO_EFFECT", Fails: true}})
	pv := newCheck(CheckProvenance, []Finding{{Signature: "NO_BOT_COMMIT", Fails: true}})
	v := decide(ClassSDKBump, false, with(with(allPass(), ig), pv))
	assert.Equal(t, StatusPass, v.Verdict)

	v = decide(ClassSDKBump, true, with(allPass(), pv))
	assert.Equal(t, StatusFail, v.Verdict)
}

func TestDecideReclassifiesOnlyUpward(t *testing.T) {
	c := newCheck(CheckPathAllowlist, []Finding{{Signature: "PATH_NOT_ALLOWED", Fails: true}})
	c.Reclassify = ClassConfigOnly
	v := decide(ClassHandWritten, false, with(allPass(), c))
	assert.Empty(t, v.ReclassifyTo)
}

func TestDecideCollectsLabelsFromEveryCheck(t *testing.T) {
	c := newCheck(CheckIgnore, nil)
	c.Labels = []string{LabelSensitivePath}
	v := decide(ClassMechanical, false, with(allPass(), c))
	assert.Equal(t, []string{LabelSensitivePath}, v.Labels)
}

func TestGateSetsCoverEveryInputClass(t *testing.T) {
	for _, c := range InputClasses {
		assert.NotEmpty(t, gateSets[c], c)
		assert.Contains(t, gateSets[c], CheckPathAllowlist, c)
		assert.Contains(t, gateSets[c], CheckLeaks, c)
	}
	assert.False(t, validClass(ClassBreakingAPI))
	assert.Equal(t, ClassBreakingAPI, atLeast(ClassSDKBump, ClassBreakingAPI))
	assert.Equal(t, ClassConfigOnly, atLeast(ClassConfigOnly, ClassSDKBump))
	assert.Equal(t, ClassSDKBump, atLeast("", ClassSDKBump))
}
