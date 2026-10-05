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

// SmithyShape is one entry in a Smithy model's `shapes` map. Only the subset
// of fields this tool needs is modelled; unknown fields are ignored so that
// additions to the format do not break parsing.
type SmithyShape struct {
	Type    string                     `json:"type"`
	Members map[string]SmithyMemberRef `json:"members"`
	// Member is the element reference of a `list` shape. Smithy encodes this
	// as a top-level singular "member" key, a sibling of "type" — not as an
	// entry inside the "members" map. Verified against a real model:
	// "com.amazonaws.s3files#AccessPoints" is
	// {"type": "list", "member": {"target": "...#ListAccessPointsDescription"}}.
	Member *SmithyMemberRef           `json:"member"`
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

// OperationNames returns the short names of every operation in the model, in
// sorted order so that output is deterministic.
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

// maxWalkDepth bounds how deep the member walk descends into nested
// structures. Six levels is well past anything ACK surfaces in a CRD and
// keeps the output reviewable.
const maxWalkDepth = 6

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
// path. List shapes are traversed transparently, so a list of structures
// contributes the structure's members at the list member's path rather than
// introducing an index segment.
//
// The walk is cycle-guarded by tracking the shape IDs on the current branch,
// and bounded by maxDepth, which is silently capped at maxWalkDepth. An
// unknown or empty shapeID yields an empty map.
//
// The guard is per-branch rather than global on purpose: a global visited set
// would suppress legitimately distinct paths to the same shape, so two sibling
// fields both targeting Tag would only report one of them. Completeness of the
// path enumeration matters more here than avoiding re-visits, which maxDepth
// already bounds.
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

	// Lists are transparent: descend into the element shape at the same path.
	// Note this reads shape.Member (the singular top-level key), not
	// shape.Members["member"] — see the SmithyShape.Member comment.
	if shape.Type == "list" {
		if shape.Member != nil {
			m.walk(shape.Member.Target, prefix, remaining, onBranch, out)
		}
		return
	}

	// Maps are deliberately opaque leaves. Smithy encodes a map's element
	// types under top-level "key"/"value" keys, so they would not be reached
	// by the members loop below anyway — this branch makes the choice explicit
	// rather than incidental. ACK renders an AWS map as a CRD object with
	// additionalProperties, meaning there are no per-key field paths in the
	// CRD to compare against, so descending would emit paths that can never
	// match. Do not "fix" this the way the list branch above was fixed.
	if shape.Type == "map" {
		return
	}

	// Enums are leaves too, and this one is not obvious: a Smithy enum shape
	// carries a `members` map exactly like a structure, one entry per permitted
	// value, each targeting smithy.api#Unit with an enumValue trait. So the
	// loop below would happily descend and report every allowed value as though
	// it were a field.
	//
	// An enum-typed member is a plain string in the CRD; its values are never
	// CRD properties, so those paths can never match and every one is a false
	// finding. Measured on the real s3 model: 73 enum shapes, and the
	// v1.32.6 -> v1.41.5 delta adds 28 enum values — about 40% of producer 3's
	// output for that diff was entries like
	// `CreateBucketConfiguration.LocationConstraint.ap_southeast_4`, one per AWS
	// region. The enum-typed member itself is still recorded at its own path,
	// which is the thing a CRD can actually expose.
	if shape.Type == "enum" || shape.Type == "intEnum" {
		return
	}

	// Structures and unions are both walked here. A union's variants are
	// mutually exclusive, but this function enumerates reachable paths rather
	// than valid combinations, so listing every variant is correct for a diff:
	// the question asked later is only whether a given path appeared or
	// disappeared between two model versions.
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
