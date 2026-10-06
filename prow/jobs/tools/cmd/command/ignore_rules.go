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
	"slices"
	"strings"
)

// The generator.yaml `ignore:` rules, applied by every producer so an ignored
// operation, resource, shape or field path never yields a finding.

// ignoresOperation reports whether ignore.operations lists opID. Exact, as in
// code-generator's OperationIsIgnored.
func (c *generatorConfig) ignoresOperation(opID string) bool {
	return c != nil && slices.Contains(c.Ignore.Operations, opID)
}

// ignoresResource reports whether ignore.resource_names lists a resource name,
// ignoring case as kind lookups here do (AWS VpcEndpoint vs ACK VPCEndpoint).
func (c *generatorConfig) ignoresResource(name string) bool {
	if c == nil || name == "" {
		return false
	}
	return slices.ContainsFunc(c.Ignore.ResourceNames, func(ignored string) bool {
		return strings.EqualFold(ignored, name)
	})
}

// ignoredOpReason returns why generator.yaml ignores an operation, or "" when it
// does not: it is in ignore.operations, or classifies onto an ignored resource
// (CreateFoo, DescribeFoos, BatchCreateFoos for Foo, but not CreateFooBar).
func (in *ControllerInputs) ignoredOpReason(opID string) string {
	if in == nil || in.Config == nil {
		return ""
	}
	if in.Config.ignoresOperation(opID) {
		return "listed in generator.yaml `ignore.operations`"
	}
	opTypes, name := in.ClassifyOpWithOverrides(opID, in.Config.ResourceNames())
	if !opTypes.Has(OpTypeUnknown) && in.Config.ignoresResource(name) {
		return fmt.Sprintf("concerns `%s`, which generator.yaml ignores", name)
	}
	return ""
}

// ignoresOp reports whether generator.yaml ignores an operation; see
// ignoredOpReason.
func (in *ControllerInputs) ignoresOp(opID string) bool {
	return in.ignoredOpReason(opID) != ""
}

// calledOps returns, sorted, the operations a resource's package calls. Ignore
// rules are not applied: an ignored op that hand-written code still calls is
// one the controller reconciles, so gaps in it are real.
func (in *ControllerInputs) calledOps(kind string) []string {
	var out []string
	for op := range in.UsedOps[kindToResourceDir(kind)] {
		out = append(out, op)
	}
	slices.Sort(out)
	return out
}

// ignoresMember reports whether generator.yaml ignores the member at path in an
// operation's request (or response, when isOutput) and so everything beneath it:
// it or an ancestor targets an ignore.shape_names shape, or the path from the
// root falls under an ignore.field_paths entry. members is the walk of ref.
func (c *generatorConfig) ignoresMember(
	m *SmithyModel,
	opID string,
	ref *SmithyMemberRef,
	isOutput bool,
	path string,
	members map[string]MemberInfo,
) bool {
	if c == nil {
		return false
	}
	return declinedShapeAncestor(path, members, c.Ignore.ShapeNames) ||
		declinedFieldPath(m, fieldRootNames(opID, ref, isOutput), path, members, c.Ignore.FieldPaths)
}

// namesIgnoredResource reports whether an operation name mentions a resource in
// ignore.resource_names (e.g. s3's AbortMultipartUpload and MultipartUpload).
//
// It is a substring test because unclassified operations carry no resource
// name. A match does not count when the operation also names a longer known
// resource containing the ignored one (ec2 ignores `Ipam` but reports
// IpamInternetRegistryAssociation). known holds the lowercased names of every
// CRD kind and reported resource; nil means none.
func namesIgnoredResource(opID string, ignoredResourceNames []string, known map[string]string) bool {
	return ignoredNameIn(opID, ignoredResourceNames, known) != ""
}

// ignoredNameIn returns the ignored resource name an operation concerns, by
// namesIgnoredResource's rule, or "" when it concerns none.
func ignoredNameIn(opID string, ignoredResourceNames []string, known map[string]string) string {
	lowered := strings.ToLower(opID)
	for _, ignored := range ignoredResourceNames {
		if ignored == "" {
			continue
		}
		low := strings.ToLower(ignored)
		if !strings.Contains(lowered, low) {
			continue
		}
		longer := false
		for name := range known {
			if len(name) > len(low) && strings.Contains(name, low) && strings.Contains(lowered, name) {
				longer = true
				break
			}
		}
		if !longer {
			return ignored
		}
	}
	return ""
}

// declinedFieldPath reports whether a member path, or an ancestor of it, is in
// ignore.field_paths.
//
// Entries are shape-qualified as codegen reads them: a shape name, then a member
// path inside it (`CreateCapacityReservationInput.DryRun`). So each segment of
// awsPath is matched against its containing shape. The root answers to
// `<Operation>Input`/`<Operation>Output` as well as its Smithy shape name.
func declinedFieldPath(
	m *SmithyModel,
	rootNames []string,
	awsPath string,
	members map[string]MemberInfo,
	declined []string,
) bool {
	segments := strings.Split(awsPath, ".")
	for i := range segments {
		containers := rootNames
		if i > 0 {
			parent, ok := members[strings.Join(segments[:i], ".")]
			if !ok {
				continue
			}
			containers = []string{shapeShortName(elementShape(m, parent.Target))}
		}
		rest := strings.ToLower(strings.Join(segments[i:], "."))
		for _, entry := range declined {
			shape, path, ok := strings.Cut(strings.ToLower(entry), ".")
			if !ok || path == "" {
				continue
			}
			if !slices.ContainsFunc(containers, func(c string) bool { return strings.EqualFold(c, shape) }) {
				continue
			}
			if rest == path || strings.HasPrefix(rest, path+".") {
				return true
			}
		}
	}
	return false
}

// fieldRootNames returns every name a field_paths entry may use for the root of an
// operation's request or response.
func fieldRootNames(opName string, ref *SmithyMemberRef, isOutput bool) []string {
	names := []string{opName + "Input"}
	if isOutput {
		names = []string{opName + "Output"}
	}
	if ref != nil {
		names = append(names, shapeShortName(ref.Target))
	}
	return names
}

// declinedShapeName reports whether a member's target shape is in
// ignore.shape_names, which applies wherever the shape is referenced.
// targetShapeID is an absolute Smithy ID; only its short name is compared.
func declinedShapeName(targetShapeID string, declined []string) bool {
	if targetShapeID == "" {
		return false
	}
	short := shapeShortName(targetShapeID)
	for _, name := range declined {
		if name != "" && strings.EqualFold(name, short) {
			return true
		}
	}
	return false
}

// declinedShapeAncestor reports whether a member path, or any of the paths it
// hangs from, targets a declined shape. Codegen emits nothing beneath a declined
// member, but its children have their own, undeclined targets. WalkMembers
// records every intermediate path, so each prefix is looked up directly.
func declinedShapeAncestor(
	path string,
	members map[string]MemberInfo,
	declined []string,
) bool {
	if len(declined) == 0 {
		return false
	}
	segments := strings.Split(path, ".")
	for i := 1; i <= len(segments); i++ {
		prefix := strings.Join(segments[:i], ".")
		if mi, ok := members[prefix]; ok && declinedShapeName(mi.Target, declined) {
			return true
		}
	}
	return false
}
