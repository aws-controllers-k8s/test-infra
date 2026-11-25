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
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
	"gopkg.in/yaml.v3"
)

const (
	sdkCoreModule    = "github.com/aws/aws-sdk-go-v2"
	sdkV1Module      = "github.com/aws/aws-sdk-go"
	smithyModule     = "github.com/aws/smithy-go"
	runtimeModule    = "github.com/aws-controllers-k8s/runtime"
	sdkServicePrefix = "github.com/aws/aws-sdk-go-v2/service/"
)

// Pins is what a controller commit says it was generated with and builds against.
type Pins struct {
	APIVersion, MetadataPath    string
	CoreSDK, ServiceSDK         string // aws_sdk_go_version, aws_service_sdk_version
	CodegenVersion, CodegenHash string // ack_generate_info.version, .build_hash
	RuntimeVersion              string // go.mod runtime
	GoModService                string // go.mod aws-sdk-go-v2/service/<pkg>
	GoModCore, GoModSmithy      string
	LegacyV1                    bool // go.mod requires aws-sdk-go v1
}

var apiVersionRE = regexp.MustCompile(`^v[0-9]+(?:(?:alpha|beta)[0-9]+)?$`)

// readPins reads the pins from rev (never the working tree).
func readPins(ctx context.Context, g gitOps, rev, pkg string) (Pins, error) {
	mdb, err := g.show(ctx, rev, "metadata.yaml")
	if err != nil {
		return Pins{}, err
	}
	var md struct {
		APIVersions []struct {
			APIVersion string `yaml:"api_version"`
			Status     string `yaml:"status"`
		} `yaml:"api_versions"`
	}
	if err := yaml.Unmarshal(mdb, &md); err != nil {
		return Pins{}, fmt.Errorf("metadata.yaml: %w", err)
	}
	var avail []string
	for _, v := range md.APIVersions {
		if v.Status == "available" {
			avail = append(avail, v.APIVersion)
		}
	}
	if len(avail) != 1 || !apiVersionRE.MatchString(avail[0]) {
		return Pins{}, fmt.Errorf("metadata.yaml: want one available api_version, have %q", avail)
	}
	p := Pins{APIVersion: avail[0], MetadataPath: "apis/" + avail[0] + "/ack-generate-metadata.yaml"}

	gmb, err := g.show(ctx, rev, p.MetadataPath)
	if err != nil {
		return Pins{}, err
	}
	var gm struct {
		Info struct {
			Version   string `yaml:"version"`
			BuildHash string `yaml:"build_hash"`
		} `yaml:"ack_generate_info"`
		Core    string `yaml:"aws_sdk_go_version"`
		Service string `yaml:"aws_service_sdk_version"`
	}
	if err := yaml.Unmarshal(gmb, &gm); err != nil {
		return Pins{}, fmt.Errorf("%s: %w", p.MetadataPath, err)
	}
	p.CodegenVersion, p.CodegenHash, p.CoreSDK, p.ServiceSDK = gm.Info.Version, gm.Info.BuildHash, gm.Core, gm.Service

	mb, err := g.show(ctx, rev, "go.mod")
	if err != nil {
		return Pins{}, err
	}
	mf, err := modfile.ParseLax("go.mod", mb, nil)
	if err != nil {
		return Pins{}, err
	}
	for _, r := range mf.Require {
		switch r.Mod.Path {
		case runtimeModule:
			p.RuntimeVersion = r.Mod.Version
		case sdkCoreModule:
			p.GoModCore = r.Mod.Version
		case smithyModule:
			p.GoModSmithy = r.Mod.Version
		case sdkV1Module:
			p.LegacyV1 = true
		case sdkServicePrefix + pkg:
			p.GoModService = r.Mod.Version
		}
	}
	return p, nil
}

// decide returns the outcome that stops the run before any build, or "" to
// bump. It never lets a pin go down (Plan 5's sdk-pin gate checks again).
func decide(p Pins, t Target) (outcome, reason string) {
	switch {
	case p.LegacyV1 || (p.CoreSDK == "" && p.ServiceSDK == ""):
		return OutcomeNeedsHuman, "UNSUPPORTED_PIN_REGIME"
	case p.ServiceSDK != "" && !semver.IsValid(p.ServiceSDK):
		return OutcomeNeedsHuman, "SDK_PIN_UNREADABLE"
	case p.ServiceSDK != "" && semver.Compare(p.ServiceSDK, t.Version) >= 0:
		return OutcomeNoChange, "ALREADY_AT_TARGET"
	case p.GoModService != "" && semver.Compare(p.GoModService, t.Version) > 0:
		return OutcomeNeedsHuman, "SDK_PIN_WOULD_DECREASE"
	case strings.Contains(p.CodegenVersion, "dirty") || !shaRE.MatchString(p.CodegenHash):
		return OutcomeNeedsHuman, "CODEGEN_PIN_UNUSABLE"
	case p.RuntimeVersion == "":
		return OutcomeNeedsHuman, "RUNTIME_PIN_UNUSABLE"
	}
	return "", ""
}

// runtimeRev is the runtime revision for a go.mod version: the tag, or the
// commit of a pseudo-version.
func runtimeRev(v string) (string, error) {
	if module.IsPseudoVersion(v) {
		return module.PseudoVersionRev(v)
	}
	if !semver.IsValid(v) {
		return "", fmt.Errorf("runtime version %q is not a semantic version", v)
	}
	return v, nil
}

// codegenSupportsServicePin reports whether the code-generator checkout has
// ack-generate's --aws-service-sdk-version flag.
func codegenSupportsServicePin(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "cmd", "ack-generate", "command", "root.go"))
	return err == nil && bytes.Contains(b, []byte(`"aws-service-sdk-version"`))
}
