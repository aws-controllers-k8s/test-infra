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
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	awssdkmodel "github.com/aws-controllers-k8s/code-generator/pkg/api"
	ackgenconfig "github.com/aws-controllers-k8s/code-generator/pkg/config"
	ackfp "github.com/aws-controllers-k8s/code-generator/pkg/fieldpath"
	acksdk "github.com/aws-controllers-k8s/code-generator/pkg/sdk"
	"gopkg.in/yaml.v3"
)

type ignoreSpec struct {
	FieldPaths    []string `yaml:"field_paths"`
	ShapeNames    []string `yaml:"shape_names"`
	Operations    []string `yaml:"operations"`
	ResourceNames []string `yaml:"resource_names"`
}

func (s ignoreSpec) empty() bool {
	return len(s.FieldPaths)+len(s.ShapeNames)+len(s.Operations)+len(s.ResourceNames) == 0
}

// generatorConfig is the generator.yaml subset dod reads. It is decoded
// loosely: keys this code-generator version does not know are not errors.
type generatorConfig struct {
	Ignore   ignoreSpec `yaml:"ignore"`
	SDKNames struct {
		ModelName   string `yaml:"model_name"`
		PackageName string `yaml:"package_name"`
	} `yaml:"sdk_names"`
}

func readGeneratorConfig(b []byte) (generatorConfig, error) {
	var c generatorConfig
	if b == nil {
		return c, nil
	}
	err := yaml.Unmarshal(b, &c)
	return c, err
}

// modelName is the aws-models file base name (generator.yaml sdk_names).
func modelName(service string, c generatorConfig) string {
	return filepath.Base(cmp.Or(c.SDKNames.ModelName, service))
}

func addedEntries(base, head []string) []string {
	var out []string
	for _, h := range sortedUnique(head) {
		if !slices.Contains(base, h) {
			out = append(out, h)
		}
	}
	return out
}

func addedIgnores(base, head generatorConfig) ignoreSpec {
	return ignoreSpec{
		FieldPaths:    addedEntries(base.Ignore.FieldPaths, head.Ignore.FieldPaths),
		ShapeNames:    addedEntries(base.Ignore.ShapeNames, head.Ignore.ShapeNames),
		Operations:    addedEntries(base.Ignore.Operations, head.Ignore.Operations),
		ResourceNames: addedEntries(base.Ignore.ResourceNames, head.Ignore.ResourceNames),
	}
}

// loadAPI loads a model with code-generator's own loader (ACK shape names,
// before any ignore rule), from a throwaway copy of the aws-models layout.
func loadAPI(model []byte, name string) (api *awssdkmodel.API, err error) {
	defer func() {
		if r := recover(); r != nil {
			api, err = nil, fmt.Errorf("code-generator panicked: %v", r)
		}
	}()
	dir, err := os.MkdirTemp("", "ack-dod-model-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	modelDir := filepath.Join(dir, "codegen", "sdk-codegen", "aws-models")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(modelDir, name+".json"), model, 0o644); err != nil {
		return nil, err
	}
	sdkapi, err := acksdk.NewHelper(dir, ackgenconfig.Config{}).API(name)
	if err != nil {
		return nil, err
	}
	return sdkapi.API, nil
}

// fieldPathEffect mirrors code-generator's ApplyShapeIgnoreRules: it errors on
// an unknown first shape, walks to the parent case-insensitively, then deletes
// the last segment by exact name from a structure's members. Anything else is
// a silent no-op, which is what this reports.
func fieldPathEffect(api *awssdkmodel.API, entry string) (sig, why string) {
	defer func() {
		if r := recover(); r != nil {
			sig, why = "IGNORE_FIELD_PATH_NO_EFFECT", "the path does not resolve"
		}
	}()
	fp := ackfp.FromString(entry)
	if fp.Size() < 2 {
		return "IGNORE_FIELD_PATH_INVALID", "want <Shape>.<Member>[.<Member>...]"
	}
	sn := fp.At(0)
	shape, ok := api.Shapes[sn]
	if !ok {
		return "IGNORE_UNKNOWN_SHAPE", "no shape " + sn
	}
	last := fp.Pop()
	parent := fp.ShapeRef(&awssdkmodel.ShapeRef{ShapeName: sn, Shape: shape})
	if parent == nil || parent.Shape == nil || parent.Shape.Type != "structure" {
		return "IGNORE_FIELD_PATH_NO_EFFECT", "the parent of " + last + " is not a structure"
	}
	if _, ok := parent.Shape.MemberRefs[last]; !ok {
		return "IGNORE_FIELD_PATH_NO_EFFECT", "no member named exactly " + last
	}
	return "", ""
}

func shapeUsed(api *awssdkmodel.API, sn string) bool {
	exists := false
	for _, s := range api.Shapes {
		exists = exists || s.ShapeName == sn
	}
	if !exists {
		return false
	}
	for _, s := range api.Shapes {
		if s.MemberRef.ShapeName == sn || s.ValueRef.ShapeName == sn {
			return true
		}
		for _, m := range s.MemberRefs {
			if m.ShapeName == sn {
				return true
			}
		}
	}
	return false
}

// checkIgnore checks that every added ignore entry removes something (design
// §6 ignore-effective: ValidateConfig does not check them).
func checkIgnore(added ignoreSpec, api *awssdkmodel.API) Check {
	var findings []Finding
	bad := func(sig, entry, why string) {
		findings = append(findings, Finding{Signature: sig, File: "generator.yaml", Detail: entry + ": " + why, Fails: true})
	}
	for _, e := range added.FieldPaths {
		if sig, why := fieldPathEffect(api, e); sig != "" {
			bad(sig, e, why)
		}
	}
	for _, sn := range added.ShapeNames {
		if !shapeUsed(api, sn) {
			bad("IGNORE_SHAPE_NO_EFFECT", sn, "no member uses this shape")
		}
	}
	for _, op := range added.Operations {
		if _, ok := api.Operations[op]; !ok {
			bad("IGNORE_OPERATION_UNKNOWN", op, "no such operation")
		}
	}
	for _, rn := range added.ResourceNames {
		if !slices.ContainsFunc(sortedKeys(api.Operations), func(op string) bool { return strings.HasSuffix(op, rn) }) {
			bad("IGNORE_RESOURCE_UNMATCHED", rn, "no operation ends with this name")
		}
	}
	return newCheck(CheckIgnore, findings)
}

// ignoreCheck reads both generator.yaml sides and loads the model only when
// something was added.
func ignoreCheck(baseGen, headGen, model []byte, name string) Check {
	base, err1 := readGeneratorConfig(baseGen)
	head, err2 := readGeneratorConfig(headGen)
	if err1 != nil || err2 != nil {
		return newCheck(CheckIgnore, []Finding{{Signature: "GENERATOR_CONFIG_UNREADABLE", File: "generator.yaml", Fails: true}})
	}
	added := addedIgnores(base, head)
	if added.empty() {
		return newCheck(CheckIgnore, nil)
	}
	if model == nil {
		return skipCheck(CheckIgnore, "SDK_MODEL_MISSING")
	}
	api, err := loadAPI(model, name)
	if err != nil {
		return newCheck(CheckIgnore, []Finding{{Signature: "SDK_MODEL_INVALID", Fails: true}})
	}
	return checkIgnore(added, api)
}
