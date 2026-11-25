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
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// sensitiveName is design §6's name rule, the same as code-generator
// pkg/crdcompat's.
var sensitiveName = regexp.MustCompile(`(?i)password|secret|token|privatekey|credential`)

const sensitiveTrait = "smithy.api#sensitive"

type smithyRef struct {
	Target string                     `json:"target"`
	Traits map[string]json.RawMessage `json:"traits"`
}

type smithyShape struct {
	Type    string                     `json:"type"`
	Members map[string]smithyRef       `json:"members"`
	Member  *smithyRef                 `json:"member"`
	Value   *smithyRef                 `json:"value"`
	Traits  map[string]json.RawMessage `json:"traits"`
}

// smithySensitiveNames returns the lowercased names of every structure or
// union member the model marks @sensitive: on the member, on its target, or on
// a list element or map value target (code-generator's Shape.IsSensitive).
// code-generator's v2 loader drops the trait, so the raw JSON is read.
func smithySensitiveNames(model []byte) (map[string]bool, error) {
	var d struct {
		Shapes map[string]smithyShape `json:"shapes"`
	}
	if err := json.Unmarshal(model, &d); err != nil {
		return nil, fmt.Errorf("smithy model: %w", err)
	}
	if len(d.Shapes) == 0 {
		return nil, errors.New("smithy model has no shapes")
	}
	marked := func(id string) bool { _, ok := d.Shapes[id].Traits[sensitiveTrait]; return ok }
	sensitive := func(id string) bool {
		s := d.Shapes[id]
		switch {
		case marked(id):
			return true
		case (s.Type == "list" || s.Type == "set") && s.Member != nil:
			return marked(s.Member.Target)
		case s.Type == "map" && s.Value != nil:
			return marked(s.Value.Target)
		}
		return false
	}
	out := map[string]bool{}
	for _, s := range d.Shapes {
		if s.Type != "structure" && s.Type != "union" {
			continue
		}
		for name, ref := range s.Members {
			if _, ok := ref.Traits[sensitiveTrait]; ok || sensitive(ref.Target) {
				out[strings.ToLower(name)] = true
			}
		}
	}
	return out, nil
}

// crdVersionSchemas returns each version's openAPIV3Schema from the first
// YAML document of a CRD file.
func crdVersionSchemas(data []byte) ([]map[string]any, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var d map[string]any
		if err := dec.Decode(&d); err != nil {
			if errors.Is(err, io.EOF) {
				return nil, errors.New("no YAML document")
			}
			return nil, err
		}
		if d == nil {
			continue
		}
		spec, _ := d["spec"].(map[string]any)
		versions, _ := spec["versions"].([]any)
		var out []map[string]any
		for _, v := range versions {
			vm, _ := v.(map[string]any)
			schema, _ := vm["schema"].(map[string]any)
			if root, ok := schema["openAPIV3Schema"].(map[string]any); ok {
				out = append(out, root)
			}
		}
		return out, nil
	}
}

// nodeAt follows a kro schema path (".properties.spec.properties.a.items").
func nodeAt(root map[string]any, kroPath string) (map[string]any, bool) {
	node := root
	segs := strings.Split(strings.TrimPrefix(kroPath, "."), ".")
	for i := 0; i < len(segs); i++ {
		switch segs[i] {
		case "properties":
			if i+1 >= len(segs) {
				return nil, false
			}
			i++
			props, _ := node["properties"].(map[string]any)
			next, ok := props[segs[i]].(map[string]any)
			if !ok {
				return nil, false
			}
			node = next
		case "items", "additionalProperties":
			next, ok := node[segs[i]].(map[string]any)
			if !ok {
				return nil, false
			}
			node = next
		default:
			return nil, false
		}
	}
	return node, true
}

// lastProperty is the name after the last "properties" segment of a kro path.
func lastProperty(kroPath string) string {
	segs := strings.Split(kroPath, ".")
	for i := len(segs) - 2; i >= 0; i-- {
		if segs[i] == "properties" {
			return segs[i+1]
		}
	}
	return ""
}

// isSecretRef reports an ACK SecretKeyReference: key, name and namespace, all
// strings. Its is_secret decision was made, so it is never a candidate.
func isSecretRef(node map[string]any) bool {
	props, _ := node["properties"].(map[string]any)
	for _, k := range []string{"key", "name", "namespace"} {
		p, _ := props[k].(map[string]any)
		if p == nil || p["type"] != "string" {
			return false
		}
	}
	return true
}

const maxSchemaDepth = 64

func joinField(field, name string) string {
	if field == "" {
		return name
	}
	return field + "." + name
}

// walkFields visits node and every property beneath it; list elements and map
// values are transparent ("[]", "{}"), as in Plan 4's field paths. visit
// returns false to stop descending.
func walkFields(node map[string]any, field, name string, depth int, visit func(field, name string, node map[string]any) bool) {
	if depth > maxSchemaDepth || !visit(field, name, node) {
		return
	}
	walkChildren(node, field, depth, visit)
}

func walkChildren(node map[string]any, field string, depth int, visit func(field, name string, node map[string]any) bool) {
	if props, ok := node["properties"].(map[string]any); ok {
		for _, k := range sortedKeys(props) {
			if child, ok := props[k].(map[string]any); ok {
				walkFields(child, joinField(field, k), k, depth+1, visit)
			}
		}
	}
	if items, ok := node["items"].(map[string]any); ok {
		walkChildren(items, field+"[]", depth+1, visit)
	}
	if ap, ok := node["additionalProperties"].(map[string]any); ok {
		walkChildren(ap, field+"{}", depth+1, visit)
	}
}

// addedBuckets hold the changes that add fields.
var addedBuckets = []string{"added_optional_spec", "added_status", "added_required", "crd_added"}

// checkSensitive fails on any added field (or field under one) whose name
// matches the design regex or a Smithy @sensitive member (decisions 11, 12).
// crds maps a CRD file to its head version schemas; extra are findings the
// caller made while loading them.
func checkSensitive(d *crdCompat, crds map[string][]map[string]any, smithy map[string]bool, extra []Finding) Check {
	type key struct{ file, field string }
	seen := map[key]bool{}
	findings := append([]Finding{}, extra...)
	add := func(file, field, sig string) {
		if seen[key{file, field}] {
			return
		}
		seen[key{file, field}] = true
		findings = append(findings, Finding{Signature: sig, File: file, Detail: "field " + field, Fails: true})
	}
	for _, c := range d.SensitiveCandidates {
		add(c.File, c.Field, "SENSITIVE_NAME")
	}
	for _, b := range addedBuckets {
		for _, ch := range d.Buckets[b] {
			for _, root := range crds[ch.File] {
				visit := func(field, name string, n map[string]any) bool {
					if isSecretRef(n) {
						return false
					}
					switch {
					case name == "":
					case sensitiveName.MatchString(name):
						add(ch.File, field, "SENSITIVE_NAME")
					case smithy[strings.ToLower(name)]:
						add(ch.File, field, "SENSITIVE_SMITHY")
					}
					return true
				}
				if b == "crd_added" {
					walkChildren(root, "", 0, visit)
					continue
				}
				if node, ok := nodeAt(root, ch.Path); ok {
					walkFields(node, ch.Field, lastProperty(ch.Path), 0, visit)
				}
			}
		}
	}
	c := newCheck(CheckSensitive, findings)
	if c.Status == StatusFail {
		c.Reclassify = ClassHandWritten
	}
	return c
}
