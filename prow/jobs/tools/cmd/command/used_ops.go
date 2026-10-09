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

// sdkOpCallRE matches an SDK input literal (`svcsdk.CreateWidgetInput{`) or
// client call (`rm.sdkapi.CreateWidget(`), so calls from custom hooks count too.
// It is not anchored to a token boundary (RE2 has no lookbehind), so
// `mocksdkapi.X(` would also match; no controller has such code today.
var sdkOpCallRE = regexp.MustCompile(`svcsdk\.([A-Z][A-Za-z0-9]*)Input\{|sdkapi\.([A-Z][A-Za-z0-9]*)\(`)

// scanUsedOps returns the set of AWS SDK operations each resource package
// invokes, keyed by normalizeResourceKey of the package directory name. It is a
// regex heuristic, so revisit it if generated code changes shape.
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
			// Skip tests: mocked SDK calls would mark unused operations as used
			// and produce false field findings.
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
// name to a common key by stripping underscores and lowercasing, so kind
// `DHCPOptions` matches `pkg/resource/dhcp_options` without acronym-aware case
// conversion.
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
