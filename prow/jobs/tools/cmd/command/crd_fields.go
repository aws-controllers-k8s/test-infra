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
}

// readCRDFields returns, per resource kind, the set of field paths the
// controller's CRDs expose. Paths are dotted and lowercased; spec and status
// are merged, since a field being in either means ACK already surfaces it.
// Array item properties are flattened at the array's own path, so
// "rules.prefix" rather than "rules[].prefix".
//
// A missing config/crd/bases directory yields an empty map rather than an
// error: a controller with no CRDs is odd but not a failure of this tool.
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
		// Fail the whole controller on an unreadable or malformed CRD, rather
		// than skipping the file and carrying on.
		//
		// This looks like the less robust choice and is not. The field set is a
		// suppression filter, so an incomplete one produces confidently wrong
		// output rather than a visible failure: a kind missing from the map
		// makes HasCRD report false, so producer 1 announces a resource ACK
		// already generates as brand new, while producers 3 and 4 iterate the
		// map's keys and silently skip that kind entirely. One stray file would
		// buy a false "new resource" claim plus a blind spot, both looking
		// perfectly plausible in the issue.
		//
		// Returning an error instead surfaces as a per-service failure: the
		// service is logged and skipped, every other service still gets its
		// issue, and the job goes red so a human looks. Incomplete data is worse
		// than no data here.
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

		// versions[0] only. Surveyed across all 73 controller repos on disk:
		// every CRD declares exactly one entry in spec.versions, so this is
		// safe today. If ACK ever ships a second version for a resource, fields
		// unique to the later version would be missing from this set and would
		// surface as false "not present in the CRD" findings — at which point
		// this needs to union every version's schema.
		// Merge rather than overwrite. Two files declaring the same kind is
		// against ACK convention, but if it happens — mid-rename, say — an
		// overwrite would drop fields the CRD really does expose, and dropped
		// fields become false findings. A union can only ever over-suppress,
		// which is the safer direction to fail.
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

// flattenCRDProps accumulates into the fields map rather than returning one,
// unlike the other readers in this file. That is deliberate for a recursive
// walk: returning a map per level would mean allocating and merging at every
// node for no benefit.
//
// flattenCRDProps walks an OpenAPI schema node, adding every property path it
// reaches to fields. Array items are transparent: their properties land at the
// array's path.
func flattenCRDProps(node crdProps, prefix string, fields map[string]bool) {
	if node.Items != nil {
		flattenCRDProps(*node.Items, prefix, fields)
	}
	for name, child := range node.Properties {
		path := strings.ToLower(name)
		if prefix != "" {
			path = prefix + "." + path
		}
		fields[path] = true
		// ACK's codegen suffixes an underscore onto property names that would
		// collide with a Go keyword, so the AWS member `Type` is emitted as the
		// CRD property `type_`. Record the un-suffixed spelling as well, or a
		// suppression lookup keyed on the AWS member name (`Type` -> `type`)
		// misses and we report a field the CRD already exposes.
		//
		// Surveyed across all 76 controllers: `type_` is the only
		// underscore-suffixed property, 45 occurrences, and in every one of
		// them the suffixed segment is the final path segment — hence trimming
		// the tail of the joined path is sufficient. If codegen ever escapes a
		// non-leaf name, this needs to trim per segment instead.
		if trimmed := strings.TrimRight(path, "_"); trimmed != path {
			fields[trimmed] = true
		}
		flattenCRDProps(child, path, fields)
	}
}
