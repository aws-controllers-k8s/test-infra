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
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// generatorConfig is the subset of generator.yaml this tool needs (full schema
// in code-generator's pkg/config), kept small so unrelated schema changes
// cannot break it.
type generatorConfig struct {
	Ignore   ignoreConfig   `yaml:"ignore"`
	SDKNames sdkNamesConfig `yaml:"sdk_names"`
	// Operations carries per-operation overrides, which code-generator applies
	// before name inference.
	Operations map[string]operationOverride `yaml:"operations"`
	Resources  map[string]resourceConfig    `yaml:"resources"`
}

// ignoreConfig lists what a controller intentionally does not generate.
type ignoreConfig struct {
	Operations    []string `yaml:"operations"`
	ResourceNames []string `yaml:"resource_names"`
	// FieldPaths are dotted shape-member paths codegen skips, e.g. s3's
	// `VersioningConfiguration.MFADelete`.
	FieldPaths []string `yaml:"field_paths"`
	// ShapeNames skip a member whose target shape has one of these names,
	// whatever path reaches it.
	ShapeNames []string `yaml:"shape_names"`
}

// sdkNamesConfig overrides the names used to locate the SDK model, e.g.
// route53's model is `route-53` and documentdb's package is `docdb`.
type sdkNamesConfig struct {
	ModelName   string `yaml:"model_name"`
	PackageName string `yaml:"package_name"`
}

// resourceConfig is the per-resource block of generator.yaml, narrowed to the
// renames and field sourcing this tool needs.
type resourceConfig struct {
	Renames resourceRenames `yaml:"renames"`
	// Fields declares CRD fields sourced from another operation's shape via
	// `from`, e.g. s3 Bucket's `Abac` comes from PutBucketAbac's AbacStatus:
	//
	//	fields:
	//	  Abac:
	//	    from:
	//	      operation: PutBucketAbac
	//	      path: AbacStatus
	Fields map[string]resourceFieldConfig `yaml:"fields"`
	// UpdateOperation names hand-written update code that replaces codegen's,
	// so fields it sets need that code changed rather than a new hook.
	UpdateOperation struct {
		CustomMethodName string `yaml:"custom_method_name"`
	} `yaml:"update_operation"`
}

type resourceFieldConfig struct {
	From *resourceFieldFrom `yaml:"from"`
}

type resourceFieldFrom struct {
	Operation string `yaml:"operation"`
	Path      string `yaml:"path"`
}

type resourceRenames struct {
	Operations map[string]operationRenames `yaml:"operations"`
}

// operationRenames maps AWS member names to the ACK field names codegen
// generates for them, per operation.
type operationRenames struct {
	InputFields  map[string]string `yaml:"input_fields"`
	OutputFields map[string]string `yaml:"output_fields"`
}

// operationOverride is the subset of generator.yaml's per-operation config
// that affects classification.
type operationOverride struct {
	OperationType stringArray `yaml:"operation_type"`
	ResourceName  stringArray `yaml:"resource_name"`
	// OutputWrapperFieldPath names the response member codegen reads the
	// resource's fields from, e.g. iam's GetGroup declares `Group`.
	OutputWrapperFieldPath string `yaml:"output_wrapper_field_path"`
	// InputWrapperFieldPath is the request-side counterpart: codegen flattens
	// this member's members into Spec and drops other request members.
	InputWrapperFieldPath string `yaml:"input_wrapper_field_path"`
}

// stringArray accepts a YAML scalar or sequence, mirroring code-generator's
// StringArray. yaml.v3 does not call UnmarshalYAML for null values, so those
// stay empty.
type stringArray []string

func (s *stringArray) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		var single string
		if err := node.Decode(&single); err != nil {
			return err
		}
		*s = stringArray{single}
		return nil
	case yaml.SequenceNode:
		var many []string
		if err := node.Decode(&many); err != nil {
			return err
		}
		*s = many
		return nil
	default:
		return fmt.Errorf("expected scalar or sequence for string array, got kind %d", node.Kind)
	}
}

// readGeneratorConfig reads a controller's generator.yaml.
func readGeneratorConfig(controllerPath string) (*generatorConfig, error) {
	path := filepath.Join(controllerPath, "generator.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("unable to read %s: %s", path, err)
	}

	var cfg generatorConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("unable to unmarshal %s: %s", path, err)
	}
	return &cfg, nil
}

// ResourceNames returns the declared resource names, sorted. ClassifyOp uses
// them to handle "pluralized singular" names as codegen does.
func (c *generatorConfig) ResourceNames() []string {
	names := make([]string, 0, len(c.Resources))
	for name := range c.Resources {
		names = append(names, name)
	}
	// Sorted so output is deterministic; findings are fingerprinted to detect
	// GitHub issue changes.
	sort.Strings(names)
	return names
}

// resource returns the config block for a resource kind, matched
// case-insensitively as code-generator's GetResourceConfig does: CRD kinds
// uppercase acronyms (VPCEndpoint) while config keys may not (VpcEndpoint).
// An exact match wins, then the first case-insensitive match in sorted order.
func (c *generatorConfig) resource(kind string) (resourceConfig, bool) {
	if c == nil {
		return resourceConfig{}, false
	}
	if res, ok := c.Resources[kind]; ok {
		return res, true
	}
	for _, name := range c.ResourceNames() {
		if strings.EqualFold(name, kind) {
			return c.Resources[name], true
		}
	}
	return resourceConfig{}, false
}

// RenamesForResource returns a flat AWS-member-name to ACK-field-name map for
// one resource, collapsed across operations and across input and output
// fields. ACK keeps renames consistent within a resource, so collapsing is
// safe and keeps the suppression lookup simple.
func (c *generatorConfig) RenamesForResource(kind string) map[string]string {
	res, ok := c.resource(kind)
	if !ok {
		return nil
	}

	// Merge in sorted order so conflicting renames resolve deterministically.
	opNames := make([]string, 0, len(res.Renames.Operations))
	for opName := range res.Renames.Operations {
		opNames = append(opNames, opName)
	}
	sort.Strings(opNames)

	out := map[string]string{}
	for _, opName := range opNames {
		op := res.Renames.Operations[opName]
		for from, to := range op.InputFields {
			out[from] = to
		}
		for from, to := range op.OutputFields {
			out[from] = to
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// renamedPath applies a resource's renames to an AWS member path, segment by
// segment. A key is a member name or a dotted path renaming its last segment;
// a dotted key's parents may be spelled as in AWS or already renamed (as in
// firehose), so both are tried. Keys match exactly, as in code-generator's
// GetResourceFieldName.
func (c *generatorConfig) renamedPath(kind string, segments []string) []string {
	out := slices.Clone(segments)
	renames := c.RenamesForResource(kind)
	if len(renames) == 0 {
		return out
	}
	for i := range segments {
		original := strings.Join(segments[:i+1], ".")
		renamed, ok := renames[original]
		if !ok && i > 0 {
			renamed, ok = renames[strings.Join(out[:i], ".")+"."+segments[i]]
		}
		if ok {
			out[i] = renamed
		}
	}
	return out
}

// inputWrapper returns the request member codegen flattens into Spec for an
// operation, or "" when it reads the request as-is. See
// operationOverride.InputWrapperFieldPath.
func (c *generatorConfig) inputWrapper(opName string) string {
	if c == nil {
		return ""
	}
	return c.Operations[opName].InputWrapperFieldPath
}
