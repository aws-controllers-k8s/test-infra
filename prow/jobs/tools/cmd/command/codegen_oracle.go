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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"sort"
	"strings"

	"github.com/aquasecurity/go-version/pkg/semver"
	awssdkmodel "github.com/aws-controllers-k8s/code-generator/pkg/api"
	ackgenconfig "github.com/aws-controllers-k8s/code-generator/pkg/config"
	ackmodel "github.com/aws-controllers-k8s/code-generator/pkg/model"
	acksdk "github.com/aws-controllers-k8s/code-generator/pkg/sdk"
	"github.com/aws-controllers-k8s/pkg/names"
	"github.com/google/go-github/v63/github"
)

// codeGeneratorModule is the module the oracle runs, pinned in go.mod.
const codeGeneratorModule = "github.com/aws-controllers-k8s/code-generator"

// codeGeneratorVersion is the code-generator version built into this binary, or
// "unknown" when the build carries no module information.
var codeGeneratorVersion = func() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, dep := range info.Deps {
		if dep.Path != codeGeneratorModule {
			continue
		}
		if dep.Replace != nil && dep.Replace.Version != "" {
			return dep.Replace.Version
		}
		return dep.Version
	}
	return "unknown"
}()

// codegenCRD is one CRD as code-generator generates it from a model and the
// controller's generator.yaml.
type codegenCRD struct {
	// Kind is the resource name as the operations spell it (VpcEndpoint).
	Kind string
	// Spec and Status map each field path, keyed by codegenKey, to its spelling
	// in SDK member names (renamed where generator.yaml renames).
	Spec, Status map[string]string
	// Ops are the operations codegen maps onto the resource (crd.Ops), sorted.
	Ops []string
	// Create is the Create operation; Deletable is whether a Delete is mapped.
	Create    string
	Deletable bool
}

// codegenCRDs are one model's CRDs, keyed by lowercased Kind.
type codegenCRDs map[string]*codegenCRD

// codegenView is code-generator's CRDs for the generation pin and the latest
// model, both from the controller's current generator.yaml. One code-generator
// version builds both, so a change in its semantics cancels out of the diff.
type codegenView struct {
	pin, latest codegenCRDs
}

// codegenKey normalizes a field path for comparison: lowercased, as
// readCRDFields does, and without underscores, which codegen adds to Go
// keywords (`type_`) and strcase removes from member names.
func codegenKey(path string) string {
	return strings.ToLower(strings.ReplaceAll(path, "_", ""))
}

// codegenDefaultConfig is ack-generate's DefaultConfig (pkg/generate/ack), copied
// so the template packages stay out of this binary. IncludeACKMetadata keeps the
// primary ARN out of Status, as ack-generate does.
func codegenDefaultConfig() ackgenconfig.Config {
	return ackgenconfig.Config{
		PrefixConfig: ackgenconfig.PrefixConfig{
			SpecField:   ".Spec",
			StatusField: ".Status",
		},
		IncludeACKMetadata:             true,
		SetManyOutputNotFoundErrReturn: "return nil, ackerr.NotFound",
	}
}

// runCodegenOracle generates the controller's CRDs from the pin and latest
// models. Any failure is the service's analysis failure: guessing without the
// oracle would put fields under "Regenerate is enough" that regeneration lacks.
func runCodegenOracle(in *ControllerInputs, pin, latest *SmithyModel) (*codegenView, error) {
	dir, err := os.MkdirTemp("", "ack-codegen-oracle-")
	if err != nil {
		return nil, fmt.Errorf("unable to create the code-generator model dir: %s", err)
	}
	defer os.RemoveAll(dir)

	view := &codegenView{}
	for _, side := range []struct {
		name  string
		model *SmithyModel
		out   *codegenCRDs
	}{{"pin", pin, &view.pin}, {"latest", latest, &view.latest}} {
		crds, err := generateCRDs(filepath.Join(dir, side.name), in, side.model)
		if err != nil {
			return nil, fmt.Errorf("code-generator %s could not generate %s's CRDs from the %s model: %w",
				codeGeneratorVersion, in.Service, side.name, err)
		}
		*side.out = crds
	}
	return view, nil
}

// generateCRDs writes model where code-generator looks for it under sdkDir and
// returns the CRDs it generates. A panic in code-generator is returned as an
// error.
func generateCRDs(sdkDir string, in *ControllerInputs, model *SmithyModel) (crds codegenCRDs, err error) {
	defer func() {
		if r := recover(); r != nil {
			crds, err = nil, fmt.Errorf("panic: %v", r)
		}
	}()
	if len(model.raw) == 0 {
		return nil, fmt.Errorf("no model document to load")
	}
	// filepath.Base as in fetchModel: model_name comes from generator.yaml.
	modelName := filepath.Base(in.ModelName)
	modelDir := filepath.Join(sdkDir, "codegen", "sdk-codegen", "aws-models")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(modelDir, modelName+".json"), model.raw, 0o644); err != nil {
		return nil, err
	}

	// Strict, as ack-generate loads it: a key this code-generator does not know
	// fails here rather than generating something the controller would not.
	cfg, err := ackgenconfig.New(in.GeneratorConfigPath, codegenDefaultConfig())
	if err != nil {
		return nil, fmt.Errorf("generator.yaml: %w", err)
	}
	api, err := acksdk.NewHelper(sdkDir, cfg).API(modelName)
	if err != nil {
		return nil, err
	}
	m, err := ackmodel.New(api, in.Service, "v1alpha1", cfg, ackgenconfig.DocumentationConfig{})
	if err != nil {
		return nil, err
	}
	generated, err := m.GetCRDs()
	if err != nil {
		return nil, err
	}

	crds = codegenCRDs{}
	for _, crd := range generated {
		c := &codegenCRD{
			Kind:   crd.Names.Original,
			Spec:   codegenFieldPaths(crd.SpecFields),
			Status: codegenFieldPaths(crd.StatusFields),
		}
		for _, op := range crd.Ops.IterOps() {
			c.Ops = append(c.Ops, op.Name)
		}
		slices.Sort(c.Ops)
		c.Ops = slices.Compact(c.Ops)
		if crd.Ops.Create != nil {
			c.Create = crd.Ops.Create.Name
		}
		c.Deletable = crd.Ops.Delete != nil
		crds[strings.ToLower(c.Kind)] = c
	}
	return crds, nil
}

// codegenFieldPaths returns every field path under a CRD's Spec or Status fields,
// keyed by codegenKey. Reference fields (`<Field>Ref`) are codegen's own, not the
// API's, so they are left out.
func codegenFieldPaths(fields map[string]*ackmodel.Field) map[string]string {
	out := map[string]string{}
	for _, f := range fields {
		if f.IsReference() {
			continue
		}
		addCodegenPaths(out, "", "", f.Names.Camel, f.Names.Original, f.ShapeRef, map[string]bool{}, 1)
	}
	return out
}

// addCodegenPaths records a field and, through its shape, every member below
// it. Lists and maps are transparent, as in WalkMembers and readCRDFields.
// Members take codegen's Camel name for the key and the SDK name for the path.
func addCodegenPaths(
	out map[string]string,
	keyPrefix, pathPrefix, camel, original string,
	ref *awssdkmodel.ShapeRef,
	onBranch map[string]bool,
	depth int,
) {
	key, path := codegenKey(camel), original
	if keyPrefix != "" {
		key, path = keyPrefix+"."+key, pathPrefix+"."+original
	}
	out[key] = path
	if ref == nil || depth >= maxWalkDepth {
		return
	}
	shape := ref.Shape
	for shape != nil && (shape.Type == "list" || shape.Type == "map") {
		if shape.Type == "list" {
			shape = shape.MemberRef.Shape
		} else {
			shape = shape.ValueRef.Shape
		}
	}
	if shape == nil || shape.Type != "structure" || onBranch[shape.ShapeName] {
		return
	}
	onBranch[shape.ShapeName] = true
	defer delete(onBranch, shape.ShapeName)
	for name, member := range shape.MemberRefs {
		addCodegenPaths(out, key, path, names.New(name).Camel, name, member, onBranch, depth+1)
	}
}

// gained returns the paths the latest CRD has on one side that the pin's lacks.
func gained(pin, latest *codegenCRD, side func(*codegenCRD) map[string]string) map[string]string {
	out := map[string]string{}
	if latest == nil {
		return out
	}
	var before map[string]string
	if pin != nil {
		before = side(pin)
	}
	for key, path := range side(latest) {
		if _, ok := before[key]; !ok {
			out[key] = path
		}
	}
	return out
}

func specSide(c *codegenCRD) map[string]string   { return c.Spec }
func statusSide(c *codegenCRD) map[string]string { return c.Status }

// covers reports whether two field keys are one finding: equal, or one inside
// the other, since a child of a new field belongs to that field.
func covers(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+".") || strings.HasPrefix(b, a+".")
}

// reconcileWithCodegen makes code-generator the authority on what regeneration
// adds:
//
//   - A new Spec or Status field left needing no work stays under "Regenerate is
//     enough" only if the latest CRD gains it (Spec for Spec; either side for
//     Status, as exposedAt); otherwise it gets the work it needs.
//   - A field the latest CRD gains that no finding mentions is added.
//   - A new resource codegen generates no CRD for is dropped; one it newly
//     generates that no finding names is added.
//
// Findings that already need work keep their reason.
func reconcileWithCodegen(
	latestModel *SmithyModel, findings []Finding, in *ControllerInputs, view *codegenView,
) []Finding {
	declared := []string{}
	if in.Config != nil {
		declared = in.Config.ResourceNames()
	}

	mentioned := map[string][]string{}
	resources := map[string]bool{}
	// Top-level Spec findings, which foldReadBacks may have folded a Status
	// `W.X` into.
	spec := map[string]map[string]Finding{}
	for _, f := range findings {
		if f.Kind == "" {
			continue
		}
		low := strings.ToLower(f.Kind)
		mentioned[low] = append(mentioned[low], codegenKey(f.Subject))
		resources[low] = resources[low] || isResourceClass(f.Class)
		if f.Class == ClassSpecField && !strings.Contains(f.Subject, ".") {
			if spec[low] == nil {
				spec[low] = map[string]Finding{}
			}
			spec[low][codegenKey(f.Subject)] = f
		}
	}
	// reported is whether a finding mentions a field path, or folded it in.
	reported := func(low, key, path string) bool {
		if slices.ContainsFunc(mentioned[low], func(m string) bool { return covers(m, key) }) {
			return true
		}
		wrapper, field, nested := strings.Cut(path, ".")
		if !nested || strings.Contains(field, ".") {
			return false
		}
		f, ok := spec[low][codegenKey(field)]
		return ok && responseStructure(latestModel, f.evidenceOps(), wrapper)
	}

	out := make([]Finding, 0, len(findings))
	for _, f := range findings {
		low := strings.ToLower(f.Kind)
		pin, latest := view.pin[low], view.latest[low]
		switch {
		case f.Class == ClassNewResource || f.Class == ClassTransientResource:
			if latest == nil {
				out = append(out, Finding{
					Class:       ClassDroppedOperation,
					Subject:     createOpOf(in, declared, f),
					NewSincePin: f.NewSincePin,
					Detail: fmt.Sprintf("implies `%s`, but code-generator generates no CRD for it "+
						"from generator.yaml", f.Kind),
				})
				continue
			}
			f.NewSincePin = pin == nil
		case f.NewSincePin && f.Work == workNone && (f.Class == ClassSpecField || f.Class == ClassStatusField):
			key := codegenKey(f.Subject)
			_, inSpec := gained(pin, latest, specSide)[key]
			_, inStatus := gained(pin, latest, statusSide)[key]
			if !inSpec && !(f.Class == ClassStatusField && inStatus) {
				f.Detail, f.Work = notGeneratedDetail(f, latest), workNotGenerated
			}
		}
		out = append(out, f)
	}

	kinds := make([]string, 0, len(view.latest))
	for low := range view.latest {
		kinds = append(kinds, low)
	}
	sort.Strings(kinds)
	for _, low := range kinds {
		latest, pin := view.latest[low], view.pin[low]
		canonical, hasCRD := in.CanonicalKind(latest.Kind)
		switch {
		case pin == nil && !hasCRD && !resources[low]:
			f := Finding{
				Kind: latest.Kind, Class: ClassNewResource, Subject: latest.Kind, NewSincePin: true,
				Detail: "code-generator generates a CRD for it", Evidence: newEvidence(latest.Ops),
			}
			if !latest.Deletable {
				f.Class = ClassTransientResource
				f.Detail = "code-generator generates a CRD for it, but nothing deletes it: AWS expires or consumes it"
			}
			out = append(out, f)
		case pin != nil && hasCRD:
			for _, side := range []struct {
				class FindingClass
				paths map[string]string
			}{
				{ClassSpecField, gained(pin, latest, specSide)},
				{ClassStatusField, gained(pin, latest, statusSide)},
			} {
				out = append(out, unreportedFields(in, canonical, side.class, side.paths,
					func(key, path string) bool { return reported(low, key, path) })...)
			}
		}
	}
	return out
}

// unreportedFields returns, as findings, the top-level paths among gained that
// are not reported and the CRD does not already expose.
func unreportedFields(
	in *ControllerInputs, kind string, class FindingClass, gained map[string]string,
	reported func(key, path string) bool,
) []Finding {
	keys := make([]string, 0, len(gained))
	for key := range gained {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out []Finding
	for _, key := range keys {
		top := !slices.ContainsFunc(keys, func(other string) bool { return strings.HasPrefix(key, other+".") })
		if !top || exposedAt(in, kind, key, true) || reported(key, gained[key]) {
			continue
		}
		out = append(out, Finding{
			Kind: kind, Class: class, Subject: gained[key], NewSincePin: true, Evidence: codegenEvidence,
		})
	}
	return out
}

// codegenEvidence is the Evidence of a field only code-generator reports.
const codegenEvidence = "code-generator"

// createOpOf returns the Create operation a resource finding is implied by, or
// its kind when none of its operations is one.
func createOpOf(in *ControllerInputs, declared []string, f Finding) string {
	for _, opID := range f.evidenceOps() {
		if opTypes, name := in.ClassifyOpWithOverrides(opID, declared); opTypes.Has(OpTypeCreate) &&
			strings.EqualFold(name, f.Kind) {
			return opID
		}
	}
	return f.Kind
}

// notGeneratedDetail says why regeneration does not add a field the API added,
// by where its evidence operations stand in codegen's operation map.
func notGeneratedDetail(f Finding, crd *codegenCRD) string {
	if crd == nil {
		return fmt.Sprintf("regeneration does not add it: code-generator generates no `%s` CRD "+
			"from generator.yaml", f.Kind)
	}
	// A response member below a field Spec already has: the field takes the
	// Create request's shape (dynamodb's ReplicationGroup), so Status never
	// gets the response's.
	if head, _, nested := strings.Cut(f.Subject, "."); nested && f.Class == ClassStatusField {
		if _, inSpec := crd.Spec[codegenKey(head)]; inSpec {
			return fmt.Sprintf("regeneration does not add it: Spec's `%s` has the Create request's shape, "+
				"which lacks it, so it needs its own Status field with `from:` or a hook", head)
		}
	}
	ops := f.evidenceOps()
	section, request, carries, verb := "Status", "response", "returns", "returned"
	if f.Class == ClassSpecField {
		section, request, carries, verb = "Spec", "request", "sends", "sent"
	}
	if slices.Contains(ops, crd.Create) {
		return fmt.Sprintf("regeneration does not add it to %s although `%s` %s it: "+
			"check generator.yaml for an ignore or rename", section, crd.Create, carries)
	}
	for _, op := range ops {
		if slices.Contains(crd.Ops, op) {
			return fmt.Sprintf("regeneration does not add it: codegen builds %s only from the Create %s, "+
				"so it needs a field with `from: %s`", section, request, op)
		}
	}
	if len(ops) == 0 {
		return "regeneration does not add it: needs a `from:` field or a hook"
	}
	return fmt.Sprintf("%s only by %s, which the generated code does not call: needs a `from:` field or a hook",
		verb, quoteOps(ops))
}

// codeGeneratorReleaseWarning returns a warning when code-generator has a release
// newer than built (the version in this binary), or "" when it does not. The
// caller makes it once per run; a failure is itself only a warning, as the oracle
// still runs on one consistent version.
func codeGeneratorReleaseWarning(ctx context.Context, client *github.Client, built string) string {
	release, _, err := client.Repositories.GetLatestRelease(ctx, "aws-controllers-k8s", "code-generator")
	if err != nil {
		return fmt.Sprintf("unable to check for a newer code-generator release than %s: %s", built, err)
	}
	latest := release.GetTagName()
	ours, errOurs := semver.Parse(strings.TrimPrefix(built, "v"))
	theirs, errTheirs := semver.Parse(strings.TrimPrefix(latest, "v"))
	if errOurs != nil || errTheirs != nil {
		return fmt.Sprintf("unable to compare code-generator %s with the latest release %q", built, latest)
	}
	if theirs.GreaterThan(ours) {
		return fmt.Sprintf("code-generator %s is released but this binary runs %s; bump it in go.mod "+
			"so the oracle matches what controllers regenerate with", latest, built)
	}
	return ""
}
