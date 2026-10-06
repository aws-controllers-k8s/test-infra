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
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Placing operations whose verb ClassifyOp does not recognise (it only knows
// CRUD verbs), e.g. PutAccessPointScope or StartFlowCapture. Each is attached to
// a resource, flagged as a possible resource, or dropped; anything no rule
// places stays in the unclassified section so a miss is visible.

// opPlacement is where placeUnknownOp puts an operation.
type opPlacement int

const (
	// placeUnclassified: no rule applies; report it for a human to place.
	placeUnclassified opPlacement = iota
	// placeDrop: ACK can do nothing with it. The returned name is a drop*
	// reason, shown in a collapsed "dropped" block of the issue.
	placeDrop
	// placeOnResource: an operation on the named resource, existing or new.
	placeOnResource
	// placePossibleResource: manages a resource of its own that has no Create
	// verb, so codegen cannot see it without an `operations:` override.
	placePossibleResource
)

// Reasons placeUnknownOp gives for a drop, as rendered in the issue.
const (
	dropQuery        = "query: reads data, nothing to reconcile"
	dropEndUser      = "acts on end users, which are not ACK resources"
	dropAction       = "action: runs once, no desired state"
	dropAccountLevel = "account-level setting: the request names no resource, and ACK models per-resource state; " +
		"a singleton resource for it would need custom code"
)

var (
	// Verbs that run something once, so have no desired state to reconcile.
	actionVerbs = []string{
		"Acquire", "Cancel", "Execute", "Insight", "Invoke", "Preview", "Publish",
		"Rollback", "Send", "Simulate", "Start", "Stop", "Terminate", "Test", "Validate",
	}
	// Search reads data whatever follows it; Get, Describe and List read data
	// after a Batch or Admin prefix, which is how they escape ClassifyOp.
	queryVerbs = []string{"Search"}
	readVerbs  = []string{"Describe", "Get", "List"}

	// Request members that are never resource identifiers.
	nonIdentifierMembers = []string{"ClientToken", "DryRun", "MaxResults", "NextToken", "UpdateToken"}

	unknownOpRE  = regexp.MustCompile(`^(Batch|Admin)?([A-Z][a-z]+)(.*)$`)
	trailingToRE = regexp.MustCompile(`(?:To|From|For)([A-Z]\w*)$`)
	identifierRE = regexp.MustCompile(`(?i)(name|arn|id|identifier)s?$`)
)

// placeUnknownOp decides where an OpTypeUnknown operation belongs. resources maps
// lowercased names of existing and new resources to their spelling; existing
// marks those with a CRD. Rule order matters:
//
//  1. Queries and Admin* operations are dropped.
//  2. A name that is a known resource places it there; an action verb only
//     against a new resource (Stop<Job> may be its delete).
//  3. Other action verbs are dropped.
//  4. A name with its own CRUD siblings goes to a prefixing resource whose read
//     identifiers the request carries, else a resource the request names, else
//     is account-level (no identifiers) or a possible new resource.
//  5. Otherwise a resource the request names, or else a prefixing resource.
//  6. No identifiers at all means an account-level setting.
func placeUnknownOp(
	opID string,
	model *SmithyModel,
	resources map[string]string,
	existing map[string]bool,
) (opPlacement, string) {
	m := unknownOpRE.FindStringSubmatch(opID)
	if m == nil {
		return placeUnclassified, ""
	}
	prefix, verb, rest := m[1], m[2], m[3]
	request := requestMembers(model, opID)
	identifiers := identifierMembers(request)
	// A member named for a known resource identifies it too: s3's bare `Bucket`.
	for member := range request {
		if _, ok := resources[strings.ToLower(member)]; ok && !slices.Contains(identifiers, member) {
			identifiers = append(identifiers, member)
		}
	}

	if slices.Contains(queryVerbs, verb) || (prefix != "" && slices.Contains(readVerbs, verb)) {
		return placeDrop, dropQuery
	}
	if prefix == "Admin" {
		// Cognito's Admin* operations act on end users, which are not ACK
		// resources: AdminDeleteSoftwareToken, AdminGetUserAuthFactors.
		return placeDrop, dropEndUser
	}

	targets := nameTargets(rest)
	for _, target := range targets {
		name, ok := resources[strings.ToLower(target)]
		if !ok {
			continue
		}
		if slices.Contains(actionVerbs, verb) && existing[name] {
			return placeDrop, dropAction
		}
		return placeOnResource, name
	}
	if slices.Contains(actionVerbs, verb) {
		return placeDrop, dropAction
	}

	if own := ownResource(model, targets); own != "" {
		if parent := parentResource(model, own, request, resources); parent != "" {
			return placeOnResource, parent
		}
		if named := resourceNamedBy(identifiers, resources); named != "" {
			return placeOnResource, named
		}
		if len(identifiers) == 0 {
			return placeDrop, dropAccountLevel
		}
		return placePossibleResource, own
	}

	// A resource the request names beats one the name starts with
	// (EnableApplicationStatusCheckSuppression takes InstanceIds).
	named := resourceNamedBy(identifiers, resources)
	if parent := longestPrefixResource(rest, resources); parent != "" && (named == "" || named == parent) {
		return placeOnResource, parent
	}
	if named != "" {
		return placeOnResource, named
	}
	if len(identifiers) == 0 {
		return placeDrop, dropAccountLevel
	}
	return placeUnclassified, ""
}

// nameTargets lists the resource names an operation's verb-less remainder could
// mean: the target after a To/From/For (AttachRuleGroupsToProxyConfiguration
// attaches *to* ProxyConfiguration), then the remainder itself, each also
// singularised.
func nameTargets(rest string) []string {
	var targets []string
	add := func(name string) {
		for _, n := range []string{name, pluralizer.Singular(name)} {
			if n != "" && !slices.Contains(targets, n) {
				targets = append(targets, n)
			}
		}
	}
	if m := trailingToRE.FindStringSubmatch(rest); m != nil {
		add(m[1])
	}
	add(rest)
	return targets
}

// ownResource returns the first target the model has CRUD operations for, which
// makes it a resource in its own right rather than a field of something else.
func ownResource(model *SmithyModel, targets []string) string {
	for _, target := range targets {
		for _, verb := range []string{"Create", "Get", "Describe", "Delete", "List"} {
			for _, name := range []string{target, target + "s"} {
				if _, ok := model.Operation(verb + name); ok {
					return target
				}
			}
		}
	}
	return ""
}

// parentResource returns the longest known resource whose name prefixes own and
// whose read operation's request members all appear in request — that is, the
// operation addresses the parent, not a resource of its own. A parent with no
// single-read operation is matched on name alone.
func parentResource(model *SmithyModel, own string, request map[string]bool, resources map[string]string) string {
	for _, name := range prefixResources(own, resources) {
		readOne := readOneMembers(model, name)
		if len(readOne) == 0 {
			return name
		}
		covered := true
		for member := range readOne {
			if !request[member] {
				covered = false
				break
			}
		}
		if covered {
			return name
		}
	}
	return ""
}

// longestPrefixResource returns the longest known resource whose name strictly
// prefixes rest.
func longestPrefixResource(rest string, resources map[string]string) string {
	if names := prefixResources(rest, resources); len(names) > 0 {
		return names[0]
	}
	return ""
}

// prefixResources lists known resources whose names strictly prefix s, longest
// first.
func prefixResources(s string, resources map[string]string) []string {
	lowered := strings.ToLower(s)
	var names []string
	for low, name := range resources {
		if low != lowered && strings.HasPrefix(lowered, low) {
			names = append(names, name)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		if len(names[i]) != len(names[j]) {
			return len(names[i]) > len(names[j])
		}
		return names[i] < names[j]
	})
	return names
}

// resourceNamedBy returns the one known resource some identifier member names —
// FirewallArn, logGroupIdentifier — or "" when none or several do.
func resourceNamedBy(identifiers []string, resources map[string]string) string {
	found := ""
	for _, member := range identifiers {
		stem := strings.ToLower(identifierRE.ReplaceAllString(member, ""))
		name, ok := resources[stem]
		if !ok {
			continue
		}
		if found != "" && found != name {
			return ""
		}
		found = name
	}
	return found
}

// requiredOwner returns the one known resource every required request member
// identifies, by an identifier or by its bare name (s3's `Bucket`), or "". It
// tells s3's GetObjectLockConfiguration (requires Bucket) from GetObjectAcl
// (Bucket and Key), which name substrings cannot.
func requiredOwner(model *SmithyModel, opID string, resources map[string]string) string {
	op, ok := model.Operation(opID)
	if !ok || op.Input == nil {
		return ""
	}
	found := ""
	for member, ref := range model.Shapes[op.Input.Target].Members {
		if _, required := ref.Traits["smithy.api#required"]; !required {
			continue
		}
		name, ok := resources[strings.ToLower(identifierRE.ReplaceAllString(member, ""))]
		if !ok || (found != "" && found != name) {
			return ""
		}
		found = name
	}
	return found
}

// readOneMembers returns the request members of a resource's read operation,
// found by the naming codegen itself relies on.
func readOneMembers(model *SmithyModel, resource string) map[string]bool {
	for _, opID := range []string{"Get" + resource, "Describe" + resource, "Describe" + resource + "s"} {
		if _, ok := model.Operation(opID); ok {
			return requestMembers(model, opID)
		}
	}
	return nil
}

// requestMembers returns an operation's top-level request member names, less
// those that never identify anything.
func requestMembers(model *SmithyModel, opID string) map[string]bool {
	members := map[string]bool{}
	op, ok := model.Operation(opID)
	if !ok || op.Input == nil {
		return members
	}
	for name := range model.Shapes[op.Input.Target].Members {
		if !slices.Contains(nonIdentifierMembers, name) {
			members[name] = true
		}
	}
	return members
}

// identifierMembers returns the members that look like resource identifiers.
// AccountId is excluded: every account-level setting carries one.
func identifierMembers(request map[string]bool) []string {
	var out []string
	for name := range request {
		if identifierRE.MatchString(name) && !strings.EqualFold(name, "AccountId") {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
