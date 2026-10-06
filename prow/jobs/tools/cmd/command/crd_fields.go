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
	"strings"

	"gopkg.in/yaml.v3"
)

// crdDoc is the subset of a CustomResourceDefinition needed to recover the
// resource kind and its spec/status property tree.
type crdDoc struct {
	Spec struct {
		Names struct {
			Kind string `yaml:"kind"`
		} `yaml:"names"`
		Versions []struct {
			Schema struct {
				OpenAPIV3Schema crdProps `yaml:"openAPIV3Schema"`
			} `yaml:"schema"`
		} `yaml:"versions"`
	} `yaml:"spec"`
}

// crdProps is a node in an OpenAPI v3 schema.
type crdProps struct {
	Type       string              `yaml:"type"`
	Properties map[string]crdProps `yaml:"properties"`
	Items      *crdProps           `yaml:"items"`
	// AdditionalProperties is a map's value schema; empty when the CRD gives a
	// boolean.
	AdditionalProperties *crdProps `yaml:"additionalProperties"`
}

// UnmarshalYAML accepts the boolean form of additionalProperties, which has no
// fields to record.
func (p *crdProps) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		*p = crdProps{}
		return nil
	}
	type plain crdProps
	return node.Decode((*plain)(p))
}

// readCRDFields returns, per resource kind, the set of field paths the
// controller's CRDs expose. Paths are dotted and lowercased; spec and status
// are merged, since a field in either is already surfaced. A missing
// config/crd/bases directory yields an empty map, not an error.
func readCRDFields(controllerPath string) (map[string]map[string]bool, error) {
	dir := filepath.Join(controllerPath, "config", "crd", "bases")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]map[string]bool{}, nil
		}
		return nil, fmt.Errorf("unable to read %s: %s", dir, err)
	}

	out := map[string]map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		// Fail rather than skip a bad CRD: the field set is a suppression
		// filter, so a missing kind would yield plausible but wrong findings
		// instead of a visible per-service failure.
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("unable to read %s: %s", path, err)
		}

		var doc crdDoc
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("unable to unmarshal %s: %s", path, err)
		}
		kind := doc.Spec.Names.Kind
		if kind == "" || len(doc.Spec.Versions) == 0 {
			continue
		}

		// Only versions[0] is read; every ACK CRD currently has one version.
		// Merge rather than overwrite if two files declare the same kind: a
		// union can only over-suppress, which is the safer failure.
		fields, ok := out[kind]
		if !ok {
			fields = map[string]bool{}
			out[kind] = fields
		}

		root := doc.Spec.Versions[0].Schema.OpenAPIV3Schema
		for _, section := range []string{"spec", "status"} {
			if node, ok := root.Properties[section]; ok {
				flattenCRDProps(node, "", fields)
			}
		}
	}
	return out, nil
}

// flattenCRDProps adds every property path under node to fields. Array items
// and map values are transparent, so "rules.prefix" rather than "rules[].prefix",
// matching WalkMembers.
func flattenCRDProps(node crdProps, prefix string, fields map[string]bool) {
	if node.Items != nil {
		flattenCRDProps(*node.Items, prefix, fields)
	}
	if node.AdditionalProperties != nil {
		flattenCRDProps(*node.AdditionalProperties, prefix, fields)
	}
	for name, child := range node.Properties {
		path := strings.ToLower(name)
		if prefix != "" {
			path = prefix + "." + path
		}
		fields[path] = true
		// codegen suffixes Go keywords with "_" (AWS `Type` becomes `type_`),
		// so also record the unsuffixed name. Trimming only the path tail is
		// enough because `type_` only appears as a leaf today.
		if trimmed := strings.TrimRight(path, "_"); trimmed != path {
			fields[trimmed] = true
		}
		flattenCRDProps(child, path, fields)
	}
}
