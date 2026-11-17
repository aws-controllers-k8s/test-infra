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

import "slices"

// Change classes. The first six are the ledger's names plus the two design §6
// classes the ledger does not produce yet; breaking-API is only ever an output.
const (
	ClassMechanical  = "mechanical"
	ClassDocsOnly    = "docs-only"
	ClassSDKBump     = "SDK-bump"
	ClassConfigOnly  = "config-only"
	ClassHandWritten = "hand-written-code"
	ClassNewResource = "new-resource"
	ClassBreakingAPI = "breaking-API"
)

// InputClasses are the classes dod can be asked to gate.
var InputClasses = []string{ClassMechanical, ClassDocsOnly, ClassSDKBump, ClassConfigOnly, ClassHandWritten, ClassNewResource}

// classRank orders classes for "at least" (design §6 downgrade rules).
var classRank = map[string]int{
	ClassMechanical: 0, ClassDocsOnly: 0, ClassSDKBump: 1, ClassConfigOnly: 2,
	ClassHandWritten: 3, ClassNewResource: 4, ClassBreakingAPI: 5,
}

func validClass(c string) bool { return slices.Contains(InputClasses, c) }

// atLeast returns the higher-ranked class; "" ranks below everything.
func atLeast(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	case classRank[b] > classRank[a]:
		return b
	}
	return a
}

// Check IDs, in the order Run runs them and the verdict lists them.
const (
	CheckPathAllowlist = "path-allowlist"
	CheckProvenance    = "provenance"
	CheckRegenClean    = "regen-clean"
	CheckBuild         = "build"
	CheckCRDDiff       = "crd-diff"
	CheckSensitive     = "sensitive-field"
	CheckCodeScan      = "code-scan"
	CheckLeaks         = "leak-inventory"
	CheckSDKPin        = "sdk-pin"
	CheckIgnore        = "ignore-effective"
)

var CheckOrder = []string{
	CheckPathAllowlist, CheckProvenance, CheckRegenClean, CheckBuild, CheckCRDDiff,
	CheckSensitive, CheckCodeScan, CheckLeaks, CheckSDKPin, CheckIgnore,
}

// LabelSensitivePath is design §6's label for a change to a sensitive path.
const LabelSensitivePath = "autopilot/sensitive-path"

var sdkBumpGates = []string{
	CheckPathAllowlist, CheckProvenance, CheckRegenClean, CheckBuild, CheckCRDDiff,
	CheckSensitive, CheckCodeScan, CheckLeaks, CheckSDKPin,
}

// gateSets are the binding checks per class (design §6 "Binding gates").
var gateSets = map[string][]string{
	ClassMechanical:  {CheckPathAllowlist, CheckProvenance, CheckBuild, CheckCodeScan, CheckLeaks, CheckSDKPin},
	ClassDocsOnly:    sdkBumpGates,
	ClassSDKBump:     sdkBumpGates,
	ClassConfigOnly:  append(slices.Clone(sdkBumpGates), CheckIgnore),
	ClassHandWritten: append(slices.Clone(sdkBumpGates), CheckIgnore),
	ClassNewResource: append(slices.Clone(sdkBumpGates), CheckIgnore),
}

// notCovered names the design §6 gates for a class that dod does not run, so a
// pass is never read as "all gates green".
var notCovered = map[string][]string{
	ClassMechanical:  {"real-aws-e2e", "rescan"},
	ClassDocsOnly:    {},
	ClassSDKBump:     {"real-aws-e2e", "behavior-probe", "upgrade-probe", "detector-rerun", "aws-resource-leak-check"},
	ClassConfigOnly:  {"real-aws-e2e", "behavior-probe", "new-e2e-test", "emulator-e2e", "aws-resource-leak-check"},
	ClassHandWritten: {"real-aws-e2e", "behavior-probe", "new-e2e-test", "emulator-e2e", "aws-resource-leak-check", "red-green-proof"},
	ClassNewResource: {"real-aws-e2e", "behavior-probe", "plan-schema-hash", "crud-tags-adoption-e2e", "iam-recommended-policy", "aws-resource-leak-check"},
}

func binding(class, workItem, id string) bool {
	if id == CheckProvenance && workItem == "" {
		return false
	}
	return slices.Contains(gateSets[class], id)
}

// decide applies the class's gate set: the verdict passes only when every
// binding check passes. Reclassification comes from binding checks and only
// ever moves up.
func decide(class, workItem string, checks []Check) Verdict {
	v := Verdict{Schema: Schema, Verdict: StatusPass, NotCovered: slices.Clone(notCovered[class])}
	reclass := ""
	for i := range checks {
		c := &checks[i]
		c.Binding = binding(class, workItem, c.ID)
		v.Labels = append(v.Labels, c.Labels...)
		if !c.Binding {
			continue
		}
		if c.Status != StatusPass {
			v.Verdict = StatusFail
			reasons := c.Reasons
			if len(reasons) == 0 {
				reasons = []string{"NO_REASON"}
			}
			for _, r := range reasons {
				v.Reasons = append(v.Reasons, c.ID+":"+r)
			}
		}
		reclass = atLeast(reclass, c.Reclassify)
	}
	if reclass != "" && classRank[reclass] > classRank[class] {
		v.ReclassifyTo = reclass
	}
	v.Checks = checks
	return v
}
