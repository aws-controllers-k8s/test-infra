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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/aquasecurity/go-version/pkg/semver"
	"github.com/google/go-github/v63/github"
	"gopkg.in/yaml.v3"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/generator"
)

const (
	sdkRepoOwner = "aws"
	sdkRepoName  = "aws-sdk-go-v2"

	coreModelURLTemplate       = "https://raw.githubusercontent.com/aws/aws-sdk-go-v2/%s/codegen/sdk-codegen/aws-models/%s.json"
	perServiceModelURLTemplate = "https://raw.githubusercontent.com/aws/aws-sdk-go-v2/service/%s/%s/codegen/sdk-codegen/aws-models/%s.json"

	modelFetchTimeout = 60 * time.Second

	// maxLatestCandidates bounds how many of the newest per-service tags are tried
	// for the latest model. A tag can lack the model file, so the newest alone is
	// not enough; several misses in a row mean a layout change worth looking at.
	maxLatestCandidates = 3
)

// errModelNotFound marks a model fetch that returned 404.
var errModelNotFound = errors.New("model file not found")

// tagPrefixFor returns the git ref prefix of a tag series: core aws-sdk-go-v2
// for an empty packageName, otherwise the per-service series.
func tagPrefixFor(packageName string) string {
	if packageName == "" {
		return "tags/v"
	}
	return fmt.Sprintf("tags/service/%s/", packageName)
}

// newestSemverRef picks the highest non-prerelease semver from a list of git
// refs, skipping refs whose last path segment is not semver.
func newestSemverRef(refs []string) (string, error) {
	newest, err := newestSemverRefs(refs, 1)
	if err != nil {
		return "", err
	}
	return newest[0], nil
}

// newestSemverRefs returns up to n of the highest non-prerelease semvers in refs,
// highest first, skipping the same refs newestSemverRef skips.
func newestSemverRefs(refs []string, n int) ([]string, error) {
	type candidate struct {
		version string
		parsed  semver.Version
	}
	var candidates []candidate

	for _, ref := range refs {
		version := ref[strings.LastIndex(ref, "/")+1:]
		if strings.Contains(version, "-") {
			// Prerelease, e.g. v1.44.0-rc.1.
			continue
		}

		// aquasecurity/go-version does not accept a leading `v`, so strip it to
		// parse; the result keeps the prefixed spelling for model URLs.
		parsed, err := semver.Parse(strings.TrimPrefix(version, "v"))
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{version, parsed})
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no usable semver tags among %d refs", len(refs))
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].parsed.GreaterThan(candidates[j].parsed)
	})
	newest := make([]string, 0, min(n, len(candidates)))
	for _, c := range candidates[:min(n, len(candidates))] {
		newest = append(newest, c.version)
	}
	return newest, nil
}

// resolveLatestSDKVersion returns up to maxLatestCandidates of the newest
// per-service tags for a service, highest first.
//
// It uses the per-service series even for a controller pinned to a core version:
// the core tag moves only when the core module changes, so its model can lag the
// service's, while a per-service tag is cut whenever the service model changes.
func resolveLatestSDKVersion(
	ctx context.Context,
	client *github.Client,
	packageName string,
) ([]string, error) {
	prefix := tagPrefixFor(packageName)

	opts := &github.ReferenceListOptions{ListOptions: github.ListOptions{PerPage: 100}}
	var refs []string
	for {
		page, resp, err := client.Git.ListMatchingRefs(ctx, sdkRepoOwner, sdkRepoName, &github.ReferenceListOptions{
			Ref:         prefix,
			ListOptions: opts.ListOptions,
		})
		if err != nil {
			return nil, fmt.Errorf("unable to list refs %q: %s", prefix, err)
		}
		for _, ref := range page {
			refs = append(refs, ref.GetRef())
		}
		if resp.NextPage == 0 {
			break
		}
		opts.ListOptions.Page = resp.NextPage
	}

	return newestSemverRefs(refsAtPrefixDepth(refs, prefix), maxLatestCandidates)
}

// latestVersionCache memoises resolveLatestSDKVersion per tag series for one run.
// It is not concurrency-safe; the reconcile loop is sequential.
//
// Failures are cached too: the likeliest persistent failure is a rate-limit 403,
// which re-listing for every service would only prolong, and a run is shorter than
// a rate-limit window. Each service still records its own analysis failure.
type latestVersionCache struct {
	resolved map[string]latestVersionResult
}

// latestVersionResult is one series' candidate versions or the error resolving them.
type latestVersionResult struct {
	versions []string
	err      error
}

func newLatestVersionCache() *latestVersionCache {
	return &latestVersionCache{resolved: map[string]latestVersionResult{}}
}

// resolve is resolveLatestSDKVersion through the cache, keyed by ref prefix.
func (c *latestVersionCache) resolve(
	ctx context.Context,
	client *github.Client,
	packageName string,
) ([]string, error) {
	series := tagPrefixFor(packageName)
	if result, ok := c.resolved[series]; ok {
		return result.versions, result.err
	}

	versions, err := resolveLatestSDKVersion(ctx, client, packageName)
	if err != nil {
		c.resolved[series] = latestVersionResult{err: err}
		return nil, err
	}
	c.resolved[series] = latestVersionResult{versions: versions}
	return versions, nil
}

// latestModel returns the newest of candidates (highest first) whose model file
// exists, and that model. Only a 404 moves on to the next candidate; other errors
// are returned. Reaching releaseVersion returns found false and no error: the
// controller already ships the newest SDK that has the model.
func latestModel(
	ctx context.Context,
	cacheDir, modelName, packageName, releaseVersion string,
	candidates []string,
) (version string, model *SmithyModel, found bool, err error) {
	for _, candidate := range candidates {
		if candidate == releaseVersion {
			return candidate, nil, false, nil
		}
		model, err := fetchModel(ctx, cacheDir, modelName, packageName, "", candidate)
		if errors.Is(err, errModelNotFound) {
			log.Printf("%s: no %s model at service/%s/%s; trying the next newest tag",
				packageName, modelName, packageName, candidate)
			continue
		}
		if err != nil {
			return candidate, nil, false, err
		}
		return candidate, model, true, nil
	}
	return "", nil, false, fmt.Errorf("none of the %d newest service/%s tags %v has the %s model",
		len(candidates), packageName, candidates, modelName)
}

// refsAtPrefixDepth keeps only refs whose remainder after the prefix is a single
// path segment, discarding tags for nested sub-modules. GitHub's matching-refs
// filter is textual, so `tags/service/s3/` also matches
// `refs/tags/service/s3/internal/configtesting/v0.1.0`.
func refsAtPrefixDepth(refs []string, prefix string) []string {
	full := "refs/" + prefix
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		remainder := strings.TrimPrefix(ref, full)
		if remainder == ref {
			continue
		}
		if strings.Contains(remainder, "/") {
			continue
		}
		out = append(out, ref)
	}
	return out
}

// modelURL builds the raw.githubusercontent URL for a model JSON. When
// serviceVersion is non-empty the per-service path is used and coreVersion is
// ignored.
func modelURL(modelName, packageName, coreVersion, serviceVersion string) string {
	if serviceVersion != "" {
		return fmt.Sprintf(perServiceModelURLTemplate, packageName, serviceVersion, modelName)
	}
	return fmt.Sprintf(coreModelURLTemplate, coreVersion, modelName)
}

// fetchModel returns the parsed model at a version, caching the raw JSON on
// disk under cacheDir so that a baseline shared between services is fetched
// once.
func fetchModel(
	ctx context.Context,
	cacheDir string,
	modelName, packageName, coreVersion, serviceVersion string,
) (*SmithyModel, error) {
	url := modelURL(modelName, packageName, coreVersion, serviceVersion)

	// The path is qualified by series because core v1.41.5 and
	// service/<pkg>/v1.41.5 are different models, and the cache can persist
	// across runs. filepath.Base keeps generator.yaml values such as
	// `package_name: ../..` from escaping cacheDir.
	series := "core"
	version := coreVersion
	if serviceVersion != "" {
		series = "service"
		version = serviceVersion
	}
	cachePath := filepath.Join(
		cacheDir, series, filepath.Base(packageName), version, filepath.Base(modelName)+".json",
	)

	if data, err := os.ReadFile(cachePath); err == nil {
		return LoadSmithyModel(data)
	}

	data, err := httpGet(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return nil, fmt.Errorf("unable to create cache dir for %s: %s", cachePath, err)
	}
	if err := os.WriteFile(cachePath, data, 0o644); err != nil {
		return nil, fmt.Errorf("unable to write cache file %s: %s", cachePath, err)
	}
	return LoadSmithyModel(data)
}

// modelHTTPClient is used instead of http.DefaultClient so model fetching does
// not share transport state with the rest of the process.
var modelHTTPClient = &http.Client{}

func httpGet(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, modelFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("unable to build request for %s: %s", url, err)
	}
	resp, err := modelHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("unable to GET %s: %s", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Drain so the connection can be reused. A 404 is expected: a
		// per-service tag can exist without the model file.
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("GET %s returned %d: %w", url, resp.StatusCode, errModelNotFound)
		}
		return nil, fmt.Errorf("GET %s returned %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// FindingClass categorises a detected change by the kind of work it implies.
type FindingClass int

const (
	// ClassNewResource is a resource codegen would add a CRD for today.
	ClassNewResource FindingClass = iota
	// ClassNewOperation is an uncalled operation on a resource that has a CRD.
	// Operations on a resource reported as new go into that resource's Evidence
	// instead (foldOperationsIntoResources).
	ClassNewOperation
	// ClassUnknownOperation is an operation whose name does not classify, so it
	// needs human judgement.
	ClassUnknownOperation
	// ClassSpecField is a new member that could be desired state: set at Create,
	// or set at Update and readable back. See fieldRoles.class.
	ClassSpecField
	// ClassPossibleResource is a resource managed without a Create verb (e.g.
	// RegisterCapability/DeregisterCapability), which codegen cannot infer
	// without an `operations:` override.
	ClassPossibleResource
	// ClassDroppedOperation is an operation nothing in ACK could model, with the
	// reason as its Detail. It is listed for transparency only, never reported.
	ClassDroppedOperation
	// ClassStatusField is a new member only ever returned: observed state.
	ClassStatusField
	// ClassLifecycleField is a new member only sent on Update or Delete and never
	// returned (e.g. ec2's QuoteId). It parameterises one call, so a maintainer
	// decides whether it belongs in the CRD.
	ClassLifecycleField
	// ClassDroppedField is a new member only on a read request (e.g. ec2's
	// IncludeManagedResources). It is listed for transparency only.
	ClassDroppedField
	// ClassTransientResource is a resource codegen would add a CRD for but which
	// has no Delete operation, so AWS expires or consumes it.
	ClassTransientResource
)

// isDropped reports whether a class is shown only in the issue's collapsed
// "not reported" list.
func (c FindingClass) isDropped() bool {
	return c == ClassDroppedOperation || c == ClassDroppedField
}

// isOperation reports whether a class is an operation finding. Operations are
// supporting evidence, never a reason to notify on their own: see actionable.
func (c FindingClass) isOperation() bool {
	return c == ClassNewOperation || c == ClassUnknownOperation
}

// reportable returns the findings new since the controller's SDK release and not
// dropped. The rest are shown in the issue for transparency only, so counts and
// rendering of findings go through here.
func reportable(findings []Finding) []Finding {
	out := make([]Finding, 0, len(findings))
	for _, f := range findings {
		if !f.Class.isDropped() && f.NewSincePin {
			out = append(out, f)
		}
	}
	return out
}

// actionable returns the reportable findings about a resource or field. It, not
// reportable, decides whether an issue is filed, and it is what the fingerprint
// hashes, so a change in operations alone never notifies anyone or re-files a
// closed issue; it only silently rewords an open one.
func actionable(findings []Finding) []Finding {
	out := make([]Finding, 0, len(findings))
	for _, f := range reportable(findings) {
		if !f.Class.isOperation() {
			out = append(out, f)
		}
	}
	return out
}

// markPreexisting clears NewSincePin on every finding the release model already
// has. release is the model at the service module version in the controller's
// go.mod; nil leaves findings unchanged.
//
// Producers diff against the generation model, which answers "is this a gap" but
// not "is this new": a controller can build against a much newer service module
// than it was generated from.
func markPreexisting(findings []Finding, release *SmithyModel, in *ControllerInputs) []Finding {
	if release == nil {
		return findings
	}
	releaseResources := createResourceNames(release, in, in.Config.ResourceNames())
	inRelease := func(opID string) bool {
		_, ok := release.Operation(opID)
		return ok
	}
	out := slices.Clone(findings)
	for i, f := range out {
		var old bool
		switch f.Class {
		case ClassNewResource, ClassTransientResource:
			old = releaseResources[f.Subject]
		case ClassPossibleResource:
			// Only its own operations decide; a generic read in Evidence can be
			// far older than the resource.
			old = slices.ContainsFunc(f.evidenceOps(), func(opID string) bool {
				return strings.Contains(opID, f.Subject) && inRelease(opID)
			})
		case ClassNewOperation, ClassUnknownOperation, ClassDroppedOperation:
			old = inRelease(f.Subject)
		case ClassSpecField, ClassStatusField, ClassLifecycleField, ClassDroppedField:
			old = slices.ContainsFunc(f.evidenceOps(), func(opID string) bool {
				return fieldInModel(release, in, opID, f.Subject)
			})
		}
		if old {
			out[i].NewSincePin = false
		}
	}
	return out
}

// fieldInModel reports whether an operation's request or response carries a member
// path, spelled as findAddedFields keys it (relative to any unwrapped wrapper).
func fieldInModel(m *SmithyModel, in *ControllerInputs, opID, path string) bool {
	op, ok := m.Operation(opID)
	if !ok {
		return false
	}
	for _, side := range []struct {
		ref      *SmithyMemberRef
		isOutput bool
	}{{op.Input, false}, {op.Output, true}} {
		if side.ref == nil {
			continue
		}
		members := m.WalkMembers(side.ref.Target, maxWalkDepth)
		if _, ok := members[path]; ok {
			return true
		}
		wrapper := in.Config.inputWrapper(opID)
		if side.isOutput {
			wrapper = outputWrapper(m, in, opID, side.ref)
		}
		if wrapper != "" {
			if _, ok := members[wrapper+"."+path]; ok {
				return true
			}
		}
	}
	return false
}

// String returns the stable name of a finding class. Names are hashed into issue
// fingerprints, so renaming or reusing one invalidates every open issue's marker.
func (c FindingClass) String() string {
	switch c {
	case ClassNewResource:
		return "new-resource"
	case ClassNewOperation:
		return "new-operation"
	case ClassUnknownOperation:
		return "unknown-operation"
	case ClassSpecField:
		return "spec-field"
	case ClassPossibleResource:
		return "possible-resource"
	case ClassDroppedOperation:
		return "dropped-operation"
	case ClassStatusField:
		return "status-field"
	case ClassLifecycleField:
		return "lifecycle-field"
	case ClassDroppedField:
		return "dropped-field"
	case ClassTransientResource:
		return "transient-resource"
	}
	return fmt.Sprintf("unnamed-class-%d", int(c))
}

// Finding is one detected change.
type Finding struct {
	// Kind is the resource kind, or "" when not attributable to one resource.
	Kind  string
	Class FindingClass
	// Subject is the operation name, resource name, or member path.
	Subject string
	// Detail is human-readable supporting text, or "" when the section heading
	// says enough.
	Detail string
	// NewSincePin records whether the subject is new since the SDK release the
	// controller builds against. Only new findings drive the notification. See
	// markPreexisting.
	NewSincePin bool
	// Evidence is the sorted, comma-separated operations supporting the finding.
	// Unlike Detail it is hashed into the fingerprint. A string so that Finding
	// stays comparable.
	Evidence string
	// SetBy, ReadBy and ReturnedBy split operations by what they do with a field:
	// send it, return it from a read, or return it from another response. Same
	// form as Evidence; a ReadBy or ReturnedBy entry is `Op=Path` when returned at
	// a different path. Not hashed. See annotateFields.
	SetBy      string
	ReadBy     string
	ReturnedBy string
}

// newEvidence builds an Evidence value from operation names in any order.
func newEvidence(ops []string) string {
	ops = slices.Clone(ops)
	sort.Strings(ops)
	return strings.Join(slices.Compact(ops), ",")
}

// evidenceOps returns a finding's Evidence as a list.
func (f Finding) evidenceOps() []string {
	if f.Evidence == "" {
		return nil
	}
	return strings.Split(f.Evidence, ",")
}

// quoteOps renders operation names as a comma-separated list of code spans.
func quoteOps(ops []string) string {
	quoted := make([]string, len(ops))
	for i, op := range ops {
		quoted[i] = "`" + op + "`"
	}
	return strings.Join(quoted, ", ")
}

// denylistedOpPrefixes are verbs that never correspond to ACK-managed
// resources or fields.
var denylistedOpPrefixes = []string{"Tag", "Untag"}

func isDenylistedOp(opID string) bool {
	for _, prefix := range denylistedOpPrefixes {
		if strings.HasPrefix(opID, prefix) {
			return true
		}
	}
	return false
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

// findNewResources reports resources codegen would generate a CRD for today but
// the controller does not have. It mirrors codegen's rule, so this is the set of
// CRDs a regeneration would add.
func findNewResources(latest, baseline *SmithyModel, in *ControllerInputs) []Finding {
	declared := in.Config.ResourceNames()
	ignored := in.Config.Ignore.ResourceNames

	// NewSincePin is decided per resource, not per operation: an `operations:`
	// override can imply one resource through several Create operations.
	baselineResources := createResourceNames(baseline, in, declared)
	opsByResource := operationsByResource(latest, in)

	var findings []Finding
	seen := map[string]bool{}
	for _, opID := range latest.OperationNames() {
		opTypes, resName := in.ClassifyOpWithOverrides(opID, declared)

		// OpTypeCreate only: code-generator builds CRDs from opMap[OpTypeCreate]
		// alone (pkg/model/model.go), so OpTypeCreateBatch and OpTypeReplace
		// produce no CRD.
		if !opTypes.Has(OpTypeCreate) {
			continue
		}
		if seen[resName] || slices.Contains(ignored, resName) || in.HasCRD(resName) {
			continue
		}
		seen[resName] = true

		ops := opsByResource[strings.ToLower(resName)]
		f := Finding{
			Kind:        resName,
			Class:       ClassNewResource,
			Subject:     resName,
			Detail:      fmt.Sprintf("implied by `%s`, so codegen generates a CRD for it", opID),
			NewSincePin: !baselineResources[resName],
			Evidence:    newEvidence(ops),
		}
		// A resource nothing can delete is one AWS expires or consumes; a CRD for
		// it is rarely wanted.
		if !slices.ContainsFunc(ops, func(op string) bool {
			opTypes, _ := in.ClassifyOpWithOverrides(op, declared)
			return opTypes.Has(OpTypeDelete)
		}) {
			f.Class = ClassTransientResource
			f.Detail = fmt.Sprintf("implied by `%s`, but nothing deletes it: AWS expires or consumes it", opID)
		}
		findings = append(findings, f)
	}
	return findings
}

// operationsByResource maps each lowercased resource name to the operations that
// classify onto it, excluding Tag operations and ignore.operations.
func operationsByResource(m *SmithyModel, in *ControllerInputs) map[string][]string {
	declared := in.Config.ResourceNames()
	out := map[string][]string{}
	for _, opID := range m.OperationNames() {
		if isDenylistedOp(opID) || slices.Contains(in.Config.Ignore.Operations, opID) {
			continue
		}
		if opTypes, resName := in.ClassifyOpWithOverrides(opID, declared); !opTypes.Has(OpTypeUnknown) {
			low := strings.ToLower(resName)
			out[low] = append(out[low], opID)
		}
	}
	return out
}

// foldOperationsIntoResources moves operations on a new or possibly new resource
// into that resource's Evidence: they implement it rather than being a separate gap.
func foldOperationsIntoResources(findings []Finding) []Finding {
	resources := map[string]int{}
	for i, f := range findings {
		if isResourceClass(f.Class) {
			resources[f.Kind] = i
		}
	}
	out := make([]Finding, 0, len(findings))
	folded := map[int][]string{}
	for _, f := range findings {
		if i, ok := resources[f.Kind]; ok && f.Class == ClassNewOperation {
			folded[i] = append(folded[i], f.Subject)
			continue
		}
		out = append(out, f)
	}
	for i := range out {
		if !isResourceClass(out[i].Class) {
			continue
		}
		if ops := folded[resources[out[i].Kind]]; len(ops) > 0 {
			out[i].Evidence = newEvidence(append(out[i].evidenceOps(), ops...))
		}
	}
	return out
}

// createResourceNames returns the set of resource names a model implies a CRD
// for, by the same OpTypeCreate rule findNewResources applies.
func createResourceNames(
	m *SmithyModel,
	in *ControllerInputs,
	declared []string,
) map[string]bool {
	out := map[string]bool{}
	for _, opID := range m.OperationNames() {
		if opTypes, resName := in.ClassifyOpWithOverrides(opID, declared); opTypes.Has(OpTypeCreate) {
			out[resName] = true
		}
	}
	return out
}

// findNewOperations reports operations the controller does not call. Ones on an
// existing CRD are ClassNewOperation; Create operations for a resource with no
// CRD are left to findNewResources. Unclassified operations go through
// placeUnknownOp, which places them on a resource, as a possible resource,
// drops them, or leaves them as ClassUnknownOperation.
func findNewOperations(latest, baseline *SmithyModel, in *ControllerInputs) []Finding {
	declared := in.Config.ResourceNames()
	used := allUsedOps(in.UsedOps)

	// Every resource the report knows about, for placeUnknownOp: CRD kinds and
	// the resources findNewResources reports.
	resources := map[string]string{}
	existing := map[string]bool{}
	for low, kind := range in.kindsByLower {
		resources[low] = kind
		existing[kind] = true
	}
	for _, f := range findNewResources(latest, baseline, in) {
		resources[strings.ToLower(f.Subject)] = f.Subject
	}
	possible := map[string][]string{}

	var findings []Finding
	for _, opID := range latest.OperationNames() {
		if used[opID] || isDenylistedOp(opID) {
			continue
		}
		if slices.Contains(in.Config.Ignore.Operations, opID) {
			continue
		}

		opTypes, resName := in.ClassifyOpWithOverrides(opID, declared)
		_, inBaseline := baseline.Operation(opID)
		canonicalKind, hasCRD := in.CanonicalKind(resName)
		_, knownResource := resources[strings.ToLower(resName)]

		switch {
		// A classified operation whose resource name is unknown is placed like an
		// unclassified one: it is usually on a sub-object, e.g.
		// ModifyVpcEndpointPayerResponsibility configures VPCEndpoint.
		case opTypes.Has(OpTypeUnknown) || (!opTypes.Has(OpTypeCreate) && !knownResource):
			// The ignored-resource substring test must stay inside this arm. Run
			// before classification it would hide real findings whenever an
			// ignored name is a substring of a managed one (ec2 ignores `Route`
			// but manages `RouteTable`).
			if ignored := ignoredNameIn(opID, in.Config.Ignore.ResourceNames, resources); ignored != "" {
				// Listed when new, so a deliberate ignore is distinguishable
				// from a miss.
				if !inBaseline {
					findings = append(findings, Finding{
						Class:       ClassDroppedOperation,
						Subject:     opID,
						Detail:      fmt.Sprintf("concerns `%s`, which generator.yaml ignores", ignored),
						NewSincePin: true,
					})
				}
				continue
			}

			// Only operations new since the baseline are reported: an old
			// unclassifiable operation has already been declined, and reporting
			// them all would be permanent noise (over 100 for ec2).
			if inBaseline {
				// An old status read can still return new state (e.g.
				// DescribeInstanceStatus gained ApplicationStatus). Only reads of
				// the resource's own status count.
				if isReadOp(opTypes, opID) {
					if placement, name := placeUnknownOp(opID, latest, resources, existing); placement == placeOnResource &&
						existing[name] && isStatusRead(opID, name) {
						findings = append(findings, readStatusCandidates(latest, baseline, in, name, opID)...)
					}
				}
				continue
			}
			switch placement, name := placeUnknownOp(opID, latest, resources, existing); placement {
			case placeDrop:
				findings = append(findings, Finding{
					Class:       ClassDroppedOperation,
					Subject:     opID,
					Detail:      name,
					NewSincePin: true,
				})
			case placeOnResource:
				findings = append(findings, newOperationFinding(latest, name, opID, opTypes))
				if existing[name] {
					findings = append(findings, setterFieldCandidates(latest, in, name, opID, opTypes)...)
					if isReadOp(opTypes, opID) {
						findings = append(findings, readStatusCandidates(latest, baseline, in, name, opID)...)
					}
				}
			case placePossibleResource:
				possible[name] = append(possible[name], opID)
			default:
				findings = append(findings, Finding{
					Class:       ClassUnknownOperation,
					Subject:     opID,
					NewSincePin: true,
				})
			}
		case hasCRD:
			// As above: an old operation still not called was declined, not
			// missed (typically List*, which ACK does not call by design).
			if inBaseline {
				continue
			}
			// Report the CRD's own casing, not the AWS spelling.
			findings = append(findings, newOperationFinding(latest, canonicalKind, opID, opTypes))
			findings = append(findings, setterFieldCandidates(latest, in, canonicalKind, opID, opTypes)...)
			if isReadOp(opTypes, opID) {
				findings = append(findings, readStatusCandidates(latest, baseline, in, canonicalKind, opID)...)
			}
		}
	}

	// One finding per possible resource, naming every operation that manages it,
	// including ones that classify (e.g. its Delete).
	opsByResource := operationsByResource(latest, in)
	for name, opIDs := range possible {
		// A resource with only reads is not one a controller could manage.
		allOps := append(slices.Clone(opIDs), opsByResource[strings.ToLower(name)]...)
		if !slices.ContainsFunc(allOps, func(opID string) bool {
			opTypes, _ := in.ClassifyOpWithOverrides(opID, declared)
			return !isReadOp(opTypes, opID)
		}) {
			for _, opID := range opIDs {
				findings = append(findings, Finding{
					Class: ClassDroppedOperation, Subject: opID, Detail: dropQuery, NewSincePin: true,
				})
			}
			continue
		}
		// Nor is one nothing can read, directly or through a broader resource: a
		// controller could not recover its state after a restart.
		reads := genericReads(latest, name, opIDs)
		if !slices.ContainsFunc(append(slices.Clone(allOps), reads...), func(opID string) bool {
			opTypes, _ := in.ClassifyOpWithOverrides(opID, declared)
			return isReadOp(opTypes, opID)
		}) {
			slices.Sort(allOps)
			for _, opID := range slices.Compact(allOps) {
				findings = append(findings, Finding{
					Class: ClassDroppedOperation, Subject: opID, NewSincePin: true,
					Detail: fmt.Sprintf("lifecycle action on `%s`, which has no Create and no read operation, "+
						"so a controller could not recover its state", name),
				})
			}
			continue
		}
		findings = append(findings, Finding{
			Kind:        name,
			Class:       ClassPossibleResource,
			Subject:     name,
			Detail:      "no Create operation, so a controller cannot create it; modelling it needs an `operations:` override",
			NewSincePin: true,
			Evidence:    newEvidence(append(allOps, reads...)),
		})
	}
	return findings
}

// genericReads returns the reads of the broader resource a possible resource's
// operations identify it by: AcceptTransitGatewayClientVpnAttachment takes a
// TransitGatewayAttachmentId, read via DescribeTransitGatewayAttachments.
func genericReads(m *SmithyModel, name string, opIDs []string) []string {
	var out []string
	for _, opID := range opIDs {
		for _, id := range identifierMembers(requestMembers(m, opID)) {
			stem := identifierRE.ReplaceAllString(id, "")
			if stem == "" || strings.EqualFold(stem, name) {
				continue
			}
			for _, read := range []string{"Get" + stem, "Describe" + stem, "Describe" + stem + "s", "List" + stem + "s"} {
				if _, ok := m.Operation(read); ok {
					out = append(out, read)
				}
			}
		}
	}
	return out
}

// readStatusCandidates returns the members an uncalled read's response gained
// since the generation model, as Status candidates for the resource it is placed
// on. findAddedFields only sees operations the controller calls.
func readStatusCandidates(latest, baseline *SmithyModel, in *ControllerInputs, kind, opID string) []Finding {
	op, ok := latest.Operation(opID)
	if !ok || op.Output == nil {
		return nil
	}
	latestMembers := latest.WalkMembers(op.Output.Target, maxWalkDepth)
	baselineMembers := map[string]MemberInfo{}
	history := false
	if old, ok := baseline.Operation(opID); ok && old.Output != nil {
		baselineMembers = baseline.WalkMembers(old.Output.Target, maxWalkDepth)
	} else if !ok {
		// A new read counts only when its response has one top-level member; a
		// response of many scalars describes something other than the resource.
		top := 0
		for name := range latest.Shapes[op.Output.Target].Members {
			if !strings.EqualFold(name, "NextToken") {
				top++
			}
		}
		if top != 1 {
			return nil
		}
		history = isGeneratedHistory(latest, op)
	}
	var out []Finding
	for path := range latestMembers {
		if _, existed := baselineMembers[path]; existed {
			continue
		}
		if strings.EqualFold(path, "NextToken") || hasNewAncestor(path, latestMembers, baselineMembers) {
			continue
		}
		if exposedInCRD(in, kind, path) ||
			declinedShapeAncestor(path, latestMembers, in.Config.Ignore.ShapeNames) {
			continue
		}
		f := Finding{
			Kind: kind, Class: ClassStatusField, Subject: path, NewSincePin: true, Evidence: opID,
			Detail: secondaryReadDetail,
		}
		if history {
			f.Class = ClassDroppedField
			f.Detail = dropGeneratedHistory
		}
		out = append(out, f)
	}
	return out
}

// dropGeneratedHistory is the reason a paginated record of generated items is not
// a Status candidate.
const dropGeneratedHistory = "paginated history of items the service generates, not resource state: " +
	"in Status it grows without bound and churns reconciliation; expose at most a bounded latest summary"

// isGeneratedHistory reports whether a read pages through items no Put, Create,
// Update, Set or Modify operation sets, such as reports a Start* operation
// produces.
func isGeneratedHistory(m *SmithyModel, op SmithyShape) bool {
	members := m.Shapes[op.Output.Target].Members
	if _, paged := members["NextToken"]; !paged {
		if _, paged = members["nextToken"]; !paged {
			return false
		}
	}
	for name := range members {
		if strings.EqualFold(name, "NextToken") {
			continue
		}
		item := strings.ToLower(pluralizer.Singular(name))
		for _, opID := range m.OperationNames() {
			lowered := strings.ToLower(opID)
			for _, verb := range []string{"put", "create", "update", "set", "modify"} {
				if strings.HasPrefix(lowered, verb) && strings.Contains(lowered, item) {
					return false
				}
			}
		}
	}
	return true
}

// secondaryReadDetail marks a Status candidate returned only by an uncalled read,
// which usually answers for many resources at once.
const secondaryReadDetail = "from a read the controller does not call: needs a custom read hook " +
	"that filters the response to this resource, not only regeneration"

// isStatusRead reports whether a read is of a resource's status or health:
// Describe<Kind>Status, Get<Kind>Health and the like.
func isStatusRead(opID, kind string) bool {
	m := unknownOpRE.FindStringSubmatch(opID)
	if m == nil {
		return false
	}
	rest, ok := strings.CutPrefix(strings.ToLower(m[3]), strings.ToLower(kind))
	return ok && slices.Contains([]string{"status", "statuses", "health"}, rest)
}

// newOperationFinding is a new operation on a resource. A read is flagged as
// such, since its response could back Status fields.
func newOperationFinding(m *SmithyModel, kind, opID string, opTypes OpTypes) Finding {
	f := Finding{Kind: kind, Class: ClassNewOperation, Subject: opID, NewSincePin: true}
	if isReadOp(opTypes, opID) {
		f.Detail = "read: its response could back Status fields"
		if op, ok := m.Operation(opID); ok && op.Output != nil && isGeneratedHistory(m, op) {
			f.Detail = "read: pages through a history the service generates, so it is context for a " +
				"bounded summary at most, not a source of Status fields"
		}
	}
	return f
}

// isReadOp reports whether an operation reads, by classification or else verb.
func isReadOp(opTypes OpTypes, opID string) bool {
	if opTypes.Has(OpTypeGet, OpTypeList, OpTypeGetAttributes) {
		return true
	}
	return slices.ContainsFunc(readVerbs, func(v string) bool { return strings.HasPrefix(opID, v) })
}

// requestPlumbingMembers qualify how a call is made, not what it sets.
var requestPlumbingMembers = []string{
	"ChecksumAlgorithm", "ContentMD5", "ExpectedBucketOwner", "RequestPayer",
}

// setterFieldCandidates returns the request members of a new operation that sets
// part of an existing resource. They are Spec candidates when a read returns them
// at the same top-level name, which plain reconciliation needs; otherwise they
// are lifecycle fields needing custom code. The resource's identifier, plumbing
// members and anything the CRD exposes are skipped.
func setterFieldCandidates(m *SmithyModel, in *ControllerInputs, kind, opID string, opTypes OpTypes) []Finding {
	setter := opTypes.Has(OpTypeUpdate, OpTypeSetAttributes) ||
		strings.HasPrefix(opID, "Put") || strings.HasPrefix(opID, "Set")
	if !setter {
		return nil
	}
	readable := returnedNames(m, in, kind, opID)
	var out []Finding
	for member := range requestMembers(m, opID) {
		if slices.Contains(requestPlumbingMembers, member) {
			continue
		}
		stem := identifierRE.ReplaceAllString(member, "")
		// The resource's own identifier: VpcEndpointId, or s3's bare Bucket.
		if strings.EqualFold(stem, kind) {
			continue
		}
		if exposedInCRD(in, kind, member) {
			continue
		}
		f := Finding{Kind: kind, Class: ClassSpecField, Subject: member, NewSincePin: true, Evidence: opID}
		if !readable[strings.ToLower(member)] {
			f.Class = ClassLifecycleField
			f.Detail = "not returned at this path, so reconciling it needs custom code"
		}
		out = append(out, f)
	}
	return out
}

// returnedNames returns the lowercased top-level members of the responses of a
// resource's operations and of opID itself, looking through a response wrapper.
// Top level only: a field read back from inside a list needs custom reconciliation.
func returnedNames(m *SmithyModel, in *ControllerInputs, kind, opID string) map[string]bool {
	names := map[string]bool{}
	ops := []string{opID}
	for op := range in.UsedOps[kindToResourceDir(kind)] {
		ops = append(ops, op)
	}
	for _, op := range ops {
		shape, ok := m.Operation(op)
		if !ok || shape.Output == nil {
			continue
		}
		wrapper := outputWrapper(m, in, op, shape.Output)
		for path := range m.WalkMembers(shape.Output.Target, maxWalkDepth) {
			if path = unwrapped(path, wrapper); !strings.Contains(path, ".") {
				names[strings.ToLower(path)] = true
			}
		}
	}
	return names
}

// kindToResourceDir maps a resource kind onto the key scanUsedOps stores its
// operations under. See normalizeResourceKey.
func kindToResourceDir(kind string) string {
	return normalizeResourceKey(kind)
}

// exposedInCRD reports whether an AWS member path is surfaced by a resource's CRD,
// after applying generator.yaml renames and lowercasing each segment.
func exposedInCRD(in *ControllerInputs, kind, awsPath string) bool {
	fields, ok := in.CRDFields[kind]
	if !ok {
		return false
	}

	segments := in.Config.renamedPath(kind, strings.Split(awsPath, "."))
	for i, segment := range segments {
		segments[i] = strings.ToLower(segment)
	}
	return fields[strings.Join(segments, ".")]
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

// elementShape resolves a list, however deeply nested, to its element shape.
func elementShape(m *SmithyModel, shapeID string) string {
	for range maxWalkDepth {
		shape, ok := m.Shapes[shapeID]
		if !ok || shape.Type != "list" || shape.Member == nil {
			return shapeID
		}
		shapeID = shape.Member.Target
	}
	return shapeID
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

// sourcedAsCRDField reports whether a member is exposed under another name because
// generator.yaml's `resources.<Kind>.fields.<Name>.from` sources a field from it.
// The declared path matches exactly or as a prefix, covering its children.
func sourcedAsCRDField(in *ControllerInputs, kind, opName, path string) bool {
	res, ok := in.Config.resource(kind)
	if !ok {
		return false
	}
	for _, field := range res.Fields {
		if field.From == nil {
			continue
		}
		if !strings.EqualFold(field.From.Operation, opName) {
			continue
		}
		declared := strings.ToLower(field.From.Path)
		lowered := strings.ToLower(path)
		if declared != "" &&
			(lowered == declared || strings.HasPrefix(lowered, declared+".")) {
			return true
		}
	}
	return false
}

// isACKManagedARN reports whether a member is the resource ARN that ack-generate
// wires onto Status.ACKResourceMetadata.ARN. This is a codegen convention, not
// configuration, for response members named `Arn` or `<Kind>Arn`; only the last
// path segment is matched.
func isACKManagedARN(kind, path string) bool {
	segments := strings.Split(path, ".")
	last := strings.ToLower(segments[len(segments)-1])
	return last == "arn" || last == strings.ToLower(kind)+"arn"
}

// findAddedFields reports members added to operations the controller calls that
// the resource's CRD does not expose: one finding per new field per resource,
// not per operation, with Evidence naming the operations it appeared in. Each is
// classed by how those operations use it (fieldRoles.class).
func findAddedFields(latest, baseline *SmithyModel, in *ControllerInputs) []Finding {
	var findings []Finding
	declared := in.Config.ResourceNames()

	for kind := range in.CRDFields {
		ops := in.UsedOps[kindToResourceDir(kind)]
		if len(ops) == 0 {
			continue
		}
		opsByPath := map[string][]string{}
		rolesByPath := map[string]*fieldRoles{}

		opNames := make([]string, 0, len(ops))
		for op := range ops {
			opNames = append(opNames, op)
		}
		sort.Strings(opNames)

		for _, opName := range opNames {
			latestOp, ok := latest.Operation(opName)
			if !ok {
				continue
			}
			baselineOp, hadBaseline := baseline.Operation(opName)
			opTypes, _ := in.ClassifyOpWithOverrides(opName, declared)

			for _, side := range []struct {
				latestRef   *SmithyMemberRef
				baselineRef *SmithyMemberRef
				isOutput    bool
			}{
				{latestOp.Input, refOrNil(hadBaseline, baselineOp.Input), false},
				{latestOp.Output, refOrNil(hadBaseline, baselineOp.Output), true},
			} {
				if side.latestRef == nil {
					continue
				}
				latestMembers := latest.WalkMembers(side.latestRef.Target, maxWalkDepth)

				baselineMembers := map[string]MemberInfo{}
				if side.baselineRef != nil {
					baselineMembers = baseline.WalkMembers(side.baselineRef.Target, maxWalkDepth)
				}

				paths := make([]string, 0, len(latestMembers))
				for path := range latestMembers {
					paths = append(paths, path)
				}
				sort.Strings(paths)

				// Codegen never infers an input wrapper, only an output one.
				wrapper := in.Config.inputWrapper(opName)
				if side.isOutput {
					wrapper = outputWrapper(latest, in, opName, side.latestRef)
				}
				rootNames := fieldRootNames(opName, side.latestRef, side.isOutput)

				for _, path := range paths {
					if _, existed := baselineMembers[path]; existed {
						continue
					}
					// Codegen flattens an input wrapper's members into Spec and
					// sends nothing outside it, so outside members are skipped
					// and inside ones lose the wrapper prefix.
					crdPath := path
					if !side.isOutput && wrapper != "" {
						rest, under := strings.CutPrefix(path, wrapper+".")
						if !under {
							continue
						}
						crdPath = rest
					}
					// A child of a new field belongs to that field's finding,
					// and shares its suppressions.
					if hasNewAncestor(path, latestMembers, baselineMembers) {
						continue
					}
					if exposedInCRD(in, kind, crdPath) {
						continue
					}
					if underDeclinedParent(in, kind, opName, path, wrapper, baselineMembers) {
						continue
					}
					// Absent from the CRD is not missing if generator.yaml or a
					// codegen convention surfaces or declines the member.
					if declinedFieldPath(latest, rootNames, path, latestMembers, in.Config.Ignore.FieldPaths) {
						continue
					}
					if declinedShapeAncestor(path, latestMembers, in.Config.Ignore.ShapeNames) {
						continue
					}
					if sourcedAsCRDField(in, kind, opName, path) {
						continue
					}
					// Output only: codegen maps a response ARN to status, but a
					// request member named `Arn` is a real field.
					if side.isOutput && isACKManagedARN(kind, path) {
						continue
					}
					// Keyed by the path as the CRD sees it, so one field reached
					// through different wrappers (`Table.X`, `TableDescription.X`)
					// is one finding. An output wrapper the CRD itself models
					// stays in the path.
					key := crdPath
					if side.isOutput && wrapper != "" && !exposedInCRD(in, kind, wrapper) {
						key = unwrapped(path, wrapper)
					}
					if !slices.Contains(opsByPath[key], opName) {
						opsByPath[key] = append(opsByPath[key], opName)
					}
					if rolesByPath[key] == nil {
						rolesByPath[key] = &fieldRoles{}
					}
					rolesByPath[key].add(opTypes, opName, side.isOutput)
				}
			}
		}

		for path, opNames := range opsByPath {
			class := rolesByPath[path].class()
			detail := ""
			if class == ClassDroppedField {
				detail = dropReadOption
			}
			findings = append(findings, Finding{
				Kind:        kind,
				Class:       class,
				Subject:     path,
				Detail:      detail,
				NewSincePin: true,
				Evidence:    newEvidence(opNames),
			})
		}
	}

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Kind != findings[j].Kind {
			return findings[i].Kind < findings[j].Kind
		}
		return findings[i].Subject < findings[j].Subject
	})
	return findings
}

// dropReadOption is the reason a read-request-only field is not reported.
const dropReadOption = "read request option: shapes what the controller reads back, not resource state"

// fieldRoles records how the operations a new field appeared in use it.
type fieldRoles struct {
	createInput bool
	updateInput bool
	deleteInput bool
	readInput   bool
	output      bool
	// readOutput is output from a read, which the controller observes on every
	// reconciliation.
	readOutput bool
}

// add records one side of one operation. An operation with several types
// (operation_type: [Create, Update]) sends the field in each role.
func (r *fieldRoles) add(opTypes OpTypes, opID string, isOutput bool) {
	if isOutput {
		r.output = true
		r.readOutput = r.readOutput || isReadOp(opTypes, opID)
		return
	}
	for _, opType := range opTypes {
		switch opType {
		case OpTypeCreate, OpTypeCreateBatch:
			r.createInput = true
		case OpTypeGet, OpTypeList, OpTypeGetAttributes:
			r.readInput = true
		case OpTypeDelete:
			r.deleteInput = true
		default:
			// Update, Replace, SetAttributes, and custom operations hooks call.
			r.updateInput = true
		}
	}
}

// class says what kind of candidate the field is: Spec if sent at Create, or sent
// at Update and returned by a read (an Update's own response does not count, as
// the controller cannot reconcile what it never reads back); Status if only
// returned; Lifecycle if only sent on Update or Delete; dropped if only on a read
// request.
func (r *fieldRoles) class() FindingClass {
	switch {
	case r.createInput || (r.updateInput && r.readOutput):
		return ClassSpecField
	case r.updateInput:
		return ClassLifecycleField
	case r.output:
		return ClassStatusField
	case r.deleteInput:
		return ClassLifecycleField
	}
	return ClassDroppedField
}

// hasNewAncestor reports whether any ancestor of path is also new since the pin.
func hasNewAncestor(path string, latestMembers, baselineMembers map[string]MemberInfo) bool {
	for i := strings.LastIndex(path, "."); i > 0; i = strings.LastIndex(path[:i], ".") {
		ancestor := path[:i]
		if _, inLatest := latestMembers[ancestor]; !inLatest {
			continue
		}
		if _, existed := baselineMembers[ancestor]; !existed {
			return true
		}
	}
	return false
}

// outputWrapper returns the response member codegen unwraps before mapping a
// response onto a resource, or "" when it reads the response as-is. Mirrors
// code-generator's SetResource: a configured output_wrapper_field_path wins;
// otherwise a response whose only member is a structure is unwrapped.
func outputWrapper(m *SmithyModel, in *ControllerInputs, opName string, output *SmithyMemberRef) string {
	if in.Config != nil {
		if override, ok := in.Config.Operations[opName]; ok && override.OutputWrapperFieldPath != "" {
			return override.OutputWrapperFieldPath
		}
	}
	if output == nil {
		return ""
	}
	shape, ok := m.Shapes[output.Target]
	if !ok || len(shape.Members) != 1 {
		return ""
	}
	for name, member := range shape.Members {
		if m.Shapes[member.Target].Type == "structure" {
			return name
		}
	}
	return ""
}

// unwrapped strips wrapper from the front of an output member path, if present.
func unwrapped(path, wrapper string) string {
	if wrapper == "" {
		return path
	}
	if rest, ok := strings.CutPrefix(path, wrapper+"."); ok {
		return rest
	}
	return path
}

// underDeclinedParent reports whether a new member sits beneath a member that
// already existed at the pin and is not in the CRD. Codegen generates a nested
// field only as part of its parent's type, so regeneration cannot add it. Only
// the nearest pre-existing ancestor decides; the wrapper never does.
func underDeclinedParent(
	in *ControllerInputs,
	kind, opName, path, wrapper string,
	baselineMembers map[string]MemberInfo,
) bool {
	for i := strings.LastIndex(path, "."); i > 0; i = strings.LastIndex(path[:i], ".") {
		ancestor := path[:i]
		if _, existed := baselineMembers[ancestor]; !existed {
			continue
		}
		if ancestor == wrapper {
			return false
		}
		// Check both spellings: a CRD can model the wrapper itself (s3's
		// `objectLockConfiguration`).
		return !exposedInCRD(in, kind, ancestor) &&
			!exposedInCRD(in, kind, unwrapped(ancestor, wrapper)) &&
			!sourcedAsCRDField(in, kind, opName, ancestor)
	}
	return false
}

// refOrNil returns ref only when present is true, so a missing baseline
// operation has no members.
func refOrNil(present bool, ref *SmithyMemberRef) *SmithyMemberRef {
	if !present {
		return nil
	}
	return ref
}

const fingerprintPrefix = "<!-- ack-api-change-fingerprint: "

// The generated region bounds what renderIssueBody writes, so a maintainer's
// additions to the body survive a refresh and only the marker inside it counts.
const (
	generatedRegionBegin = "<!-- ack-api-change-begin -->"
	generatedRegionEnd   = "<!-- ack-api-change-end -->"
)

// fingerprintRE matches the marker on a line of its own. The trailing [ \t\r]* is
// required: Go's (?m)$ matches only before \n, and a body edited in the GitHub web
// UI comes back CRLF-terminated.
var fingerprintRE = regexp.MustCompile(
	`(?m)^` + regexp.QuoteMeta(fingerprintPrefix) + `([0-9a-f]{64}) -->[ \t\r]*$`,
)

// dedupeFindings drops exact duplicates, preserving input order. Producers walk
// Input and Output independently with root-relative paths, so a member on both
// sides can yield two identical findings. The whole struct is compared, so
// findings that differ only in Detail both survive.
func dedupeFindings(findings []Finding) []Finding {
	seen := make(map[Finding]bool, len(findings))
	out := make([]Finding, 0, len(findings))
	for _, f := range findings {
		if seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// fingerprintFindings hashes the identity of a finding set so a later run can
// recognise it in an existing issue. It covers the service (all controllers file
// into one repo) and each finding's Class name, Kind, Subject and Evidence, but
// not Detail, so prose and version changes do not churn issues. Callers pass only
// actionable findings.
//
// The hashed bytes are a wire format stored in open issues; changing them
// re-files every issue. TestFingerprintFormatLock pins it.
func fingerprintFindings(service string, findings []Finding) string {
	lines := make([]string, 0, len(findings))
	for _, f := range findings {
		lines = append(lines, fmt.Sprintf("%q|%q|%q|%q", f.Class.String(), f.Kind, f.Subject, f.Evidence))
	}
	sort.Strings(lines)

	h := sha256.New()
	fmt.Fprintf(h, "%q\n", service)
	for _, line := range lines {
		fmt.Fprintln(h, line)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// parseFingerprint returns the fingerprint embedded in an issue body, or "".
//
// With exactly one well-formed generated region the marker is read from inside it,
// so a pasted marker elsewhere cannot win. Otherwise the first marker in the body
// wins, since the bot's region starts at byte 0. This keeps a damaged body
// attributed to its service (avoiding a duplicate) even though
// replaceGeneratedRegion refuses to rewrite it.
func parseFingerprint(body string) string {
	if start, end, shape := generatedRegion(body); shape == regionUnique {
		body = body[start:end]
	}
	match := fingerprintRE.FindStringSubmatch(body)
	if match == nil {
		return ""
	}
	return match[1]
}

// regionShape is what generatedRegion found.
type regionShape int

const (
	// regionAbsent: neither marker appears in the body.
	regionAbsent regionShape = iota
	// regionUnique: exactly one begin and one end marker, each on its own line,
	// begin first.
	regionUnique
	// regionMalformed: any other arrangement.
	regionMalformed
)

// generatedRegion returns the half-open byte range from the begin marker through
// the end marker. Anything but regionUnique is reported rather than guessed at:
// refusing to refresh is safer than rewriting a maintainer's text. Raw marker
// occurrences are counted, so a marker in prose also makes the body malformed.
// Marker lines may have trailing whitespace or \r, as for fingerprintRE.
func generatedRegion(body string) (start, end int, shape regionShape) {
	begins := strings.Count(body, generatedRegionBegin)
	ends := strings.Count(body, generatedRegionEnd)
	if begins == 0 && ends == 0 {
		return 0, 0, regionAbsent
	}
	if begins != 1 || ends != 1 {
		return 0, 0, regionMalformed
	}
	start = strings.Index(body, generatedRegionBegin)
	endAt := strings.Index(body, generatedRegionEnd)
	if endAt < start ||
		!isCompleteLine(body, start, len(generatedRegionBegin)) ||
		!isCompleteLine(body, endAt, len(generatedRegionEnd)) {
		return 0, 0, regionMalformed
	}
	return start, endAt + len(generatedRegionEnd), regionUnique
}

// isCompleteLine reports whether body[at:at+n] is the whole of its line, give or
// take trailing spaces, tabs and a \r.
func isCompleteLine(body string, at, n int) bool {
	if at > 0 && body[at-1] != '\n' {
		return false
	}
	rest := body[at+n:]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[:nl]
	}
	return strings.Trim(rest, " \t\r") == ""
}

// errUnmanageableRegion is wrapped by replaceGeneratedRegion when a body has no
// single well-formed generated region to replace.
var errUnmanageableRegion = errors.New("issue body has no single well-formed generated region")

// replaceGeneratedRegion substitutes newRegion for the generated region of an
// existing body, preserving every byte outside it so a maintainer's text survives.
// There is deliberately no whole-body fallback: a body without exactly one
// well-formed region is refused with errUnmanageableRegion and left for a human.
func replaceGeneratedRegion(existingBody, newRegion string) (string, error) {
	start, end, shape := generatedRegion(existingBody)
	switch shape {
	case regionAbsent:
		return "", fmt.Errorf("%w: neither marker appears in it", errUnmanageableRegion)
	case regionMalformed:
		return "", fmt.Errorf("%w: expected exactly one %s line followed by exactly one %s line",
			errUnmanageableRegion, generatedRegionBegin, generatedRegionEnd)
	}
	// The existing suffix already has the newline after the end marker; trimming
	// newRegion's keeps an identical replacement a no-op.
	return existingBody[:start] + strings.TrimSuffix(newRegion, "\n") + existingBody[end:], nil
}

// githubMaxIssueBody is GitHub's limit on an issue body; exceeding it is a 422.
const githubMaxIssueBody = 65536

// comparedVersionsRE reads back the versions issueFooter wrote.
var comparedVersionsRE = regexp.MustCompile(`\nCompared aws-sdk-go-v2 (\S+) -> (\S+)\n`)

// parseComparedVersions returns the versions an issue's footer names.
func parseComparedVersions(body string) (baseline, latest string, ok bool) {
	m := comparedVersionsRE.FindStringSubmatch(body)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// issueFooter ends every generated region: the comparison line, then the
// fingerprint marker. It is a function so issueBodyBudget can reserve exactly its
// length; truncation must never reach the marker.
func issueFooter(baselineVersion, latestVersion, fingerprint string) string {
	return fmt.Sprintf("---\nCompared aws-sdk-go-v2 %s -> %s\n%s%s -->\n",
		baselineVersion, latestVersion, fingerprintPrefix, fingerprint)
}

// issueBodyBudget is how many bytes the blocks may spend: the GitHub limit, less
// one byte, the footer (measured, since version strings are unbounded), the end
// marker, and the overflow summary at its largest possible counts. The begin
// marker and intro are written first, so b.Len() already charges them.
//
// If the fixed parts alone exceed the limit nothing fits; that needs ~32KB
// version strings, which SDK tags never are.
func issueBodyBudget(
	baselineVersion, latestVersion, fingerprint string,
	resourceBlocks, findingCount int,
) int {
	return githubMaxIssueBody - 1 -
		len(issueFooter(baselineVersion, latestVersion, fingerprint)) -
		len(overflowSummary(resourceBlocks, findingCount)) -
		len(generatedRegionEnd) - len("\n")
}

// overflowSummary states the total truncation cost across the body, or "" when
// nothing was dropped. The resource count is named only when non-zero.
func overflowSummary(omittedResources, omittedFindings int) string {
	switch {
	case omittedResources > 0:
		return fmt.Sprintf("_%d further resource(s) and %d finding(s) omitted to keep this issue body within GitHub's size limit._\n\n",
			omittedResources, omittedFindings)
	case omittedFindings > 0:
		return fmt.Sprintf("_%d further finding(s) omitted to keep this issue body within GitHub's size limit._\n\n",
			omittedFindings)
	}
	return ""
}

// resourceSection pairs a finding class with its heading in a resource's block.
// resourceSections is in rendering order.
type resourceSection struct {
	class  FindingClass
	header string
}

var resourceSections = []resourceSection{
	{ClassNewResource, "New resource"},
	{ClassTransientResource, "Transient resource (manual review)"},
	{ClassPossibleResource, "Possible new resource (manual review)"},
	{ClassSpecField, "Spec field candidates"},
	{ClassStatusField, "Status field candidates"},
	{ClassLifecycleField, "Lifecycle and request-only fields (manual review)"},
	{ClassNewOperation, "Related new operations"},
}

// renderIssueBody renders the issue body, wrapped in the generated-region markers,
// and returns it with its fingerprint. Findings are grouped by resource, with a
// catch-all block for the rest; the body is capped at githubMaxIssueBody.
//
// The fingerprint is returned because findings are deduped first, so hashing the
// caller's raw slice would not match the embedded marker.
func renderIssueBody(
	service, baselineVersion, latestVersion string,
	findings []Finding,
) (string, string) {
	findings = dedupeFindings(findings)
	// The appendix (pre-existing and dropped findings) is measured here to size
	// its reserve, then rendered after the findings.
	all := findings
	appendixReserve := min(maxAppendixReserve,
		len(preexistingBlock(all, baselineVersion, math.MaxInt))+len(droppedBlock(all, math.MaxInt)))
	findings = reportable(findings)

	// Hashed before truncation, so an overflowing body keeps a stable identity.
	fingerprint := fingerprintFindings(service, actionable(findings))

	byKind := map[string][]Finding{}
	for _, f := range findings {
		byKind[f.Kind] = append(byKind[f.Kind], f)
	}

	kinds := make([]string, 0, len(byKind))
	for kind := range byKind {
		if kind != "" {
			kinds = append(kinds, kind)
		}
	}
	sort.Strings(kinds)

	// One block per resource, then the catch-all, which is not counted as a
	// resource in the overflow summary.
	resourceBlocks := len(kinds)
	blocks := make([]issueBlock, 0, resourceBlocks+1)
	for _, kind := range kinds {
		blocks = append(blocks, buildResourceBlock(kind, byKind[kind]))
	}
	blocks = append(blocks, buildCatchAllBlock(findings))

	// The findings hold back at most maxAppendixReserve for the appendix, which
	// gets whatever they leave and truncates itself to fit.
	budget := issueBodyBudget(baselineVersion, latestVersion, fingerprint,
		resourceBlocks, len(findings))
	findingsBudget := budget - appendixReserve

	var b strings.Builder
	b.WriteString(generatedRegionBegin + "\n")
	fmt.Fprintf(&b, "AWS SDK releases since %s, the version the `%s` controller builds against, add "+
		"resources and fields the controller does not represent. These are candidate additions for "+
		"maintainer review; not every item is necessarily appropriate for the CRD API.\n\n",
		baselineVersion, service)

	// Each block takes what it can of the remaining budget. A block too large
	// renders partially rather than being dropped, and reserves[i] holds back
	// what the blocks after it need to render something, so one oversized block
	// cannot starve the rest.
	reserves := blockReserves(blocks)

	omittedResources, omittedFindings := 0, 0
	for i, blk := range blocks {
		remaining := findingsBudget - b.Len()
		text, omitted := blk.render(remaining, min(reserves[i], remaining/2))
		if text == "" && omitted > 0 && i < resourceBlocks {
			// Nothing of this resource block fit. An empty block omits nothing
			// and is not counted.
			omittedResources++
		}
		b.WriteString(text)
		omittedFindings += omitted
	}
	// budget already reserves the overflow summary.
	room := budget - b.Len()
	preexisting := preexistingBlock(all, baselineVersion, room)
	dropped := droppedBlock(all, room-len(preexisting))
	b.WriteString(overflowSummary(omittedResources, omittedFindings))
	b.WriteString(preexisting)
	b.WriteString(dropped)
	b.WriteString(issueFooter(baselineVersion, latestVersion, fingerprint))
	b.WriteString(generatedRegionEnd + "\n")

	return b.String(), fingerprint
}

// droppedBlock renders new dropped operations and fields as a collapsed list with
// reasons, or "" when there are none, so a wrong drop can be seen and challenged.
// The result is at most limit bytes.
func droppedBlock(findings []Finding, limit int) string {
	var ops, fields []Finding
	for _, f := range findings {
		if !f.NewSincePin {
			continue
		}
		switch f.Class {
		case ClassDroppedOperation:
			ops = append(ops, f)
		case ClassDroppedField:
			fields = append(fields, f)
		}
	}
	if len(ops)+len(fields) == 0 {
		return ""
	}
	sort.Slice(ops, func(i, j int) bool { return lessFinding(ops[i], ops[j]) })
	sort.Slice(fields, func(i, j int) bool {
		if fields[i].Kind != fields[j].Kind {
			return fields[i].Kind < fields[j].Kind
		}
		return lessFinding(fields[i], fields[j])
	})

	var counts []string
	if len(ops) > 0 {
		counts = append(counts, plural(len(ops), "new operation", "new operations"))
	}
	if len(fields) > 0 {
		counts = append(counts, plural(len(fields), "new field", "new fields"))
	}
	head := fmt.Sprintf("<details>\n<summary>%s not reported, each with the reason</summary>\n\n",
		strings.Join(counts, " and "))
	entries := make([]string, 0, len(ops)+len(fields))
	for _, f := range ops {
		entries = append(entries, fmt.Sprintf("- `%s` — %s\n", f.Subject, f.Detail))
	}
	for _, f := range fields {
		entries = append(entries, fmt.Sprintf("- `%s` on %s, from %s — %s\n",
			f.Subject, f.Kind, quoteOps(f.evidenceOps()), f.Detail))
	}
	return collapsedList(head, entries, len(entries), limit)
}

// maxAppendixReserve is the most the findings hold back for the appendix
// (preexistingBlock and droppedBlock), so overflowing findings cannot crowd it out.
const maxAppendixReserve = 8 << 10

// collapsedList closes a "<details>" block opened by head, listing as many of
// entries as fit within limit bytes, and at most maxEntries of them, with a
// "_and N more_" line for the rest. It returns "" when not even head, that line
// and the closing tag fit.
func collapsedList(head string, entries []string, maxEntries, limit int) string {
	const tail = "\n</details>\n\n"
	more := func(n int) string { return fmt.Sprintf("- _and %d more_\n", n) }

	var b strings.Builder
	b.WriteString(head)
	for i, e := range entries {
		rest := len(entries) - i
		// Writing e must still leave room to say what follows it.
		need := len(e) + len(tail)
		if rest > 1 {
			need += len(more(rest - 1))
		}
		if i == maxEntries || b.Len()+need > limit {
			b.WriteString(more(rest))
			break
		}
		b.WriteString(e)
	}
	b.WriteString(tail)
	if b.Len() > limit {
		return ""
	}
	return b.String()
}

// maxPreexistingEntries bounds preexistingBlock by count, on top of its byte limit.
const maxPreexistingEntries = 50

// preexistingLabels names each class in preexistingBlock's flat list.
var preexistingLabels = map[FindingClass]string{
	ClassNewResource:       "resource",
	ClassTransientResource: "transient resource",
	ClassPossibleResource:  "possible resource",
	ClassNewOperation:      "operation",
	ClassUnknownOperation:  "unclassified operation",
	ClassSpecField:         "Spec field",
	ClassStatusField:       "Status field",
	ClassLifecycleField:    "lifecycle field",
}

// preexistingBlock renders, collapsed, the candidates already in the SDK release
// the controller builds against, or "" when there are none. They are real gaps but
// not news, so they do not count (see reportable). At most limit bytes.
func preexistingBlock(findings []Finding, baselineVersion string, limit int) string {
	var old []Finding
	for _, f := range findings {
		if !f.NewSincePin && !f.Class.isDropped() {
			old = append(old, f)
		}
	}
	if len(old) == 0 {
		return ""
	}
	sort.Slice(old, func(i, j int) bool {
		if old[i].Kind != old[j].Kind {
			return old[i].Kind < old[j].Kind
		}
		return lessFinding(old[i], old[j])
	})

	head := fmt.Sprintf("<details>\n<summary>%s already in %s and missing from the controller</summary>\n\n",
		plural(len(old), "candidate", "candidates"), baselineVersion) +
		"These predate the SDK release the controller builds against, so they do not drive this notification.\n\n"
	entries := make([]string, 0, len(old))
	for _, f := range old {
		where := ""
		if f.Kind != "" && f.Kind != f.Subject {
			where = f.Kind + " "
		}
		entry := fmt.Sprintf("- %s%s `%s`", where, preexistingLabels[f.Class], f.Subject)
		if ops := f.evidenceOps(); len(ops) > 0 {
			entry += fmt.Sprintf(" — %s", quoteOps(ops))
		}
		entries = append(entries, entry+"\n")
	}
	return collapsedList(head, entries, maxPreexistingEntries, limit)
}

// plural renders a count with the singular or plural noun to match.
func plural(n int, singular, pluralForm string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, pluralForm)
}

// issueBlock is one "## " heading's worth of the body, held as sections and
// entries so truncation can drop them independently.
type issueBlock struct {
	// heading is the "## ..." line without its newline.
	heading  string
	sections []issueSection
}

// issueSection is one "### " heading and the entries beneath it.
type issueSection struct {
	// heading is the "### ..." line without its newline.
	heading string
	entries []issueEntry
}

// issueEntry is one bullet and anything nested under it, with the number of
// findings it stands for, which truncation charges when dropping it.
type issueEntry struct {
	// text is newline-terminated and may span several lines.
	text     string
	findings int
}

// blockReserves returns, for each block, the bytes the blocks after it need to
// render something. Callers clamp these to a fraction of the remaining budget,
// since with many blocks the sum exceeds the cap.
func blockReserves(blocks []issueBlock) []int {
	reserves := make([]int, len(blocks))
	behind := 0
	for i := len(blocks) - 1; i >= 0; i-- {
		reserves[i] = behind
		behind += blocks[i].minRenderSize()
	}
	return reserves
}

// minRenderSize is the fewest bytes in which the block can say something: its
// heading, the first non-empty section's heading and first entry, the closing
// blank line, and the omission note. A block with no entries needs 0.
func (blk issueBlock) minRenderSize() int {
	for _, section := range blk.sections {
		if len(section.entries) == 0 {
			continue
		}
		total := blk.entryCount()
		return len(blk.heading) + 2 +
			len(section.heading) + 1 +
			len(section.entries[0].text) + 1 +
			len(blockOmissionNote(total, total))
	}
	return 0
}

// render emits as much of the block as fits, returning the markdown and the number
// of findings omitted. It writes up to budget bytes when the whole block fits,
// otherwise up to budget-reserve. It returns "" when not even a heading and one
// entry fit; the omitted count distinguishes that from an empty block.
func (blk issueBlock) render(budget, reserve int) (string, int) {
	if blk.entryCount() == 0 {
		return "", 0
	}
	if whole := blk.renderWhole(); len(whole) <= budget {
		return whole, 0
	}

	// The omission note is charged up front at its largest, so appending it
	// cannot push the block over budget.
	totalEntries := blk.entryCount()
	avail := budget - reserve - len(blockOmissionNote(totalEntries, totalEntries))

	var b strings.Builder
	omittedEntries, omittedFindings := 0, 0
	for _, section := range blk.sections {
		if len(section.entries) == 0 {
			continue
		}
		// Headings are written only once an entry is known to fit. The +1 is the
		// blank line that closes the section.
		prefix := section.heading + "\n"
		if b.Len() == 0 {
			prefix = blk.heading + "\n\n" + prefix
		}
		if b.Len()+len(prefix)+len(section.entries[0].text)+1 > avail {
			omittedEntries += len(section.entries)
			omittedFindings += findingCount(section.entries)
			continue
		}

		b.WriteString(prefix)
		for i, entry := range section.entries {
			if b.Len()+len(entry.text)+1 > avail {
				omittedEntries += len(section.entries) - i
				omittedFindings += findingCount(section.entries[i:])
				break
			}
			b.WriteString(entry.text)
		}
		b.WriteString("\n")
	}

	if b.Len() == 0 {
		return "", blk.findingCount()
	}
	b.WriteString(blockOmissionNote(omittedEntries, totalEntries))
	return b.String(), omittedFindings
}

// renderWhole renders the block untruncated, skipping empty sections.
func (blk issueBlock) renderWhole() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", blk.heading)
	for _, section := range blk.sections {
		if len(section.entries) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s\n", section.heading)
		for _, entry := range section.entries {
			b.WriteString(entry.text)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// entryCount is the number of bullets the block would render.
func (blk issueBlock) entryCount() int {
	n := 0
	for _, section := range blk.sections {
		n += len(section.entries)
	}
	return n
}

// findingCount is the number of findings the block stands for.
func (blk issueBlock) findingCount() int {
	n := 0
	for _, section := range blk.sections {
		n += findingCount(section.entries)
	}
	return n
}

func findingCount(entries []issueEntry) int {
	n := 0
	for _, entry := range entries {
		n += entry.findings
	}
	return n
}

// blockOmissionNote states how many entries truncation dropped from one block.
func blockOmissionNote(omitted, total int) string {
	return fmt.Sprintf("_%d of %d entries under this heading omitted to keep this issue body within GitHub's size limit._\n\n",
		omitted, total)
}

// writeBullet writes a finding's bullet line without the trailing newline. A
// field's operations follow on the same line; a resource's go on a nested line
// (see buildResourceBlock).
func writeBullet(text *strings.Builder, f Finding) {
	fmt.Fprintf(text, "- `%s`", f.Subject)
	if f.Detail != "" {
		fmt.Fprintf(text, " — %s", f.Detail)
	}
	if f.SetBy != "" || f.ReadBy != "" || f.ReturnedBy != "" {
		var roles []string
		for _, role := range []struct{ label, ops string }{
			{"set by", f.SetBy}, {"read back by", f.ReadBy}, {"in the response of", f.ReturnedBy},
		} {
			if role.ops != "" {
				roles = append(roles, role.label+" "+quoteRoleOps(role.ops))
			}
		}
		fmt.Fprintf(text, " — %s", strings.Join(roles, "; "))
		return
	}
	if !isResourceClass(f.Class) && f.Evidence != "" {
		fmt.Fprintf(text, " — %s", quoteOps(f.evidenceOps()))
	}
}

// quoteRoleOps renders a SetBy, ReadBy or ReturnedBy value: `Op`, or `Op` (as
// `Path`) when the operation carries the field at another path.
func quoteRoleOps(ops string) string {
	parts := strings.Split(ops, ",")
	for i, part := range parts {
		op, path, nested := strings.Cut(part, "=")
		parts[i] = "`" + op + "`"
		if nested {
			parts[i] += " (as `" + path + "`)"
		}
	}
	return strings.Join(parts, ", ")
}

// isResourceClass reports whether a class is about a whole resource, whose
// Evidence is the operations that would implement it.
func isResourceClass(c FindingClass) bool {
	return c == ClassNewResource || c == ClassPossibleResource || c == ClassTransientResource
}

// buildResourceBlock decomposes one resource's findings into a block, leaving out
// empty sections. Findings no section places are left to buildCatchAllBlock.
func buildResourceBlock(kind string, findings []Finding) issueBlock {
	blk := issueBlock{heading: fmt.Sprintf("## Resource: %s", kind)}
	for _, section := range resourceSections {
		items := filterFindings(findings, section.class)
		if len(items) == 0 {
			continue
		}

		entries := make([]issueEntry, 0, len(items))
		for _, f := range items {
			var text strings.Builder
			writeBullet(&text, f)
			text.WriteString("\n")
			if isResourceClass(f.Class) && f.Evidence != "" {
				fmt.Fprintf(&text, "  - Operations: %s\n", quoteOps(f.evidenceOps()))
			}
			entries = append(entries, issueEntry{text: text.String(), findings: 1})
		}

		blk.sections = append(blk.sections, issueSection{
			heading: fmt.Sprintf("### %s", section.header),
			entries: entries,
		})
	}
	return blk
}

// buildCatchAllBlock collects every finding no resource block places, so each
// finding the fingerprint covers also appears in the body.
func buildCatchAllBlock(findings []Finding) issueBlock {
	unplaced := make([]Finding, 0, len(findings))
	for _, f := range findings {
		placed := f.Kind != "" && slices.ContainsFunc(resourceSections,
			func(s resourceSection) bool { return s.class == f.Class })
		if !placed {
			unplaced = append(unplaced, f)
		}
	}
	if len(unplaced) == 0 {
		return issueBlock{}
	}
	sort.Slice(unplaced, func(i, j int) bool { return lessFinding(unplaced[i], unplaced[j]) })

	section := issueSection{heading: "### Unclassified operations"}
	for _, f := range unplaced {
		var text strings.Builder
		writeBullet(&text, f)
		// A finding with a Kind lands here only when its class has no section.
		if f.Kind != "" {
			fmt.Fprintf(&text, " (kind `%s`)", f.Kind)
		}
		text.WriteString("\n")
		section.entries = append(section.entries, issueEntry{text: text.String(), findings: 1})
	}
	return issueBlock{heading: "## Unattributed", sections: []issueSection{section}}
}

// filterFindings returns the findings of one class, ordered by lessFinding so the
// body is reproducible despite producers walking maps.
func filterFindings(findings []Finding, class FindingClass) []Finding {
	var out []Finding
	for _, f := range findings {
		if f.Class == class {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return lessFinding(out[i], out[j]) })
	return out
}

// lessFinding orders findings by Subject, then Detail, Class, Kind, Evidence and
// NewSincePin. The order must be total: sort.Slice is unstable, and ties would
// reorder the body between runs without changing the fingerprint.
func lessFinding(a, b Finding) bool {
	if a.Subject != b.Subject {
		return a.Subject < b.Subject
	}
	if a.Detail != b.Detail {
		return a.Detail < b.Detail
	}
	if a.Class != b.Class {
		return a.Class < b.Class
	}
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	if a.Evidence != b.Evidence {
		return a.Evidence < b.Evidence
	}
	return !a.NewSincePin && b.NewSincePin
}

// issueOutcome is what reconcileIssue decided to do about one service's issue.
// Values are explicit literals so a collision is visible at the declaration.
type issueOutcome int

const (
	// issueOutcomeNone is the zero value: no decision was reached. Every error
	// return from reconcileIssue uses it, so a non-zero outcome always means a
	// completed decision.
	issueOutcomeNone issueOutcome = 0
	// issueSuppressedByClosed means a maintainer closed an issue carrying this
	// exact finding set, so it is deliberately not being re-filed.
	issueSuppressedByClosed issueOutcome = 1
	// issueUnchanged means no action was needed.
	issueUnchanged issueOutcome = 2
	// issueCreated means a new issue was filed.
	issueCreated issueOutcome = 3
	// issueUpdated means an existing issue's body was refreshed.
	issueUpdated issueOutcome = 4
	// issueSkippedAtCap means an issue should have been filed but the
	// open-issue cap was already reached.
	issueSkippedAtCap issueOutcome = 5
	// issueStaleOpenIssue means an open issue exists for a service with no
	// current findings. Nothing is written, but the caller logs it because the
	// issue holds a cap slot.
	issueStaleOpenIssue issueOutcome = 6
)

// String names an outcome for the log. Unlike FindingClass.String, these names
// are not hashed and can be reworded freely.
func (o issueOutcome) String() string {
	switch o {
	case issueOutcomeNone:
		return "no-decision"
	case issueSuppressedByClosed:
		return "suppressed-by-closed-issue"
	case issueUnchanged:
		return "unchanged"
	case issueCreated:
		return "created"
	case issueUpdated:
		return "updated"
	case issueSkippedAtCap:
		return "skipped-at-cap"
	case issueStaleOpenIssue:
		return "stale-open-issue"
	}
	return fmt.Sprintf("unnamed-outcome-%d", int(o))
}

// reconcileIssue brings the GitHub issue for one service in line with the
// findings.
//
// existing is this service's open issue, or nil. Before a refresh the caller must
// pass a fresh copy (refetchManagedIssue): bodies are merged from existing's, and
// a stale one drops a maintainer's recent edits.
//
// closedFingerprints are the fingerprints of this service's closed issues. A
// matching finding set is not re-filed: closing is how a maintainer dismisses it.
// A new change yields a new fingerprint and still files.
//
// maxOpen is the cap on open issues, which must be positive, and gates creation
// only. openCount is owned by the caller, which must increment it on every
// issueCreated and on errors wrapping errIssueCreateIndeterminate.
//
// Every error return carries issueOutcomeNone. An error wrapping
// errCannotLabelIssues must abort the whole run.
//
// dryRun suppresses only the three GitHub writes; every decision still runs. A dry
// run cannot show whether the token can label issues or whether writes would
// succeed.
func reconcileIssue(
	ctx context.Context,
	client *github.Client,
	owner, repo string,
	service, baselineVersion, latestVersion string,
	findings []Finding,
	existing *github.Issue,
	closedFingerprints map[string]bool,
	maxOpen int,
	openCount int,
	dryRun bool,
) (issueOutcome, error) {
	// Rejected rather than read as "no cap", so a missing flag cannot silently
	// remove the only limit on filing.
	if maxOpen <= 0 {
		return issueOutcomeNone, fmt.Errorf(
			"open-issue cap must be positive, got %d; 0 would mean unlimited filing", maxOpen)
	}

	if len(actionable(findings)) == 0 {
		// Nothing actionable, so nothing is written, not even to close an open
		// issue: zero findings can also come from a detector bug or an empty model
		// fetch, and a wrongly closed issue goes unnoticed. A stale open issue is
		// reported instead.
		if existing != nil {
			return issueStaleOpenIssue, nil
		}
		return issueUnchanged, nil
	}

	// Use the embedded fingerprint, which is computed after deduplication.
	body, want := renderIssueBody(service, baselineVersion, latestVersion, findings)

	if existing == nil {
		// Consulted only when nothing is open: an open issue takes precedence.
		if closedFingerprints[want] {
			return issueSuppressedByClosed, nil
		}
		if openCount >= maxOpen {
			return issueSkippedAtCap, nil
		}
		// Fires only when the body's fixed parts cannot fit (see issueBodyBudget).
		if len(body) > githubMaxIssueBody {
			return issueOutcomeNone, fmt.Errorf(
				"filing an issue for %s would produce a %d-byte body, over GitHub's %d-byte limit",
				service, len(body), githubMaxIssueBody)
		}
		title := fmt.Sprintf("AWS API changes detected for %s", service)
		// apiChangeLabel is the ownership marker the next run's listing keys on,
		// and it exempts these issues from the org's stale/rotten/close jobs (an
		// automatic close would suppress current findings). `kind/api-change` is
		// the triage label humans filter by; anyone can add it, so it cannot mark
		// ownership.
		labels := []string{
			apiChangeLabel,
			"kind/api-change",
			fmt.Sprintf("service/%s", service),
			defaultProwAutoGenLabel,
		}
		if !dryRun {
			if _, err := createGithubIssueWithClient(ctx, client, owner, repo, title, body, labels); err != nil {
				// The issue may still have been filed: the label check runs after
				// the POST, and a lost response wraps errIssueCreateIndeterminate.
				return issueOutcomeNone, err
			}
		}
		return issueCreated, nil
	}

	number := existing.GetNumber()

	// Same finding set, but the rendered text may have changed (e.g. reworded
	// details), so rewrite the body silently, without a comment. The comparison
	// re-renders at the versions the issue already names, so a new SDK release
	// alone does not rewrite every open issue.
	if parseFingerprint(existing.GetBody()) == want {
		oldBaseline, oldLatest, ok := parseComparedVersions(existing.GetBody())
		if !ok {
			return issueUnchanged, nil
		}
		asFiled, _ := renderIssueBody(service, oldBaseline, oldLatest, findings)
		// A body whose region cannot be located is refused, not read as unchanged.
		reread, err := replaceGeneratedRegion(existing.GetBody(), asFiled)
		if err != nil {
			return issueOutcomeNone, fmt.Errorf("not rewording issue %s/%s#%d: %w",
				owner, repo, number, err)
		}
		if reread == existing.GetBody() {
			return issueUnchanged, nil
		}
		merged, err := replaceGeneratedRegion(existing.GetBody(), body)
		if err != nil {
			return issueOutcomeNone, fmt.Errorf("not rewording issue %s/%s#%d: %w",
				owner, repo, number, err)
		}
		if len(merged) > githubMaxIssueBody {
			return issueOutcomeNone, fmt.Errorf(
				"rewording issue %s/%s#%d would produce a %d-byte body, over GitHub's %d-byte limit",
				owner, repo, number, len(merged), githubMaxIssueBody)
		}
		if !dryRun {
			if err := updateGithubIssueBody(ctx, client, owner, repo, number, merged); err != nil {
				return issueOutcomeNone, err
			}
		}
		return issueUpdated, nil
	}

	// Merge and size-check before commenting: a merged body that is permanently
	// over the limit (maintainer text outside the region counts) or has damaged
	// markers would otherwise get a comment every run for a refresh that never
	// lands. There is no fallback to replacing the whole body, which would delete
	// the maintainer's text.
	merged, err := replaceGeneratedRegion(existing.GetBody(), body)
	if err != nil {
		return issueOutcomeNone, fmt.Errorf("not refreshing issue %s/%s#%d: %w",
			owner, repo, number, err)
	}
	if len(merged) > githubMaxIssueBody {
		return issueOutcomeNone, fmt.Errorf(
			"refreshing issue %s/%s#%d would produce a %d-byte body, over GitHub's %d-byte limit; "+
				"the generated report plus the text outside it no longer fit",
			owner, repo, number, len(merged), githubMaxIssueBody)
	}

	// Comment first, then rewrite the body: the body's fingerprint is the
	// done-marker, so a failed comment leaves it stale and the next run retries
	// both. The worst case is a duplicate comment rather than a silent change.
	//
	// The comment states no counts, since a truncated body cannot give the old
	// one, nor whether the set grew or shrank. It uses the future tense because
	// the PATCH can still fail.
	comment := fmt.Sprintf(
		"The detected API change set for `%s` has changed. The report in this issue's "+
			"body will be refreshed, comparing aws-sdk-go-v2 %s -> %s.",
		service, baselineVersion, latestVersion,
	)
	// Both writes are gated together, so a preview never comments without patching.
	if !dryRun {
		if err := commentOnGithubIssue(ctx, client, owner, repo, number, comment); err != nil {
			return issueOutcomeNone, err
		}

		if err := updateGithubIssueBody(ctx, client, owner, repo, number, merged); err != nil {
			return issueOutcomeNone, err
		}
	}
	return issueUpdated, nil
}

// getAPINotificationServices reads api_notification_services from
// jobs_config.yaml in file order, so cap-limited runs are deterministic, and
// api_notification_max_open_issues (0 if omitted), which only feeds a warning
// when it disagrees with --max-open-issues.
//
// The list is validated here because the running job reads the hand-maintained
// file directly, bypassing `make prow-gen`.
func getAPINotificationServices(configPath string) ([]string, int, error) {
	fileData, err := os.ReadFile(configPath)
	if err != nil {
		return nil, 0, fmt.Errorf("unable to read %s: %s", configPath, err)
	}

	var config *generator.JobsConfig
	if err := yaml.Unmarshal(fileData, &config); err != nil {
		return nil, 0, fmt.Errorf("unable to unmarshal %s: %s", configPath, err)
	}
	if config == nil {
		return nil, 0, fmt.Errorf("%s parsed to nothing; it is empty or contains only comments", configPath)
	}
	if err := generator.ValidateAPINotificationServices(
		config.APINotificationServices, config.AWSServices,
	); err != nil {
		return nil, 0, fmt.Errorf("%s is not usable: %w", configPath, err)
	}
	return config.APINotificationServices, config.APINotificationMaxOpenIssues, nil
}
