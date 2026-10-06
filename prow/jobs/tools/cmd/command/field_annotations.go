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

// annotateFields notes how each field candidate would be reconciled. It runs over
// every producer's merged findings, because the answer depends on their operations:
//
//   - A new operation whose request carries the field joins its evidence.
//   - A `<Field>Updates` request member is a change list applied to Field (dynamodb
//     ReplicaUpdates to Replicas): Field is desired state, the list is not exposed.
//   - A field sent beside a generator.yaml `from:` field is reconciled by that
//     field's custom code.
//   - A Spec field only a Create sends is immutable.
//   - A Status `W.X` reading back Spec field X is folded into X.
//
// Last, each field's operations are split into those that set and return it.
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
			f.Work = workFromSibling
			continue
		}
		if createOnly(latest, in, declared, newOps[f.Kind], *f) {
			f.Detail, f.Work = createOnlyDetail, workCreateOnly
		}
	}

	for i := range findings {
		f := &findings[i]
		if f.Class == ClassSpecField && f.Detail == "" {
			f.Detail, f.Work = customSetterDetail(latest, in, declared, *f)
		}
	}

	roles := newRoleIndex(latest)
	for i := range findings {
		f := &findings[i]
		if !isFieldCandidate(f.Class) {
			continue
		}
		ops := append(append(f.evidenceOps(), newOps[f.Kind]...), in.calledOps(f.Kind)...)
		slices.Sort(ops)
		var setBy, readBy, returnedBy []string
		for _, opID := range slices.Compact(ops) {
			sdkPaths := f.sdkPaths(opID)
			opTypes, _ := in.ClassifyOpWithOverrides(opID, declared)
			_, set := roles.find(opID, false, f.Kind, f.Subject)
			if !set {
				_, set = roles.findSDKPath(opID, false, sdkPaths)
			}
			if set && canSet(opTypes, opID) {
				setBy = append(setBy, opID)
			}
			path, ok := roles.find(opID, true, f.Kind, f.Subject)
			if !ok {
				path, ok = roles.findSDKPath(opID, true, sdkPaths)
			}
			if !ok {
				continue
			}
			entry := opID
			if path != "" {
				entry += "=" + path
			}
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
// caching each shape walk since every field is checked against every used operation.
type roleIndex struct {
	m     *SmithyModel
	walks map[string]map[string]MemberInfo
}

func newRoleIndex(m *SmithyModel) *roleIndex {
	return &roleIndex{m: m, walks: map[string]map[string]MemberInfo{}}
}

// find reports whether an operation's request (or response) carries field, and
// the path when it is found other than directly or under a resource wrapper.
//
// A top-level field may sit under one wrapper only if the wrapper is the resource
// (named for the kind, or the only member), as in `TableDescription.VectorIndexes`.
// Elsewhere the path matters: Scope returned only as `PayerResponsibilities.Scope`
// is per entry, which is why reconciling it needs custom code.
func (r *roleIndex) find(opID string, output bool, kind, field string) (string, bool) {
	walk := r.walk(opID, output)
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

// findSDKPath reports the first of a field's recorded SDK paths (Finding.SDKPaths)
// an operation's request (or response) carries, for fields find cannot locate by
// their renamed Subject.
func (r *roleIndex) findSDKPath(opID string, output bool, paths []string) (string, bool) {
	walk := r.walk(opID, output)
	for _, path := range paths {
		if _, ok := walk[path]; ok {
			return path, true
		}
	}
	return "", false
}

// walk returns, cached, the members of an operation's request or response.
func (r *roleIndex) walk(opID string, output bool) map[string]MemberInfo {
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
	return walk
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

// foldReadBacks folds each Status finding X, or `W.X` when W is a response
// structure, into the resource's Spec finding X: a read returning a field another
// producer reports as Spec. A list W is a per-element observation and stays.
func foldReadBacks(latest *SmithyModel, findings []Finding) []Finding {
	spec := map[[2]string]int{}
	for i, f := range findings {
		if f.Class == ClassSpecField {
			spec[[2]string{f.Kind, f.Subject}] = i
		}
	}
	out := findings[:0:0]
	for _, f := range findings {
		i, paired := spec[[2]string{f.Kind, f.Subject}]
		if wrapper, field, nested := strings.Cut(f.Subject, "."); !paired && nested {
			i, paired = spec[[2]string{f.Kind, field}]
			paired = paired && !strings.Contains(field, ".") && responseStructure(latest, f.evidenceOps(), wrapper)
		}
		if f.Class != ClassStatusField || !paired {
			out = append(out, f)
			continue
		}
		findings[i].Evidence = newEvidence(append(findings[i].evidenceOps(), f.evidenceOps()...))
		findings[i].SDKPaths = joinSDKPaths(findings[i].SDKPaths, f.SDKPaths)
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
// changes, and adds Status candidates for Field's observed-only entry members.
//
// Field's entries mix desired and observed state, so its Spec shape holds only the
// members a request sends. The returned-only members (dynamodb VectorIndexes'
// IndexStatus) would otherwise be hidden inside Field's finding; see hasNewAncestor.
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
			field.Class, field.Work = ClassSpecField, workChangeList
			field.Detail = fmt.Sprintf("custom reconciliation: %s changes it only through the change list `%s`, "+
				"so diffing and updating it needs custom code", quoteOps(updaters), list.Subject)
			ops := append(append(field.evidenceOps(), list.evidenceOps()...), updaters...)
			desired, observed := entryMembers(latest, in.Config, ops, field.Subject, list.Subject)
			if len(observed) > 0 {
				field.Detail += fmt.Sprintf("; Spec needs a normalized entry shape holding %s, "+
					"with the observed-only members as their own Status candidates", quoteOps(desired))
			}
			for _, member := range observed {
				added = append(added, Finding{
					Kind: field.Kind, Class: ClassStatusField, Subject: field.Subject + "." + member.name,
					NewSincePin: true, Evidence: newEvidence(member.ops), Work: workChangeList,
					Detail: fmt.Sprintf("observed-only member of each `%s` entry: no request sends it, "+
						"so it needs a Status entry shape beside Spec's normalized one", field.Subject),
				})
			}
			list.Detail = fmt.Sprintf("internal change list %s applies to `%s`: not a field to expose; "+
				"the custom update for `%s` builds it", quoteOps(updaters), field.Subject, field.Subject)
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
// and those only ever returned. Returned members generator.yaml ignores are left out.
func entryMembers(
	m *SmithyModel, cfg *generatorConfig, opIDs []string, field, changeList string,
) (desired []string, observed []entryMember) {
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
			members := m.WalkMembers(op.Output.Target, maxWalkDepth)
			for p := range members {
				if cfg.ignoresMember(m, opID, op.Output, true, p, members) {
					continue
				}
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
// Views of the same shape merge into one candidate with alternative sources.
// Otherwise each stays, marked detailed or summary: ec2's DescribeApplicationStatus
// and DescribeInstanceStatus return differently shaped application status. The
// candidate is named for the state, not the response wrapper.
func foldSecondaryViews(latest *SmithyModel, findings []Finding) []Finding {
	stem := func(f Finding) string { return strings.ToLower(pluralizer.Singular(lastSegment(f.Subject))) }
	secondary := func(f Finding) bool {
		return f.Class == ClassStatusField && f.Work == workSecondaryRead
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

// canSet reports whether an operation's request can change a field: a read's
// request only filters or identifies, and so does a Delete's.
func canSet(opTypes OpTypes, opID string) bool {
	return !isReadOp(opTypes, opID) && !opTypes.Only(OpTypeDelete)
}

// customSetterDetail notes when a Spec field is changed by an operation other than
// the resource's own Update, which codegen does not call, and the work that means.
// It returns "" for fields
// only a Create sends, or that the generated Update sends; a hand-written Update
// (update_operation.custom_method_name) must send the field itself.
func customSetterDetail(m *SmithyModel, in *ControllerInputs, declared []string, f Finding) (string, fieldWork) {
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
					opID, custom), workCustomUpdate
			}
			return "", workNone
		case opTypes.Has(OpTypeCreate, OpTypeCreateBatch):
		default:
			if canSet(opTypes, opID) {
				setters = append(setters, opID)
			}
		}
	}
	if len(setters) == 0 {
		return "", workNone
	}
	// Setters the controller already calls from its own update code: that code is
	// what changes, not a new hook.
	if custom != "" &&
		!slices.ContainsFunc(setters, func(op string) bool { return !in.UsedOps[kindToResourceDir(f.Kind)][op] }) {
		return fmt.Sprintf("custom reconciliation: set through %s, which the hand-written update `%s` calls, "+
			"so adding it means changing that code, not only regenerating", quoteOps(setters), custom), workCustomUpdate
	}
	pair := slices.ContainsFunc(setters, func(op string) bool { return strings.HasPrefix(op, "Associate") }) &&
		slices.ContainsFunc(setters, func(op string) bool { return strings.HasPrefix(op, "Disassociate") })
	if pair {
		return fmt.Sprintf("custom reconciliation: changed through %s, not the resource's own Update, "+
			"so an update hook must diff the list and call them", quoteOps(setters)), workListDiff
	}
	return fmt.Sprintf("custom reconciliation: changed only through %s, not the resource's own Update, "+
		"so it needs a dedicated update hook", quoteOps(setters)), workDedicatedSetter
}

// createOnlyDetail is the work a field only a Create sends needs.
const createOnlyDetail = "set only on Create; add `is_immutable: true`"

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
// It checks every operation of the resource by f's last segment, since an update
// may send the field at another path (lambda's `Code.S3ObjectStorageMode` is bare
// `S3ObjectStorageMode` on UpdateFunctionCode).
func createOnly(latest *SmithyModel, in *ControllerInputs, declared []string, newOps []string, f Finding) bool {
	created := false
	ops := append(append(slices.Clone(newOps), in.calledOps(f.Kind)...), f.evidenceOps()...)
	for _, opID := range ops {
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
	wrapper := inputWrapper(m, in, opID, op.Input)
	if wrapper == "" {
		return false
	}
	_, ok = members[wrapper+"."+path]
	return ok
}

// annotateResources notes when a new resource changes through non-read operations
// that classify to another resource, which codegen does not wire into its update
// (networkfirewall's ProxyRuleGroup changes only through CreateProxyRules and
// similar). Where such an operation takes an update token, the note names the read
// that returns it; see tokenSource.
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

// tokenSource returns the read whose request members the operation's request all
// carries, preferring the read that needs the most members (the most specific).
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
