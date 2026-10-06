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

// annotateFields tells a maintainer how each field candidate would be reconciled,
// which is what decides the work it takes. It runs over the merged findings of
// every producer, because the answer depends on operations other producers found:
//
//   - A new operation on the resource whose request carries the field is how the
//     field is updated, so it joins the field's evidence: networkfirewall's
//     AvailabilityZoneMappings is changed by AssociateAvailabilityZones and
//     DisassociateAvailabilityZones.
//   - A `<Field>Updates` request member is a change list an Update applies to
//     Field, as dynamodb's ReplicaUpdates is to Replicas. Field is then desired
//     state even when only returned (GlobalTableWitnesses), and the change list is
//     implementation, not something to expose (GlobalTableWitnessUpdates).
//   - A field sent with one a generator.yaml `from:` field is read from is
//     reconciled by that field's custom code: networkfirewall's
//     EnableMonitoringDashboard is sent with LoggingConfiguration on
//     UpdateLoggingConfiguration.
//   - A Spec field only a Create sends is immutable: networkfirewall's
//     TransitGatewayId, dynamodb's GlobalTableSourceArn.
//   - A Status path that is a Spec field read back inside a response structure is
//     that field's observed form, not a second addition: networkfirewall's
//     `Firewall.TransitGatewayId` is what CreateFirewall's TransitGatewayId reads
//     back as. It is folded into the Spec field as read-back evidence.
//
// Last, each field's operations are split into those that set it and those that
// return it.
func annotateFields(latest *SmithyModel, in *ControllerInputs, findings []Finding) []Finding {
	findings = foldReadBacks(latest, findings)

	newOps := map[string][]string{}
	for _, f := range findings {
		if f.Class == ClassNewOperation {
			newOps[f.Kind] = append(newOps[f.Kind], f.Subject)
		}
	}
	for i := range findings {
		f := &findings[i]
		if !isFieldCandidate(f.Class) || strings.Contains(f.Subject, ".") {
			continue
		}
		var setters []string
		for _, opID := range newOps[f.Kind] {
			if requestCarries(latest, in, opID, f.Subject) {
				setters = append(setters, opID)
			}
		}
		if len(setters) > 0 {
			f.Evidence = newEvidence(append(f.evidenceOps(), setters...))
		}
	}

	findings = pairChangeLists(latest, in, findings)
	findings = foldSecondaryViews(latest, findings)

	declared := []string{}
	if in.Config != nil {
		declared = in.Config.ResourceNames()
	}
	for i := range findings {
		f := &findings[i]
		if f.Class != ClassSpecField || f.Detail != "" {
			continue
		}
		if field, opID := customSourcedSibling(latest, in, *f); field != "" {
			f.Detail = fmt.Sprintf("sent with `%s` on `%s`, which custom code reconciles: "+
				"adding it means updating that hook, not only regenerating", field, opID)
			continue
		}
		if createOnly(latest, in, declared, newOps[f.Kind], *f) {
			f.Detail = "create-only: no operation changes it after creation, so it is immutable"
		}
	}

	for i := range findings {
		f := &findings[i]
		if f.Class == ClassSpecField && f.Detail == "" {
			f.Detail = customSetterDetail(latest, in, declared, *f)
		}
	}

	roles := newRoleIndex(latest)
	for i := range findings {
		f := &findings[i]
		if !isFieldCandidate(f.Class) {
			continue
		}
		ops := append(f.evidenceOps(), newOps[f.Kind]...)
		for op := range in.UsedOps[kindToResourceDir(f.Kind)] {
			ops = append(ops, op)
		}
		slices.Sort(ops)
		var setBy, readBy, returnedBy []string
		for _, opID := range slices.Compact(ops) {
			if _, ok := roles.find(opID, false, f.Kind, f.Subject); ok {
				setBy = append(setBy, opID)
			}
			path, ok := roles.find(opID, true, f.Kind, f.Subject)
			if !ok {
				continue
			}
			entry := opID
			if path != "" {
				entry += "=" + path
			}
			opTypes, _ := in.ClassifyOpWithOverrides(opID, declared)
			if isReadOp(opTypes, opID) {
				readBy = append(readBy, entry)
			} else {
				returnedBy = append(returnedBy, entry)
			}
		}
		f.SetBy, f.ReadBy, f.ReturnedBy = newEvidence(setBy), newEvidence(readBy), newEvidence(returnedBy)
	}
	return findings
}

// roleIndex answers where an operation's request or response carries a field,
// walking each shape once: ec2 fields are checked against every operation the
// controller calls.
type roleIndex struct {
	m     *SmithyModel
	walks map[string]map[string]MemberInfo
}

func newRoleIndex(m *SmithyModel) *roleIndex {
	return &roleIndex{m: m, walks: map[string]map[string]MemberInfo{}}
}

// find reports whether an operation's request (or response) carries field, and
// at which path when that is not field itself, unwrapped.
//
// A wrapper is how a single resource comes back — `TableDescription.VectorIndexes`,
// or `VpcEndpoints.PayerResponsibilities` from a list read — so it says nothing.
// Anywhere else the path is the answer: ModifyVpcEndpointPayerResponsibility sets a
// top-level Scope, but responses return it only as `PayerResponsibilities.Scope`,
// one entry per payer, and calling that "returned" without the path hid exactly
// why reconciling it needs custom code. So for a top-level field the wrapper must
// be the resource — named for the kind, or the response's only member — and
// PayerResponsibilities is neither. A nested field keeps any one wrapper, since its
// own path already places it. Only a top-level field is looked for deeper.
func (r *roleIndex) find(opID string, output bool, kind, field string) (string, bool) {
	key := opID + "/in"
	if output {
		key = opID + "/out"
	}
	walk, ok := r.walks[key]
	if !ok {
		walk = map[string]MemberInfo{}
		if op, found := r.m.Operation(opID); found {
			ref := op.Input
			if output {
				ref = op.Output
			}
			if ref != nil {
				walk = r.m.WalkMembers(ref.Target, maxWalkDepth)
			}
		}
		r.walks[key] = walk
	}
	nested := ""
	for p := range walk {
		wrapper, rest, wrapped := strings.Cut(p, ".")
		if p == field || (wrapped && rest == field &&
			(strings.Contains(field, ".") || r.resourceWrapper(walk, kind, wrapper))) {
			return "", true
		}
		if !strings.Contains(field, ".") && lastSegment(p) == field && (nested == "" || p < nested) {
			nested = p
		}
	}
	return nested, nested != ""
}

// resourceWrapper reports whether a top-level member of a walked shape stands for
// the resource of kind.
func (r *roleIndex) resourceWrapper(walk map[string]MemberInfo, kind, name string) bool {
	singular := strings.ToLower(pluralizer.Singular(name))
	if strings.HasPrefix(singular, strings.ToLower(kind)) {
		return true
	}
	for p := range walk {
		if !strings.Contains(p, ".") && p != name {
			return false
		}
	}
	return true
}

// foldReadBacks drops each Status finding `W.X` for which the same resource has a
// Spec finding X and W is a structure in the response — a list W would be a
// per-element observation, like dynamodb's `Replicas.GlobalTableSettingsReplicationMode`
// next to the table's own setting — and adds its operations to X's evidence.
func foldReadBacks(latest *SmithyModel, findings []Finding) []Finding {
	spec := map[[2]string]int{}
	for i, f := range findings {
		if f.Class == ClassSpecField && !strings.Contains(f.Subject, ".") {
			spec[[2]string{f.Kind, f.Subject}] = i
		}
	}
	out := findings[:0:0]
	for _, f := range findings {
		wrapper, field, nested := strings.Cut(f.Subject, ".")
		i, paired := spec[[2]string{f.Kind, field}]
		if f.Class != ClassStatusField || !nested || !paired || !responseStructure(latest, f.evidenceOps(), wrapper) {
			out = append(out, f)
			continue
		}
		findings[i].Evidence = newEvidence(append(findings[i].evidenceOps(), f.evidenceOps()...))
	}
	// Indices into findings were taken before filtering; copy the updated Spec
	// findings across.
	for k, f := range out {
		if i, ok := spec[[2]string{f.Kind, f.Subject}]; ok && f.Class == ClassSpecField {
			out[k] = findings[i]
		}
	}
	return out
}

// responseStructure reports whether some operation's response has a top-level
// member name that is a structure.
func responseStructure(m *SmithyModel, opIDs []string, name string) bool {
	for _, opID := range opIDs {
		op, ok := m.Operation(opID)
		if !ok || op.Output == nil {
			continue
		}
		if member, ok := m.Shapes[op.Output.Target].Members[name]; ok && m.Shapes[member.Target].Type == "structure" {
			return true
		}
	}
	return false
}

// isFieldCandidate reports whether a class is a field the CRD could gain.
func isFieldCandidate(c FindingClass) bool {
	return c == ClassSpecField || c == ClassStatusField || c == ClassLifecycleField
}

// pairChangeLists annotates each `<Field>Updates` lifecycle field and the Field it
// changes, and returns findings with Status candidates added for Field's
// observed-only members.
//
// Field is a collection whose entries mix desired and observed state, so a
// normalized Spec shape holds only the members some request sends, and the members
// only ever returned are Status candidates of their own. Without listing them the
// report hid real fields: dynamodb's VectorIndexes entries return IndexStatus,
// Backfilling, IndexArn, IndexSizeBytes and ItemCount, none of which any request
// carries. They were invisible before because a child of a new field is part of
// that field's finding (see hasNewAncestor).
func pairChangeLists(latest *SmithyModel, in *ControllerInputs, findings []Finding) []Finding {
	var added []Finding
	for i := range findings {
		list := &findings[i]
		base, ok := strings.CutSuffix(list.Subject, "Updates")
		if list.Class != ClassLifecycleField || !ok || base == "" || strings.Contains(list.Subject, ".") {
			continue
		}
		var updaters []string
		for _, opID := range list.evidenceOps() {
			if requestCarries(latest, in, opID, list.Subject) {
				updaters = append(updaters, opID)
			}
		}
		for j := range findings {
			field := &findings[j]
			if field.Kind != list.Kind || strings.Contains(field.Subject, ".") ||
				(field.Class != ClassSpecField && field.Class != ClassStatusField) {
				continue
			}
			if !strings.EqualFold(field.Subject, base) && !strings.EqualFold(pluralizer.Singular(field.Subject), base) {
				continue
			}
			field.Class = ClassSpecField
			field.Detail = fmt.Sprintf("custom reconciliation: %s changes it only through the change list `%s`, "+
				"so diffing and updating it needs custom code", quoteOps(updaters), list.Subject)
			ops := append(append(field.evidenceOps(), list.evidenceOps()...), updaters...)
			desired, observed := entryMembers(latest, ops, field.Subject, list.Subject)
			if len(observed) > 0 {
				field.Detail += fmt.Sprintf("; Spec needs a normalized entry shape holding %s, "+
					"with the observed-only members listed under Status", quoteOps(desired))
			}
			for _, member := range observed {
				added = append(added, Finding{
					Kind: field.Kind, Class: ClassStatusField, Subject: field.Subject + "." + member.name,
					NewSincePin: true, Evidence: newEvidence(member.ops),
					Detail: fmt.Sprintf("observed-only member of each `%s` entry: no request sends it", field.Subject),
				})
			}
			list.Detail = fmt.Sprintf("internal change list %s applies to `%s`: a reconciliation detail, "+
				"not a field to expose", quoteOps(updaters), field.Subject)
		}
	}
	return append(findings, added...)
}

// entryMember is a member of a collection's entries and the operations that return
// it.
type entryMember struct {
	name string
	ops  []string
}

// entryMembers splits the members directly under field in the responses of opIDs
// into those some request sends — under field itself or inside the change list —
// and those only ever returned.
func entryMembers(m *SmithyModel, opIDs []string, field, changeList string) (desired []string, observed []entryMember) {
	under := func(p, parent string) bool {
		prefix := p[:max(strings.LastIndex(p, "."), 0)]
		return prefix == parent || strings.HasSuffix(prefix, "."+parent)
	}
	sent := map[string]bool{}
	returned := map[string][]string{}
	for _, opID := range opIDs {
		op, ok := m.Operation(opID)
		if !ok {
			continue
		}
		if op.Input != nil {
			for p := range m.WalkMembers(op.Input.Target, maxWalkDepth) {
				if under(p, field) || strings.Contains(p, changeList+".") {
					sent[lastSegment(p)] = true
				}
			}
		}
		if op.Output != nil {
			for p := range m.WalkMembers(op.Output.Target, maxWalkDepth) {
				if name := lastSegment(p); under(p, field) && !slices.Contains(returned[name], opID) {
					returned[name] = append(returned[name], opID)
				}
			}
		}
	}
	for name, ops := range returned {
		if sent[name] {
			desired = append(desired, name)
		} else {
			observed = append(observed, entryMember{name, ops})
		}
	}
	slices.Sort(desired)
	slices.SortFunc(observed, func(a, b entryMember) int { return strings.Compare(a.name, b.name) })
	return desired, observed
}

// foldSecondaryViews reconciles Status fields that secondary reads return for one
// piece of state, matched by the singular of their last segment.
//
// ec2's Instance application health comes from two reads, and they are not one
// shape. DescribeApplicationStatus returns, per instance, the detailed
// ApplicationStatus (status, timestamps, resume time, per-check details);
// DescribeInstanceStatus returns the two-member ApplicationStatusSummary. So the
// state shape decides: where every view is the same shape they are one candidate
// with alternative sources, and otherwise each stays, named for what it holds and
// marked detailed or summary. Either way the candidate is named for the state —
// `ApplicationStatus` — not for the response wrapper `ApplicationStatuses` around
// it.
func foldSecondaryViews(latest *SmithyModel, findings []Finding) []Finding {
	stem := func(f Finding) string { return strings.ToLower(pluralizer.Singular(lastSegment(f.Subject))) }
	secondary := func(f Finding) bool {
		return f.Class == ClassStatusField && strings.HasPrefix(f.Detail, secondaryReadDetail)
	}
	groups := map[[2]string][]int{}
	for i, f := range findings {
		if secondary(f) {
			k := [2]string{f.Kind, stem(f)}
			groups[k] = append(groups[k], i)
		}
	}
	drop := map[int]bool{}
	for _, idx := range groups {
		if len(idx) < 2 {
			continue
		}
		slices.SortFunc(idx, func(a, b int) int { return strings.Compare(findings[a].Subject, findings[b].Subject) })
		views := make([]stateView, len(idx))
		same, known := true, true
		for k, i := range idx {
			views[k] = findStateView(latest, findings[i])
			same = same && views[k].shape == views[0].shape
			known = known && views[k].shape != ""
		}
		// Without the state's shape there is no telling whether the views are one
		// state or a summary beside a detail, so neither claim is made.
		if !known {
			continue
		}
		var sources []string
		for k, i := range idx {
			sources = append(sources, fmt.Sprintf("`%s` from %s", views[k].path, quoteOps(findings[i].evidenceOps())))
		}
		if same {
			keep := &findings[idx[0]]
			for _, i := range idx[1:] {
				keep.Evidence = newEvidence(append(keep.evidenceOps(), findings[i].evidenceOps()...))
				drop[i] = true
			}
			keep.Detail = secondaryReadDetail + fmt.Sprintf("; candidate field `%s`, one state with alternative "+
				"sources, %s", views[0].name, strings.Join(sources, " or "))
			continue
		}
		largest := 0
		for k := range views {
			if views[k].members > views[largest].members {
				largest = k
			}
		}
		for k, i := range idx {
			role := "summary"
			if k == largest {
				role = "detailed"
			}
			var others []string
			for o := range views {
				if o != k {
					others = append(others, fmt.Sprintf("`%s` (%s)",
						views[o].shape, plural(views[o].members, "member", "members")))
				}
			}
			findings[i].Detail = secondaryReadDetail + fmt.Sprintf("; candidate field `%s`, the %s view: "+
				"`%s` (%s) at `%s`, beside %s from another read; list them as summary and detailed "+
				"status, or normalize them into one summary on purpose",
				views[k].shape, role, views[k].shape, plural(views[k].members, "member", "members"),
				views[k].path, strings.Join(others, ", "))
		}
	}
	out := findings[:0:0]
	for i, f := range findings {
		if !drop[i] {
			out = append(out, f)
		}
	}
	return out
}

// stateView is where a secondary read holds a piece of state.
type stateView struct {
	// path is the deepest member under the finding named for the state:
	// `ApplicationStatuses.Instances.ApplicationStatus` for `ApplicationStatuses`.
	path, name string
	// shape is that member's structure, resolved through lists, and members its
	// member count; "" and 0 when the model does not say.
	shape   string
	members int
}

func findStateView(m *SmithyModel, f Finding) stateView {
	want := strings.ToLower(pluralizer.Singular(lastSegment(f.Subject)))
	view := stateView{path: f.Subject, name: pluralizer.Singular(lastSegment(f.Subject))}
	for _, opID := range f.evidenceOps() {
		op, ok := m.Operation(opID)
		if !ok || op.Output == nil {
			continue
		}
		for p, info := range m.WalkMembers(op.Output.Target, maxWalkDepth) {
			if p != f.Subject && !strings.HasPrefix(p, f.Subject+".") {
				continue
			}
			if strings.ToLower(pluralizer.Singular(lastSegment(p))) != want {
				continue
			}
			shape := elementShape(m, info.Target)
			if strings.Count(p, ".") < strings.Count(view.path, ".") || m.Shapes[shape].Type != "structure" {
				continue
			}
			view.path, view.name = p, lastSegment(p)
			view.shape, view.members = shape[strings.LastIndex(shape, "#")+1:], len(m.Shapes[shape].Members)
		}
	}
	return view
}

// customSetterDetail says when a Spec field is changed by something other than the
// resource's own Update, which codegen's update path does not call.
// networkfirewall's AvailabilityZoneMappings is changed by
// AssociateAvailabilityZones and DisassociateAvailabilityZones, so the hook must
// diff the list; its ProxySettings by UpdateProxySettings alone, so it needs a
// dedicated update hook. A field only a Create sends gets nothing here, nor does
// one the resource's own Update also sends — unless that Update is hand-written
// (update_operation.custom_method_name), when codegen does not send the field and
// the custom method must.
func customSetterDetail(m *SmithyModel, in *ControllerInputs, declared []string, f Finding) string {
	custom := customUpdateMethod(in, f.Kind)
	var setters []string
	for _, opID := range f.evidenceOps() {
		if !requestCarries(m, in, opID, f.Subject) {
			continue
		}
		switch opTypes, name := in.ClassifyOpWithOverrides(opID, declared); {
		case opTypes.Has(OpTypeUpdate) && strings.EqualFold(name, f.Kind):
			if custom != "" {
				return fmt.Sprintf("custom reconciliation: sent on `%s`, the resource's own Update, but that "+
					"update is the hand-written `%s`, so adding it means changing that code, not only regenerating",
					opID, custom)
			}
			return ""
		case opTypes.Has(OpTypeCreate, OpTypeCreateBatch):
		default:
			setters = append(setters, opID)
		}
	}
	if len(setters) == 0 {
		return ""
	}
	// Setters the controller already calls from its own update code: that code is
	// what changes, not a new hook.
	if custom != "" &&
		!slices.ContainsFunc(setters, func(op string) bool { return !in.UsedOps[kindToResourceDir(f.Kind)][op] }) {
		return fmt.Sprintf("custom reconciliation: set through %s, which the hand-written update `%s` calls, "+
			"so adding it means changing that code, not only regenerating", quoteOps(setters), custom)
	}
	pair := slices.ContainsFunc(setters, func(op string) bool { return strings.HasPrefix(op, "Associate") }) &&
		slices.ContainsFunc(setters, func(op string) bool { return strings.HasPrefix(op, "Disassociate") })
	if pair {
		return fmt.Sprintf("custom reconciliation: changed through %s, not the resource's own Update, "+
			"so an update hook must diff the list and call them", quoteOps(setters))
	}
	return fmt.Sprintf("custom reconciliation: changed only through %s, not the resource's own Update, "+
		"so it needs a dedicated update hook", quoteOps(setters))
}

// customUpdateMethod returns the resource's update_operation.custom_method_name,
// or "" when codegen generates its update.
func customUpdateMethod(in *ControllerInputs, kind string) string {
	res, _ := in.Config.resource(kind)
	return res.UpdateOperation.CustomMethodName
}

// customSourcedSibling returns the generator.yaml `from:` field f is sent beside,
// and the operation both are sent on, or "" when there is none.
func customSourcedSibling(latest *SmithyModel, in *ControllerInputs, f Finding) (string, string) {
	res, ok := in.Config.resource(f.Kind)
	if !ok {
		return "", ""
	}
	names := make([]string, 0, len(res.Fields))
	for name := range res.Fields {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		field := res.Fields[name]
		if field.From == nil || !slices.Contains(f.evidenceOps(), field.From.Operation) {
			continue
		}
		if requestCarries(latest, in, field.From.Operation, f.Subject) {
			return name, field.From.Operation
		}
	}
	return "", ""
}

// createOnly reports whether a Create sends f and nothing else does.
//
// Every operation of the resource is checked, not only the ones f's Evidence names,
// and by f's last path segment, because the same field reached another way is
// another path: lambda's `Code.S3ObjectStorageMode` is sent again as bare
// `S3ObjectStorageMode` on UpdateFunctionCode, so it is not immutable.
func createOnly(latest *SmithyModel, in *ControllerInputs, declared []string, newOps []string, f Finding) bool {
	created := false
	ops := append([]string{}, newOps...)
	for op := range in.UsedOps[kindToResourceDir(f.Kind)] {
		ops = append(ops, op)
	}
	for _, opID := range append(ops, f.evidenceOps()...) {
		if !requestCarriesName(latest, opID, lastSegment(f.Subject)) {
			continue
		}
		// Every role counts: an operation_type: [Create, Update] operation is also
		// how the field changes.
		if opTypes, _ := in.ClassifyOpWithOverrides(opID, declared); !opTypes.Only(OpTypeCreate, OpTypeCreateBatch) {
			return false
		}
		created = true
	}
	return created
}

// requestCarriesName reports whether an operation's request has a member whose own
// name is name, at any depth.
func requestCarriesName(m *SmithyModel, opID, name string) bool {
	op, ok := m.Operation(opID)
	if !ok || op.Input == nil {
		return false
	}
	for path := range m.WalkMembers(op.Input.Target, maxWalkDepth) {
		if lastSegment(path) == name {
			return true
		}
	}
	return false
}

// lastSegment returns the final segment of a member path.
func lastSegment(path string) string {
	return path[strings.LastIndex(path, ".")+1:]
}

// requestCarries reports whether an operation's request has a member at path, as
// spelled or, as findAddedFields keys it, relative to the operation's configured
// input wrapper.
func requestCarries(m *SmithyModel, in *ControllerInputs, opID, path string) bool {
	op, ok := m.Operation(opID)
	if !ok || op.Input == nil {
		return false
	}
	members := m.WalkMembers(op.Input.Target, maxWalkDepth)
	if _, ok := members[path]; ok {
		return true
	}
	if in == nil {
		return false
	}
	wrapper := in.Config.inputWrapper(opID)
	if wrapper == "" {
		return false
	}
	_, ok = members[wrapper+"."+path]
	return ok
}

// annotateResources says when a new resource changes through operations codegen
// does not wire into its update. networkfirewall's ProxyRuleGroup has no Update of
// its own: CreateProxyRules, DeleteProxyRules, UpdateProxyRule and
// UpdateProxyRulePriorities are how it changes, so its rules need a custom diff and
// update hooks. ProxyConfiguration does have UpdateProxyConfiguration, but its
// rule-group attachments and priorities change only through operations beside it.
// An operation is the resource's own when it classifies to the resource itself;
// reads are left out, since they change nothing.
//
// Where an operation takes an update token, the note names the read it comes
// from: the read whose request the operation's request covers most fully, so
// UpdateProxyRule takes DescribeProxyRule's token and UpdateProxyRulePriorities,
// which names no single rule, DescribeProxyRuleGroup's.
func annotateResources(latest *SmithyModel, in *ControllerInputs, findings []Finding) []Finding {
	declared := []string{}
	if in.Config != nil {
		declared = in.Config.ResourceNames()
	}
	for i := range findings {
		f := &findings[i]
		if f.Class != ClassNewResource {
			continue
		}
		var parts, tokenReads []string
		ownUpdate := false
		for _, opID := range f.evidenceOps() {
			opTypes, name := in.ClassifyOpWithOverrides(opID, declared)
			if isReadOp(opTypes, opID) {
				// Not requestMembers on the response side: UpdateToken is a
				// non-identifier it strips. The same holds for requests below.
				if op, ok := latest.Operation(opID); ok && op.Output != nil {
					if _, ok := latest.Shapes[op.Output.Target].Members["UpdateToken"]; ok {
						tokenReads = append(tokenReads, opID)
					}
				}
				continue
			}
			if strings.EqualFold(name, f.Kind) {
				ownUpdate = ownUpdate || opTypes.Has(OpTypeUpdate)
				continue
			}
			parts = append(parts, opID)
		}
		if len(parts) == 0 {
			continue
		}
		change, are, them := "change", "are", "them"
		if len(parts) == 1 {
			change, are, them = "changes", "is", "it"
		}
		if ownUpdate {
			f.Detail += fmt.Sprintf("; %s %s parts of it its own Update does not, and codegen does not "+
				"wire %s in, so those parts need a custom diff and update hooks", quoteOps(parts), change, them)
		} else {
			f.Detail += fmt.Sprintf("; it has no Update of its own: %s %s how it changes, and codegen does "+
				"not wire %s in, so it needs a custom diff and update hooks", quoteOps(parts), are, them)
		}
		var sources []string
		for _, opID := range parts {
			if !requestCarries(latest, in, opID, "UpdateToken") {
				continue
			}
			if read := tokenSource(latest, opID, tokenReads); read != "" {
				sources = append(sources, fmt.Sprintf("`%s` takes the update token `%s` returns", opID, read))
			} else {
				sources = append(sources, fmt.Sprintf("`%s` takes an update token", opID))
			}
		}
		if len(sources) > 0 {
			f.Detail += "; " + strings.Join(sources, ", ")
		}
	}
	return findings
}

// tokenSource returns the read among reads whose request members the operation's
// request carries all of, preferring the one that needs the most — the most
// specific object both name.
func tokenSource(m *SmithyModel, opID string, reads []string) string {
	request := requestMembers(m, opID)
	best, bestSize := "", 0
	for _, read := range reads {
		needs := requestMembers(m, read)
		if len(needs) <= bestSize {
			continue
		}
		covered := true
		for member := range needs {
			if !request[member] {
				covered = false
				break
			}
		}
		if covered {
			best, bestSize = read, len(needs)
		}
	}
	return best
}
