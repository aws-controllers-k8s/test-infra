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
	"fmt"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
	"gopkg.in/yaml.v3"
)

const metadataPath = "apis/v1alpha1/ack-generate-metadata.yaml"

// sdkPins are the versions that must never go down: the metadata pins
// build-controller.sh reads, and the SDK modules in go.mod.
type sdkPins struct {
	Core, Service string
	Modules       map[string]string
}

func pinnedModule(mod string) bool {
	return mod == "github.com/aws/aws-sdk-go-v2" || mod == "github.com/aws/smithy-go" ||
		strings.HasPrefix(mod, "github.com/aws/aws-sdk-go-v2/service/")
}

// readPins reads either side; nil content means the file is absent.
func readPins(meta, gomod []byte) (sdkPins, error) {
	p := sdkPins{Modules: map[string]string{}}
	if meta != nil {
		var m struct {
			Core    string `yaml:"aws_sdk_go_version"`
			Service string `yaml:"aws_service_sdk_version"`
		}
		if err := yaml.Unmarshal(meta, &m); err != nil {
			return p, fmt.Errorf("%s: %w", metadataPath, err)
		}
		p.Core, p.Service = strings.TrimSpace(m.Core), strings.TrimSpace(m.Service)
	}
	if gomod != nil {
		f, err := modfile.ParseLax("go.mod", gomod, nil)
		if err != nil {
			return p, fmt.Errorf("go.mod: %w", err)
		}
		for _, r := range f.Require {
			if pinnedModule(r.Mod.Path) {
				p.Modules[r.Mod.Path] = r.Mod.Version
			}
		}
	}
	return p, nil
}

func regime(p sdkPins) string {
	switch {
	case p.Service != "":
		return "service"
	case p.Core != "":
		return "core"
	}
	return ""
}

func comparePin(file, name, from, to string) []Finding {
	detail := name + " " + from + " -> " + to
	switch {
	case from == "" && to == "":
		return nil
	case to == "":
		return []Finding{{Signature: "SDK_PIN_REMOVED", File: file, Detail: name + " " + from, Fails: true}}
	case from == "":
		return []Finding{{Signature: "SDK_PIN_ADDED", File: file, Detail: name + " " + to}}
	case !semver.IsValid(from) || !semver.IsValid(to):
		return []Finding{{Signature: "SDK_PIN_UNPARSEABLE", File: file, Detail: detail, Fails: true}}
	}
	switch semver.Compare(from, to) {
	case 1:
		return []Finding{{Signature: "SDK_PIN_DECREASED", File: file, Detail: detail, Fails: true}}
	case -1:
		return []Finding{{Signature: "SDK_PIN_RAISED", File: file, Detail: detail}}
	}
	return nil
}

// checkSDKPin fails when any pin goes down, disappears, stops being semver, or
// moves between the core and the per-service regime (Appendix A [medium]).
func checkSDKPin(base, head sdkPins) Check {
	var findings []Finding
	if rb, rh := regime(base), regime(head); rb != "" && rh != "" && rb != rh {
		findings = append(findings, Finding{Signature: "SDK_PIN_REGIME_CHANGED", File: metadataPath, Detail: rb + " -> " + rh, Fails: true})
	}
	findings = append(findings, comparePin(metadataPath, "aws_sdk_go_version", base.Core, head.Core)...)
	findings = append(findings, comparePin(metadataPath, "aws_service_sdk_version", base.Service, head.Service)...)
	for _, mod := range sortedKeys(base.Modules) {
		findings = append(findings, comparePin("go.mod", mod, base.Modules[mod], head.Modules[mod])...)
	}
	for _, mod := range sortedKeys(head.Modules) {
		if _, ok := base.Modules[mod]; !ok {
			findings = append(findings, comparePin("go.mod", mod, "", head.Modules[mod])...)
		}
	}
	return newCheck(CheckSDKPin, findings)
}
