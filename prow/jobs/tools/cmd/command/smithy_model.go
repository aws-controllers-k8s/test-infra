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
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const docTraitKey = "smithy.api#documentation"

// SmithyMemberRef is a reference from a structure member, or an operation's
// input/output, to another shape.
type SmithyMemberRef struct {
	Target string                     `json:"target"`
	Traits map[string]json.RawMessage `json:"traits"`
}

// SmithyShape is the subset of a Smithy `shapes` entry this tool needs.
type SmithyShape struct {
	Type    string                     `json:"type"`
	Members map[string]SmithyMemberRef `json:"members"`
	// Member is a `list` shape's element. Smithy encodes it as a top-level
	// "member" key, not inside "members".
	Member *SmithyMemberRef `json:"member"`
	// Value is a `map` shape's value; keys are always strings, so not kept.
	Value  *SmithyMemberRef           `json:"value"`
	Input  *SmithyMemberRef           `json:"input"`
	Output *SmithyMemberRef           `json:"output"`
	Traits map[string]json.RawMessage `json:"traits"`
}

// SmithyModel is an AWS API model as published in aws-sdk-go-v2 under
// codegen/sdk-codegen/aws-models. Shapes are keyed by absolute shape ID, e.g.
// "com.amazonaws.s3#CreateBucket".
type SmithyModel struct {
	Shapes map[string]SmithyShape `json:"shapes"`

	// opsByName maps an operation's short name to its absolute shape ID.
	opsByName map[string]string
}

// LoadSmithyModel parses a Smithy JSON model document.
func LoadSmithyModel(data []byte) (*SmithyModel, error) {
	var m SmithyModel
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("unable to unmarshal smithy model: %s", err)
	}
	if len(m.Shapes) == 0 {
		return nil, fmt.Errorf("smithy model contains no shapes")
	}
	m.opsByName = map[string]string{}
	for id, shape := range m.Shapes {
		if shape.Type == "operation" {
			m.opsByName[shapeShortName(id)] = id
		}
	}
	return &m, nil
}

// OperationNames returns the short names of every operation, sorted.
func (m *SmithyModel) OperationNames() []string {
	names := make([]string, 0, len(m.opsByName))
	for name := range m.opsByName {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Operation looks up an operation by its short name, e.g. "CreateBucket".
func (m *SmithyModel) Operation(name string) (SmithyShape, bool) {
	id, ok := m.opsByName[name]
	if !ok {
		return SmithyShape{}, false
	}
	return m.Shapes[id], true
}

// shapeShortName strips the namespace from an absolute shape ID.
func shapeShortName(shapeID string) string {
	if i := strings.LastIndex(shapeID, "#"); i >= 0 {
		return shapeID[i+1:]
	}
	return shapeID
}

// docTrait returns the documentation trait of a shape or member, or the empty
// string when it carries none.
func docTrait(traits map[string]json.RawMessage) string {
	raw, ok := traits[docTraitKey]
	if !ok {
		return ""
	}
	var doc string
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ""
	}
	return doc
}

// maxWalkDepth is only a backstop: the per-branch cycle guard already ends every
// walk. CRDs nest deeper than six (wafv2's Rules.Statement... reaches nine), and
// the deepest AWS models (quicksight) reach sixteen.
const maxWalkDepth = 32

// MemberInfo describes one member reached by WalkMembers.
type MemberInfo struct {
	// Path is the dotted path from the walk root, e.g. "Config.Size".
	Path string
	// Target is the absolute shape ID the member points at.
	Target string
	// Doc is the member's documentation trait, possibly empty.
	Doc string
}

// WalkMembers returns every member reachable from shapeID, keyed by dotted
// path. Lists and map values are transparent (no index or key segment).
// maxDepth is capped at maxWalkDepth.
//
// Cycles are guarded per branch, not globally, so distinct paths to the same
// shape (two fields targeting Tag) are all reported.
func (m *SmithyModel) WalkMembers(shapeID string, maxDepth int) map[string]MemberInfo {
	out := map[string]MemberInfo{}
	if maxDepth > maxWalkDepth {
		maxDepth = maxWalkDepth
	}
	m.walk(shapeID, "", maxDepth, map[string]bool{}, out)
	return out
}

func (m *SmithyModel) walk(
	shapeID string,
	prefix string,
	remaining int,
	onBranch map[string]bool,
	out map[string]MemberInfo,
) {
	if shapeID == "" || remaining <= 0 || onBranch[shapeID] {
		return
	}
	shape, ok := m.Shapes[shapeID]
	if !ok {
		return
	}

	onBranch[shapeID] = true
	defer delete(onBranch, shapeID)

	// Lists are transparent: descend into the element at the same path.
	if shape.Type == "list" {
		if shape.Member != nil {
			m.walk(shape.Member.Target, prefix, remaining, onBranch, out)
		}
		return
	}

	// Maps are transparent too: ACK renders a structured value as
	// additionalProperties, whose fields readCRDFields records at the map's path.
	if shape.Type == "map" {
		if shape.Value != nil {
			m.walk(shape.Value.Target, prefix, remaining, onBranch, out)
		}
		return
	}

	// Enums are leaves: Smithy gives enum shapes a `members` map of allowed
	// values, which are not CRD fields.
	if shape.Type == "enum" || shape.Type == "intEnum" {
		return
	}

	// Structures and unions; every union variant is a reachable path.
	for name, ref := range shape.Members {
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		out[path] = MemberInfo{
			Path:   path,
			Target: ref.Target,
			Doc:    docTrait(ref.Traits),
		}
		m.walk(ref.Target, path, remaining-1, onBranch, out)
	}
}
