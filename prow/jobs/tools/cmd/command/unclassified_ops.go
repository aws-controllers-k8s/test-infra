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

// Placing operations whose verb ClassifyOp does not recognise.
//
// ClassifyOp is a port of code-generator's naming rules, which only know the
// CRUD verbs, so `PutAccessPointScope`, `AcceptDelegationRequest` and
// `StartFlowCapture` all come back OpTypeUnknown. Reported as one undifferentiated
// "unclassified" list, measured across 71 controllers that was 78 operations in 21
// services, most of them either redundant or nothing ACK could ever model. The
// rules here place each one where a maintainer would look for it, or drop it, and
// were checked one by one against the real models of those 21 services:
//
//	 6  configure an existing CRD   PutAccessPointScope -> AccessPoint
//	24  act on a resource the same report already lists as new
//	15  manage a resource that has no Create verb  PutAsset, RegisterCapability
//	33  dropped: actions, queries, account-level settings, end-user operations
//	 0  left unclassified
//
// Anything the rules cannot place still lands in the unclassified section, so a
// miss is visible rather than silent.

// opPlacement is where placeUnknownOp puts an operation.
type opPlacement int

const (
	// placeUnclassified: no rule applies; report it for a human to place.
	placeUnclassified opPlacement = iota
	// placeDrop: ACK can do nothing with it. The name returned with it is the
	// reason, one of the drop* constants, and the issue lists it under a
	// collapsed "dropped" block so a wrong drop is visible without being noise.
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
	// Verbs that run something once or read data. ACK reconciles declared
	// state, and none of these has any: StartFlowCapture, TerminateSession,
	// ValidateSecurityGroupQuotasForInterface, AcquireRole, SendDelegationToken.
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
// the lowercased name of every resource the report knows about — existing CRD
// kinds and resources findNewResources reports — to its spelling, and existing
// says which of those already have a CRD.
//
// The rules run in order, and the order is load-bearing:
//
//  1. Queries and Admin* operations are dropped first: SearchAgents names the
//     new Agent resource, but a search is still not part of its lifecycle.
//  2. A name that is exactly a known resource places the operation on it —
//     AcceptDelegationRequest on the new DelegationRequest. An action verb is
//     kept only against a new resource, where Stop<Job> is often the delete
//     operation of a job resource someone is about to add; against an existing
//     CRD it is just an action (AcquireRole is not a Role operation).
//  3. Remaining action verbs are dropped.
//  4. A name with CRUD siblings of its own (GetAccessPointScope,
//     DeleteAccessPointScope) is a resource in its own right. If its name starts
//     with a known resource *and* the request carries that resource's read
//     identifiers, it configures that resource — the s3 PutBucketX pattern.
//     Requiring the identifiers is what keeps AcceptTransitGatewayClientVpnAttachment
//     off TransitGateway: the attachment is addressed by its own ID. Failing that,
//     a single resource named by a request field claims it (PutSyslogConfiguration's
//     logGroupIdentifier); with no identifier at all it is an account-level
//     setting and dropped; otherwise it is a possible new resource.
//  5. Without siblings, a single resource named by a request field claims it
//     (AssociateAvailabilityZones' FirewallArn); failing that, a name that starts
//     with a known resource is a sub-object of it. The request wins because it is
//     what the operation acts on: EnableApplicationStatusCheckSuppression takes
//     InstanceIds, so it belongs to Instance, not ApplicationStatusCheck.
//  6. No identifier in the request at all means an account-level setting.
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

	// A resource the request names beats one the operation's name starts with:
	// EnableApplicationStatusCheckSuppression takes InstanceIds, so it suppresses
	// checks on instances, whatever its name says.
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
// operation addresses the parent, not a resource of its own.
//
// A parent with no single-read operation cannot be checked that way, so its name
// is trusted: ec2's IpamRoutingPolicyRegistration is only ever listed, scoped by
// the association it belongs to, and GetIpamRoutingPolicyRegistrationDeltas is
// still its operation even though the request names only that association.
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
