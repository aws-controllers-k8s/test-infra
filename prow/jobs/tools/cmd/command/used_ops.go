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

package command

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// sdkOpCallRE matches the two shapes a generated or hand-written ACK resource
// manager uses to name an SDK operation: an input struct literal
// (`svcsdk.CreateWidgetInput{`) and a client call (`rm.sdkapi.CreateWidget(`).
// Taking the union catches operations invoked from custom hooks, which is
// where the majority of calls live for resources like s3's Bucket.
// Known limitation: neither alternative is anchored to a token boundary, since
// RE2 has no lookbehind. So `mocksdkapi.CreateWidget(` would match as a
// substring, as would a call-shaped string literal or comment. Surveyed across
// ec2, sagemaker, rds, acm, iam, fsx, route53, s3files, and prometheusservice:
// zero occurrences. If that changes, reject a match whose preceding byte is an
// identifier character rather than trying to express it in the pattern.
var sdkOpCallRE = regexp.MustCompile(`svcsdk\.([A-Z][A-Za-z0-9]*)Input\{|sdkapi\.([A-Z][A-Za-z0-9]*)\(`)

// scanUsedOps returns the set of AWS SDK operations each resource package
// invokes, keyed by normalizeResourceKey of the package directory name.
//
// Keys are normalized rather than raw because ACK generates those directories in
// snake_case (pkg/resource/dhcp_options) while CRD kinds are PascalCase with
// uppercased acronyms (DHCPOptions). Lookups come from the kind side, so both
// sides must pass through the same normalization — see normalizeResourceKey.
//
// This is a heuristic. A missed operation causes it to be reported as unmapped
// (noise); a spurious match causes shapes to be walked that ACK does not use.
// It is isolated here so it is the only thing to revisit if generated code
// changes shape.
func scanUsedOps(controllerPath string) (map[string]map[string]bool, error) {
	root := filepath.Join(controllerPath, "pkg", "resource")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]map[string]bool{}, nil
		}
		return nil, fmt.Errorf("unable to read %s: %s", root, err)
	}

	out := map[string]map[string]bool{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		resourceDir := filepath.Join(root, entry.Name())
		files, err := os.ReadDir(resourceDir)
		if err != nil {
			return nil, fmt.Errorf("unable to read %s: %s", resourceDir, err)
		}

		ops := map[string]bool{}
		for _, file := range files {
			// Skip tests. A controller's own tests mock SDK calls, so an
			// operation named only in a _test.go would register as "used" —
			// and a spuriously used operation is the damaging direction: we
			// would walk shapes ACK never touches and manufacture false
			// "added field" findings. Production SDK calls never live in
			// _test.go, so this costs nothing.
			if strings.HasSuffix(file.Name(), "_test.go") {
				continue
			}
			if file.IsDir() || filepath.Ext(file.Name()) != ".go" {
				continue
			}
			path := filepath.Join(resourceDir, file.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("unable to read %s: %s", path, err)
			}
			for _, match := range sdkOpCallRE.FindAllStringSubmatch(string(data), -1) {
				// Exactly one of the two capture groups is populated.
				for _, name := range match[1:] {
					if name != "" {
						ops[name] = true
					}
				}
			}
		}
		if len(ops) > 0 {
			out[normalizeResourceKey(entry.Name())] = ops
		}
	}
	return out, nil
}

// normalizeResourceKey reduces a resource kind or a resource package directory
// name to a common key, by stripping underscores and lowercasing.
//
// This exists because the two spellings do not line up any other way. ACK
// generates resource packages in snake_case while CRD kinds are PascalCase with
// uppercased acronyms, so ec2 ships kind `DHCPOptions` in
// `pkg/resource/dhcp_options`, and `TransitGatewayVPCAttachment` in
// `pkg/resource/transit_gateway_vpc_attachment`. Naively lowercasing the kind
// yields `dhcpoptions`, which matches no directory — that would silently leave
// 17 of ec2's 20 resources with an empty operation set, so no field findings
// at all for them.
//
// Stripping underscores from both sides sidesteps acronym-aware case
// conversion entirely. Verified against every controller on disk: this maps
// 277 of 279 CRD kinds onto a real resource package. The two that do not match
// are prometheusservice's `AnomalyDetector` and `QueryLoggingConfiguration`,
// which ship CRDs with no resource package at all — those legitimately have no
// operations to walk.
func normalizeResourceKey(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "_", ""))
}

// allUsedOps flattens a per-resource operation map into one set.
func allUsedOps(byResource map[string]map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, ops := range byResource {
		for op := range ops {
			out[op] = true
		}
	}
	return out
}
