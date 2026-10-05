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
	"sort"

	"gopkg.in/yaml.v3"
)

// generatorConfig is the subset of a controller's generator.yaml this tool
// needs. The full schema lives in code-generator's pkg/config; we deliberately
// model only these fields so that unrelated schema changes cannot break us.
type generatorConfig struct {
	Ignore   ignoreConfig   `yaml:"ignore"`
	SDKNames sdkNamesConfig `yaml:"sdk_names"`
	// Operations carries per-operation overrides. code-generator's
	// GetOperationMap consults these before falling back to name inference, so
	// an operation listed here is classified by declaration rather than by its
	// prefix. route53 is the canonical case: `ChangeResourceRecordSets` has no
	// recognisable prefix, and its generator.yaml declares
	// `operation_type: [Create, Delete]` to bind it to a resource.
	Operations map[string]operationOverride `yaml:"operations"`
	Resources  map[string]resourceConfig    `yaml:"resources"`
}

// ignoreConfig lists what a controller intentionally does not generate.
type ignoreConfig struct {
	Operations    []string `yaml:"operations"`
	ResourceNames []string `yaml:"resource_names"`
	// FieldPaths are shape-member paths codegen is told to skip, in the same
	// dotted form the Smithy member walk produces — s3 declares
	// `CreateBucketConfiguration.Tags` and `VersioningConfiguration.MFADelete`.
	// Producer 3 must consult these or it reports fields the controller
	// deliberately declined as though they were gaps.
	FieldPaths []string `yaml:"field_paths"`
	// ShapeNames suppress a member wherever its *target shape* carries one of
	// these names, independently of the path it is reached by. s3 declares
	// `BlockedEncryptionTypes`, which suppressed four producer-3 findings
	// reached via two different operations.
	ShapeNames []string `yaml:"shape_names"`
}

// sdkNamesConfig overrides the names used to locate the SDK model. All three
// can differ from the service alias: route53's model is `route-53`,
// opensearchservice's is `opensearch`, documentdb's package is `docdb`.
type sdkNamesConfig struct {
	ModelName   string `yaml:"model_name"`
	PackageName string `yaml:"package_name"`
}

// resourceConfig is the per-resource block of generator.yaml, narrowed to the
// renames and field sourcing this tool needs.
type resourceConfig struct {
	Renames resourceRenames `yaml:"renames"`
	// Fields declares CRD fields assembled from somewhere other than the
	// resource's own create input. This is a distinct mechanism from Renames:
	// a rename maps a member within one operation, whereas `from` sources a
	// field out of a *different* operation's shape.
	//
	// s3's Bucket declares:
	//
	//	fields:
	//	  Abac:
	//	    from:
	//	      operation: PutBucketAbac
	//	      path: AbacStatus
	//
	// so the CRD field `abac` is the AWS member `AbacStatus` on PutBucketAbac.
	// Without reading this, producer 3 reports AbacStatus as missing from a CRD
	// that plainly exposes it — four findings on the real s3 delta.
	Fields map[string]resourceFieldConfig `yaml:"fields"`
	// UpdateOperation names hand-written update code that replaces codegen's.
	// lambda's Function declares `custom_method_name: customUpdateFunction`, which
	// calls UpdateFunctionConfiguration itself, so a field set there needs that
	// code changed rather than a new hook.
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
}

// stringArray accepts either a YAML scalar or a YAML sequence, mirroring
// code-generator's StringArray. Both spellings appear in real generator.yaml
// files, so a plain []string would fail to parse the scalar form.
//
// An explicitly null value (`operation_type:` with nothing after it) never
// reaches this method — yaml.v3 skips custom unmarshalling for null nodes and
// leaves the field at its zero value, which behaves the same as absent. The
// error branch below therefore only fires for a mapping or alias node.
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

// ResourceNames returns the resource names declared in the config. These feed
// ClassifyOp so that "pluralized singular" names are handled the way codegen
// handles them.
func (c *generatorConfig) ResourceNames() []string {
	names := make([]string, 0, len(c.Resources))
	for name := range c.Resources {
		names = append(names, name)
	}
	// Sorted, not map order. This tool fingerprints its findings to decide
	// whether an existing GitHub issue needs updating, so any map-iteration
	// order that can reach the output risks spurious issue churn between runs.
	// Cheap to guarantee here rather than audit every consumer.
	sort.Strings(names)
	return names
}

// RenamesForResource returns a flat AWS-member-name to ACK-field-name map for
// one resource, collapsed across operations and across input and output
// fields. ACK keeps renames consistent within a resource, so collapsing is
// safe and keeps the suppression lookup simple.
func (c *generatorConfig) RenamesForResource(kind string) map[string]string {
	res, ok := c.Resources[kind]
	if !ok {
		return nil
	}

	// Merge in sorted operation order. Collapsing assumes ACK keeps renames
	// consistent within a resource, which is convention rather than something
	// the schema enforces. If that ever breaks, last-write-wins over map
	// iteration order would let a different rename win on different runs, and
	// the resulting field-suppression difference would churn GitHub issues.
	// Sorting makes the outcome at least deterministic.
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
