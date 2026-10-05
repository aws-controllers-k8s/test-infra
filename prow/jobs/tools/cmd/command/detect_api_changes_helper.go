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
	"fmt"
	"io"
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
)

// tagPrefixFor returns the git ref prefix to search for the newest tag. An
// empty packageName means the core aws-sdk-go-v2 tag series; otherwise the
// per-service series, which is named after the SDK service package.
func tagPrefixFor(packageName string) string {
	if packageName == "" {
		return "tags/v"
	}
	return fmt.Sprintf("tags/service/%s/", packageName)
}

// newestSemverRef picks the highest non-prerelease semver from a list of git
// refs. Refs whose last path segment is not parseable semver are skipped, so
// stray or malformed tags cannot derail resolution.
func newestSemverRef(refs []string) (string, error) {
	var newest string
	var newestVer semver.Version

	for _, ref := range refs {
		version := ref[strings.LastIndex(ref, "/")+1:]
		if strings.Contains(version, "-") {
			// Prerelease, e.g. v1.44.0-rc.1.
			continue
		}

		// The leading `v` must go before parsing. aquasecurity/go-version's
		// regex is anchored `^(major)\.` with no tolerance for a prefix, so
		// `v1.44.0` does not parse at all. Every real aws-sdk-go-v2 tag carries
		// that prefix, so parsing the raw string would make every candidate
		// unparseable and this function would return "no usable semver tags"
		// for every service on every run.
		//
		// Keep the original, prefixed spelling in `newest`: that is what callers
		// need for building model URLs and for reporting the compared versions.
		parsed, err := semver.Parse(strings.TrimPrefix(version, "v"))
		if err != nil {
			// Not parseable semver; a stray tag must not derail resolution.
			continue
		}

		if newest == "" || parsed.GreaterThan(newestVer) {
			newest, newestVer = version, parsed
		}
	}

	if newest == "" {
		return "", fmt.Errorf("no usable semver tags among %d refs", len(refs))
	}
	return newest, nil
}

// resolveLatestSDKVersion returns the newest published per-service tag for a
// service, whichever series the controller pins.
//
// The per-service series even for a controller pinned to a core version: the core
// tag moves only when the core module changes, so the model at the newest core tag
// can be releases behind the newest model for the service, and a report against it
// misses whatever AWS has shipped since. A per-service tag is cut whenever the
// service's model changes, so the model at its newest tag is the newest there is.
func resolveLatestSDKVersion(
	ctx context.Context,
	client *github.Client,
	packageName string,
) (string, error) {
	prefix := tagPrefixFor(packageName)

	opts := &github.ReferenceListOptions{ListOptions: github.ListOptions{PerPage: 100}}
	var refs []string
	for {
		page, resp, err := client.Git.ListMatchingRefs(ctx, sdkRepoOwner, sdkRepoName, &github.ReferenceListOptions{
			Ref:         prefix,
			ListOptions: opts.ListOptions,
		})
		if err != nil {
			return "", fmt.Errorf("unable to list refs %q: %s", prefix, err)
		}
		for _, ref := range page {
			refs = append(refs, ref.GetRef())
		}
		if resp.NextPage == 0 {
			break
		}
		opts.ListOptions.Page = resp.NextPage
	}

	return newestSemverRef(refsAtPrefixDepth(refs, prefix))
}

// latestVersionCache memoises resolveLatestSDKVersion for the duration of one run.
//
// fetchModel caches models on disk precisely so that a baseline shared between
// services is fetched once, but resolution had no such cache: every service re-ran a
// full paginated ListMatchingRefs over the same core tag series, roughly 200+
// redundant requests at ~74 services. Those count against the 5,000/hr core limit
// rather than the 30/min search limit, so they will not 403 the way a per-service
// issue search did — but the same rationale applies, and one map removes them.
//
// Keyed by tag series. Every series is per-service now — see resolveLatestSDKVersion —
// so this saves a listing only when two controllers share an SDK package; it is kept
// for the memoised failures below. A plain map with no mutex is
// deliberate — the reconcile loop is sequential, and making this concurrency-safe
// would imply a concurrency this job does not have.
//
// Failures are memoised too. Caching an error is not caching a wrong answer: no version
// is ever invented and no diff is ever run against one, and every service still records
// its own analysis failure — the only thing shared is the one upstream request that
// established the series cannot be listed, instead of ~74 of them. That matters most in
// exactly the likeliest persistent failure of this call, a rate-limit or
// secondary-rate-limit 403, where re-listing 73 more times is actively
// counterproductive. Retrying within a run cannot help either: a single job invocation
// is far shorter than a rate-limit window, and the next periodic run starts with a fresh
// cache.
type latestVersionCache struct {
	resolved map[string]latestVersionResult
}

// latestVersionResult is one series' answer, which is either a version or the error that
// prevented there being one.
type latestVersionResult struct {
	version string
	err     error
}

func newLatestVersionCache() *latestVersionCache {
	return &latestVersionCache{resolved: map[string]latestVersionResult{}}
}

// resolve returns the newest published per-service tag for a service, consulting the
// cache first. The cache key is exactly the ref prefix that would have been listed.
func (c *latestVersionCache) resolve(
	ctx context.Context,
	client *github.Client,
	packageName string,
) (string, error) {
	series := tagPrefixFor(packageName)
	if result, ok := c.resolved[series]; ok {
		return result.version, result.err
	}

	version, err := resolveLatestSDKVersion(ctx, client, packageName)
	if err != nil {
		c.resolved[series] = latestVersionResult{err: err}
		return "", err
	}
	c.resolved[series] = latestVersionResult{version: version}
	return version, nil
}

// refsAtPrefixDepth keeps only refs whose remainder after the prefix is a single
// path segment, discarding tags for nested sub-modules.
//
// The GitHub matching-refs filter is textual, not path-aware, so the prefix
// `tags/service/s3/` also matches `refs/tags/service/s3/internal/configtesting/
// v0.1.0`. newestSemverRef reads the last path segment, so such a tag would be
// treated as a candidate version for the s3 service module itself. Those nested
// versions happen to be low today and lose the comparison, but nothing
// guarantees that — a sub-module that ever outran its parent would silently
// resolve as the service's "latest", and we would then diff against a model that
// does not exist at that tag.
func refsAtPrefixDepth(refs []string, prefix string) []string {
	full := "refs/" + prefix
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		remainder := strings.TrimPrefix(ref, full)
		if remainder == ref {
			// Did not actually carry the prefix.
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
// once per run.
func fetchModel(
	ctx context.Context,
	cacheDir string,
	modelName, packageName, coreVersion, serviceVersion string,
) (*SmithyModel, error) {
	url := modelURL(modelName, packageName, coreVersion, serviceVersion)

	// Qualify the cache path by series. The core and per-service tag series are
	// independent counters, so core v1.41.5 and service/<pkg>/v1.41.5 are
	// entirely different models that would otherwise land on the same path. A
	// single service uses one series throughout a run, so this is unreachable
	// today — but --model-cache-dir exists precisely so the cache can persist
	// across runs, and a controller that ever switches pinning style would then
	// be served the wrong model from cache. Two lines to foreclose the class.
	//
	// filepath.Base guards the two path segments that come from a controller's
	// generator.yaml. filepath.Join collapses ".." lexically rather than
	// confining to cacheDir, so a stray `package_name: ../../..` would write
	// outside the cache directory. Both values are documented as single
	// segments, so this costs nothing.
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

// modelHTTPClient is this package's own client rather than http.DefaultClient,
// so that model fetching does not share transport state with anything else in
// the process, and so there is a seam for future customisation.
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
		// Drain before returning so the connection can be reused. A 404 is a
		// legitimate outcome here — a per-service module tag can exist without a
		// model file at that ref — so this path is not vanishingly rare.
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("GET %s returned %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// FindingClass categorises a detected change by the kind of work it implies.
type FindingClass int

const (
	// ClassNewResource is a resource codegen would add a CRD for today.
	ClassNewResource FindingClass = iota
	// ClassNewOperation is an operation on a resource that already has a CRD,
	// which the controller does not call. Operations on a resource the report
	// lists as new are folded into that resource's Evidence instead: see
	// foldOperationsIntoResources.
	ClassNewOperation
	// ClassUnknownOperation is an operation whose name does not classify, and
	// which therefore needs human judgement.
	ClassUnknownOperation
	// ClassSpecField is a new member that could be desired state: it is set at
	// Create, or set at Update and readable back. See fieldRoles.class.
	ClassSpecField
	// ClassPossibleResource is a resource the model manages without a Create
	// verb — PutAsset with GetAsset, RegisterCapability with
	// DeregisterCapability — so codegen cannot infer it without an
	// `operations:` override.
	ClassPossibleResource
	// ClassDroppedOperation is an operation placeUnknownOp judged to be nothing
	// ACK could model, with the reason as its Detail. It is carried only so the
	// issue can show what was left out: it is never a finding in its own right.
	// See reportable for everywhere that matters.
	ClassDroppedOperation
	// ClassStatusField is a new member only ever returned, which could expose
	// observed state.
	ClassStatusField
	// ClassLifecycleField is a new member only sent on Update or Delete and never
	// returned — ec2's QuoteId, AcceptModificationTerms, ApplyCancellationCharges.
	// It parameterises one call rather than describing the resource, so it needs
	// a maintainer to decide whether it belongs in the CRD at all.
	ClassLifecycleField
	// ClassDroppedField is a new member that only appears on a read request —
	// ec2's IncludeManagedResources on DescribeInstances. It shapes what the
	// controller reads back, not the resource, and like ClassDroppedOperation it
	// is listed for transparency only.
	ClassDroppedField
	// ClassTransientResource is a resource codegen would add a CRD for, but which
	// has no Delete operation, so AWS expires or consumes it — ec2's
	// CapacityReservationCancellationQuote. It needs a maintainer to decide.
	ClassTransientResource
)

// isDropped reports whether a class is shown only in the issue's collapsed
// "not reported" list.
func (c FindingClass) isDropped() bool {
	return c == ClassDroppedOperation || c == ClassDroppedField
}

// reportable returns the findings that drive the notification: new since the
// controller's SDK release, and not dropped. The others are shown in the issue for
// transparency and nothing else, so everything that decides what to do — whether
// there is anything to report, how many findings there are, the fingerprint — goes
// through here. A dropped operation counting as a finding would file an issue for a
// service whose only change is a new StartFlowCapture; a pre-existing gap counting
// would file one for a controller no SDK release has touched.
func reportable(findings []Finding) []Finding {
	out := make([]Finding, 0, len(findings))
	for _, f := range findings {
		if !f.Class.isDropped() && f.NewSincePin {
			out = append(out, f)
		}
	}
	return out
}

// markPreexisting clears NewSincePin on every finding the release model already
// has. release is the model at the service module version the controller's go.mod
// requires; nil leaves findings as the producers set them.
//
// The producers compare against the model the controller was generated from, which
// is right for "is this a gap": codegen saw that model and nothing newer. It is
// wrong for "is this new": ec2-controller was generated from core v1.41.1 but
// builds against service/ec2 v1.290.1, and SecondaryNetwork, already in v1.290.1,
// was reported as an SDK addition.
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
			// Only its own operations decide: the generic read Evidence also names,
			// DescribeTransitGatewayAttachments, is far older than the attachment.
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
// path, spelled as findAddedFields keys it: relative to the response wrapper when
// codegen unwraps one.
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
		if !side.isOutput {
			continue
		}
		if wrapper := outputWrapper(m, in, opID, side.ref); wrapper != "" {
			if _, ok := members[wrapper+"."+path]; ok {
				return true
			}
		}
	}
	return false
}

// String returns the stable name of a finding class. This name is hashed into
// the issue fingerprint, so these strings are a wire format — renaming one
// invalidates every fingerprint in every open issue. Adding a class is safe;
// renaming or reusing a name is not.
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
	// Kind is the resource kind the finding belongs to, or "" when it is not
	// attributable to one resource.
	Kind string
	// Class categorises the finding.
	Class FindingClass
	// Subject is the operation name, resource name, or member path the finding
	// is about.
	Subject string
	// Detail is human-readable supporting text, or "" when the section heading
	// already says everything a detail would. Each bullet used to repeat its
	// heading — "not present in the CRD" under "Added fields" — which read as
	// noise across a body of dozens of fields.
	Detail string
	// NewSincePin records whether the subject is new since the SDK release the
	// controller builds against, as opposed to something already in that release
	// that the controller has never covered. Only new findings drive the
	// notification; the rest are listed in a collapsed section. See
	// markPreexisting.
	NewSincePin bool
	// Evidence is the sorted, comma-separated operations that support the
	// finding: where a field appeared, or which operations manage a new
	// resource. Unlike Detail it is hashed into the fingerprint, so an issue
	// refreshes when, say, AWS adds a Delete operation to a resource it lists.
	// A string rather than a slice so that Finding stays comparable.
	Evidence string
	// SetBy, ReadBy and ReturnedBy say what each operation does with a field:
	// sends it, returns it from a read (durable read-back), or returns it from any
	// other call's response. Listing CreateTable, DeleteTable, DescribeTable and
	// UpdateTable together read as though every one of them sets the field. Each
	// is in Evidence's form; a ReadBy or ReturnedBy entry is `Op=Path` when the
	// operation returns the field somewhere other than its own path. They cover
	// the controller's own operations too, so are not hashed. See annotateFields.
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

// namesIgnoredResource reports whether an operation name mentions a resource the
// controller explicitly declines to generate.
//
// Without this, producer 2 reports permanent noise. Measured against the real s3
// controller on a zero-change diff: 16 operations land in the unclassified
// bucket, and almost all of them — AbortMultipartUpload, CopyObject,
// PutObjectAcl, RestoreObject, SelectObjectContent, WriteGetObjectResponse — are
// operations on `Object` and `MultipartUpload`, both of which s3's
// generator.yaml lists under ignore.resource_names. They would reappear in the
// issue on every run forever, which defeats the point of a bucket meant for
// operations that actually need human judgement.
//
// A substring match is the right test here rather than the classified resource
// name, because these operations classify to OpTypeUnknown precisely because
// their verb is unrecognised — so the classifier hands back the whole operation
// ID, not a resource. `AbortMultipartUpload` has no usable resource name, but it
// plainly concerns MultipartUpload.
//
// Except where the operation names a longer resource the report knows about.
// ec2 ignores the broad `Ipam`, and the same report lists the new
// IpamInternetRegistryAssociation; the bare substring test then hid
// EnableIpamInternetRegistryAssociation, which is that new resource's own
// operation. known holds the lowercased names of every CRD kind and reported
// resource; nil means none.
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

// findNewResources reports resources that codegen would generate a CRD for
// today but which the controller does not have. Because the inference mirrors
// codegen's own rule, this is not a guess: it is exactly the set of CRDs a
// regeneration would add.
func findNewResources(latest, baseline *SmithyModel, in *ControllerInputs) []Finding {
	declared := in.Config.ResourceNames()
	ignored := in.Config.Ignore.ResourceNames

	// Resource names the baseline model already implied a CRD for. NewSincePin
	// must be a fact about the resource, not about whichever operation happened
	// to sort first.
	//
	// A resource can be implied by more than one Create operation — a
	// generator.yaml `operations:` override can bind an oddly-named operation to
	// a resource with operation_type: [Create], alongside a conventional
	// Create<X>. Deriving the flag from the first operation encountered would
	// then report "new" or "not new" depending on alphabetical order, which can
	// flip as AWS adds operations.
	baselineResources := createResourceNames(baseline, in, declared)
	opsByResource := operationsByResource(latest, in)

	var findings []Finding
	seen := map[string]bool{}
	for _, opID := range latest.OperationNames() {
		opType, resName := in.ClassifyOpWithOverrides(opID, declared)

		// OpTypeCreate exactly, not a "create family". code-generator emits a
		// CRD only for resources carrying an OpTypeCreate operation
		// (pkg/model/model.go:128 builds crdNameKeys from opMap[OpTypeCreate]
		// alone). OpTypeCreateBatch — which is where a plural Create<X>s and
		// every BatchCreate<X> land — and OpTypeReplace produce no CRD, so
		// counting them here would report resources codegen would never
		// generate, on every run, forever.
		if opType != OpTypeCreate {
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
		// A resource nothing can delete is one AWS expires or consumes: ec2's
		// CapacityReservationCancellationQuote lives 24 hours and is spent by
		// CancelCapacityReservation. Codegen would still generate a CRD for it, but
		// a CRD whose object cannot be deleted is rarely what anyone wants.
		if !slices.ContainsFunc(ops, func(op string) bool {
			opType, _ := in.ClassifyOpWithOverrides(op, declared)
			return opType == OpTypeDelete
		}) {
			f.Class = ClassTransientResource
			f.Detail = fmt.Sprintf("implied by `%s`, but nothing deletes it: AWS expires or consumes it", opID)
		}
		findings = append(findings, f)
	}
	return findings
}

// operationsByResource maps each lowercased resource name to the operations that
// classify onto it — the implementation context a maintainer needs for a resource
// the report lists as new: `CreateSecondaryNetwork`, `DeleteSecondaryNetwork`,
// `DescribeSecondaryNetworks`. Tag operations and ignore.operations are left out,
// as everywhere else.
func operationsByResource(m *SmithyModel, in *ControllerInputs) map[string][]string {
	declared := in.Config.ResourceNames()
	out := map[string][]string{}
	for _, opID := range m.OperationNames() {
		if isDenylistedOp(opID) || slices.Contains(in.Config.Ignore.Operations, opID) {
			continue
		}
		if opType, resName := in.ClassifyOpWithOverrides(opID, declared); opType != OpTypeUnknown {
			low := strings.ToLower(resName)
			out[low] = append(out[low], opID)
		}
	}
	return out
}

// foldOperationsIntoResources moves operations placed on a resource the report
// lists as new, or as possibly new, into that resource's Evidence. They are how
// the resource would be implemented, not a separate gap: AcceptDelegationRequest
// belongs beside the new DelegationRequest, not in a list of its own.
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
		if opType, resName := in.ClassifyOpWithOverrides(opID, declared); opType == OpTypeCreate {
			out[resName] = true
		}
	}
	return out
}

// findNewOperations reports operations the controller does not call. Ones that
// classify onto an existing CRD are ClassNewOperation. Operations that imply a
// resource with no CRD are skipped, because findNewResources already reports that
// resource and reporting both would double-count the same gap.
//
// Operations whose verb does not classify go through placeUnknownOp: onto a
// resource as ClassNewOperation, existing or new; as a ClassPossibleResource for a
// resource with no Create verb; dropped; or, failing every rule, reported as
// ClassUnknownOperation.
func findNewOperations(latest, baseline *SmithyModel, in *ControllerInputs) []Finding {
	declared := in.Config.ResourceNames()
	used := allUsedOps(in.UsedOps)

	// Every resource the report knows about, for placeUnknownOp: CRD kinds, and the
	// resources findNewResources reports, so that AcceptDelegationRequest lands
	// beside the new DelegationRequest rather than in a separate list.
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

		opType, resName := in.ClassifyOpWithOverrides(opID, declared)
		_, inBaseline := baseline.Operation(opID)
		canonicalKind, hasCRD := in.CanonicalKind(resName)
		_, knownResource := resources[strings.ToLower(resName)]

		switch {
		// A classified operation is placed like an unknown one when the name it
		// classifies to is no resource the report knows. Skipping those, as this
		// once did, silently lost every operation on a sub-object:
		// ModifyVpcEndpointPayerResponsibility classifies to a
		// VpcEndpointPayerResponsibility that does not exist, but configures
		// VPCEndpoint; DescribeApplicationStatus reads instances. Create operations
		// stay with findNewResources, and an operation on a known resource is
		// either the hasCRD arm's or already in that resource's Evidence.
		case opType == OpTypeUnknown || (opType != OpTypeCreate && !knownResource):
			// The ignored-resource filter belongs here, inside the Unknown arm,
			// and must NOT be hoisted above the classification call.
			//
			// It is a substring test, which is only safe for operations that
			// carry no usable resource name — exactly the Unknown case, where
			// the classifier hands back the whole operation ID. Applied before
			// classification it silently deletes real findings whenever an
			// ignored name is a substring of a managed one. Two present-day
			// collisions: ec2 ignores the bare name `Route` while managing a
			// `RouteTable` CRD, so every RouteTable operation would vanish; and
			// organizations ignores `Organization` while managing
			// `OrganizationalUnit`. The hasCRD arm below has an exact resource
			// name from the classifier and needs no substring guessing.
			if ignored := ignoredNameIn(opID, in.Config.Ignore.ResourceNames, resources); ignored != "" {
				// Listed, not silently skipped, when new: ec2's new
				// DescribeAccountVpcEncryptionControl vanished from the report
				// without a word, and a reader cannot tell a deliberate ignore from
				// a miss.
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

			// Temporal, not structural — unlike every other producer. An
			// operation whose verb we cannot classify and which has been in the
			// model since before the controller's pin is not news: ACK has
			// already implicitly declined to model it, and regeneration would do
			// nothing with it either way. Only a newly appeared unclassifiable
			// operation warrants a human look.
			//
			// This matters at scale. Measured on a zero-change diff: reporting
			// every unclassifiable operation structurally yields 108 entries for
			// ec2 and 3 for s3, all permanent. ec2 has hundreds of operations
			// against 20 modelled resources, so the bucket meant for human
			// judgement would be almost entirely noise, on every run, forever.
			if inBaseline {
				// An old status read the controller does not call can still return
				// new state: DescribeInstanceStatus now carries ApplicationStatus for
				// each instance. Only a read of the resource's status counts — other
				// old reads placed on a resource are about something else
				// (cloudwatchlogs' DescribeQueries reads queries, not log groups).
				if isReadOp(opType, opID) {
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
				findings = append(findings, newOperationFinding(latest, name, opID, opType))
				if existing[name] {
					findings = append(findings, setterFieldCandidates(latest, in, name, opID, opType)...)
					if isReadOp(opType, opID) {
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
			// Temporal, for the same reason as the Unknown arm above. An
			// operation that was already in the model at the controller's pin
			// and still is not called has been declined, not missed: on the live
			// sns run every finding here was a years-old List* operation
			// (ListTopics, ListSubscriptions, ListPlatformApplications), which
			// ACK controllers do not call by design. Reporting those files an
			// issue that no regeneration can ever resolve.
			if inBaseline {
				continue
			}
			// Report the CRD's own casing, not the AWS spelling.
			findings = append(findings, newOperationFinding(latest, canonicalKind, opID, opType))
			findings = append(findings, setterFieldCandidates(latest, in, canonicalKind, opID, opType)...)
			if isReadOp(opType, opID) {
				findings = append(findings, readStatusCandidates(latest, baseline, in, canonicalKind, opID)...)
			}
		}
	}

	// One finding per possible resource, naming every operation that manages it —
	// RegisterCapability and DeregisterCapability are one Capability. Operations
	// that do classify count too: TransitGatewayClientVpnAttachment is placed by
	// Accept and Reject, but DeleteTransitGatewayClientVpnAttachment is how it
	// would be deleted.
	opsByResource := operationsByResource(latest, in)
	for name, opIDs := range possible {
		// Only reads is no resource a controller could manage: networkfirewall's
		// DescribeFlowOperation and ListFlowOperations report on flow captures that
		// StartFlowCapture runs.
		allOps := append(slices.Clone(opIDs), opsByResource[strings.ToLower(name)]...)
		if !slices.ContainsFunc(allOps, func(opID string) bool {
			opType, _ := in.ClassifyOpWithOverrides(opID, declared)
			return !isReadOp(opType, opID)
		}) {
			for _, opID := range opIDs {
				findings = append(findings, Finding{
					Class: ClassDroppedOperation, Subject: opID, Detail: dropQuery, NewSincePin: true,
				})
			}
			continue
		}
		// Nor is one nothing can read, here or through a broader resource: after a
		// restart a controller could not recover its state. networkfirewall's
		// NetworkFirewallTransitGatewayAttachment has only Accept, Reject and Delete.
		reads := genericReads(latest, name, opIDs)
		if !slices.ContainsFunc(append(slices.Clone(allOps), reads...), func(opID string) bool {
			opType, _ := in.ClassifyOpWithOverrides(opID, declared)
			return isReadOp(opType, opID)
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

// genericReads returns the read operations of the broader resource a possible
// resource's operations address it as. ec2's AcceptTransitGatewayClientVpnAttachment
// takes a TransitGatewayAttachmentId, and there is no
// DescribeTransitGatewayClientVpnAttachments: the attachment is read through
// DescribeTransitGatewayAttachments, with the other attachment types.
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

// readStatusCandidates returns the members a read operation's response gained
// since the generation model, as Status candidates for the resource the read is
// placed on. The controller does not call the read, so findAddedFields never sees
// it: ec2's Instance gains application health only through DescribeInstanceStatus
// (`InstanceStatuses.ApplicationStatus`) and the new DescribeApplicationStatus.
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
		// A new read is a candidate only when its response is one thing —
		// DescribeApplicationStatus returns ApplicationStatuses. A response of
		// many scalars, like networkfirewall's GetAnalysisReportResults, describes
		// a report, not the resource, and listing each member was noise; the read
		// itself is still listed as a related operation.
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

// isGeneratedHistory reports whether a new read pages through items nothing sets.
// networkfirewall's ListAnalysisReports pages through the reports StartAnalysisReport
// produces, which are a history, not Firewall state. cloudwatchlogs'
// ListSyslogConfigurations pages too, but PutSyslogConfiguration sets what it
// lists, so those are desired state.
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

// secondaryReadDetail marks a Status candidate only a read the controller does
// not call returns. Such a read usually answers for many resources at once —
// DescribeInstanceStatus lists instances — so the field needs a custom read hook
// that calls it and picks out this resource, not only regeneration.
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

// newOperationFinding is a new operation on a resource. A read says so, because
// for an existing CRD its response is where Status fields would come from:
// DescribeApplicationStatus on Instance.
func newOperationFinding(m *SmithyModel, kind, opID string, opType OpType) Finding {
	f := Finding{Kind: kind, Class: ClassNewOperation, Subject: opID, NewSincePin: true}
	if isReadOp(opType, opID) {
		f.Detail = "read: its response could back Status fields"
		if op, ok := m.Operation(opID); ok && op.Output != nil && isGeneratedHistory(m, op) {
			f.Detail = "read: pages through a history the service generates, so it is context for a " +
				"bounded summary at most, not a source of Status fields"
		}
	}
	return f
}

// isReadOp reports whether an operation reads, by its classification or, for one
// that does not classify, its verb.
func isReadOp(opType OpType, opID string) bool {
	switch opType {
	case OpTypeGet, OpTypeList, OpTypeGetAttributes:
		return true
	}
	return slices.ContainsFunc(readVerbs, func(v string) bool { return strings.HasPrefix(opID, v) })
}

// requestPlumbingMembers are request members that qualify how a call is made, not
// what it sets: every s3 Put/Update carries the first three.
var requestPlumbingMembers = []string{
	"ChecksumAlgorithm", "ContentMD5", "ExpectedBucketOwner", "RequestPayer",
}

// setterFieldCandidates returns the request members of a new operation that sets
// part of an existing resource.
//
// A dedicated setter exists to configure exactly what its request carries, so its
// members are desired state even though they are never sent to the resource's own
// Create or Update. They are Spec candidates when a read returns them under the
// same name, which is what plain field reconciliation needs. Otherwise they are
// manual review: ModifyVpcEndpointPayerResponsibility sets one Scope and
// PayerResponsibility at a time, while reads return a PayerResponsibilities
// collection, so reconciling it takes custom code. The resource's own identifier
// and members that never carry state are left out, as is anything the CRD already
// exposes.
func setterFieldCandidates(m *SmithyModel, in *ControllerInputs, kind, opID string, opType OpType) []Finding {
	setter := opType == OpTypeUpdate || opType == OpTypeSetAttributes ||
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
// Top level only: VPCEndpoint's reads do return a Scope, but inside each entry of
// PayerResponsibilities, and a field set on its own and read back from a list
// still needs custom reconciliation.
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
// operations under. Both sides go through normalizeResourceKey, because ACK's
// package directories are snake_case while kinds are PascalCase with uppercased
// acronyms — see that function's comment for why lowercasing alone is wrong.
func kindToResourceDir(kind string) string {
	return normalizeResourceKey(kind)
}

// exposedInCRD reports whether an AWS member path is already surfaced by a
// resource's CRD. The AWS path's leading segment is resolved through the
// resource's generator.yaml renames, then every segment is lowercased, because
// CRD properties are lowerCamelCase while AWS members are UpperCamel.
func exposedInCRD(in *ControllerInputs, kind, awsPath string) bool {
	fields, ok := in.CRDFields[kind]
	if !ok {
		return false
	}

	segments := strings.Split(awsPath, ".")
	if renamed, ok := in.Config.RenamesForResource(kind)[segments[0]]; ok {
		segments[0] = renamed
	}
	for i, segment := range segments {
		segments[i] = strings.ToLower(segment)
	}
	return fields[strings.Join(segments, ".")]
}

// declinedFieldPath reports whether a member path is one the controller's
// generator.yaml tells codegen to skip, or a descendant of one.
//
// These paths are absent from the CRD *because ACK excluded them*, which is the
// opposite of a gap — reporting them says "you are missing this" about a
// deliberate decision. s3 declines four, and the omission was not theoretical:
// `CreateBucketConfiguration.Tags` plus its two children accounted for three of
// producer 3's findings on the real s3 delta, and each survived hand-verification
// because the check asked "is it in the CRD?" rather than "was it declined?".
//
// Descendants must go too. Declining `CreateBucketConfiguration.Tags` implicitly
// declines `...Tags.Key` and `...Tags.Value`, which codegen never emits either.
//
// An entry is shape-qualified, as codegen reads it: the first segment names a
// shape and the rest is a member path inside that shape — ec2's
// `CreateCapacityReservationInput.DryRun`, `CapacityReservation.CapacityBlockId`.
// So it is matched against the shape that contains each segment of awsPath, not
// against awsPath itself, which is relative to the operation's request or response
// and never carries the root shape's name. That s3's entries matched the relative
// path was a coincidence of its members being named after their shapes. The root
// shape answers to its codegen name, `<Operation>Input` or `<Operation>Output`, as
// well as its Smithy one, `<Operation>Request`.
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

// elementShape resolves a list, however deeply nested, to the shape of its
// elements: the shape whose members a path segment under it names.
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

// declinedShapeName reports whether a member's target shape is one the
// controller tells codegen to skip by name.
//
// This is a fourth suppression mechanism, distinct from ignore.field_paths: it
// keys on the *target shape's* name rather than the path, so it applies wherever
// that shape is referenced. s3 declares `BlockedEncryptionTypes`, which reaches
// producer 3 via two operations and accounted for four findings.
//
// targetShapeID is the absolute Smithy ID recorded on MemberInfo, so compare
// only its short name.
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
// hangs from, targets a declined shape.
//
// Checking only the member's own target is not enough. Declining
// `BlockedEncryptionTypes` removes that member, and codegen therefore emits
// nothing beneath it either — but the child `...BlockedEncryptionTypes.
// EncryptionType` has its own target (`EncryptionTypeList`), which is not itself
// declined, so a self-only check lets the child through. That happened on the
// real s3 delta: three of the four BlockedEncryptionTypes findings vanished and
// the one child survived.
//
// This needs no ancestor bookkeeping in the walk, because WalkMembers records
// every intermediate path — so each prefix can be looked up and its own target
// tested directly. Doing it here rather than by tracking declined prefixes
// during iteration also means it holds when the parent is not itself a candidate
// (already present in the baseline) while the child is new.
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

// sourcedAsCRDField reports whether a member is already exposed by the CRD under
// a different name, because generator.yaml sources a field from it.
//
// A fifth mechanism, and the subtlest: `resources.<Kind>.fields.<Name>.from`
// pulls a field out of another operation's shape entirely. s3's Bucket sources
// `abac` from PutBucketAbac's `AbacStatus`, so both PutBucketAbac and
// GetBucketAbac report AbacStatus as absent unless this is consulted.
//
// The declared path is matched exactly and as a prefix, since sourcing
// `AbacStatus` also brings its children.
func sourcedAsCRDField(in *ControllerInputs, kind, opName, path string) bool {
	res, ok := in.Config.Resources[kind]
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
// wires onto the common ACK status field.
//
// Unlike the other suppressions this one is not declared anywhere in
// generator.yaml — it is a codegen convention. A Create or List response member
// named `Arn` or `<Kind>Arn` is assigned to
// `Status.ACKResourceMetadata.ARN`, which every ACK CRD exposes. The generated
// s3 code does exactly that with `resp.BucketArn`. So the member *is* surfaced,
// just not under a field of its own name, and no config we read says so.
//
// Being a heuristic rather than a declaration, keep it narrow: match only the
// final path segment, and only the two spellings codegen actually recognises.
func isACKManagedARN(kind, path string) bool {
	segments := strings.Split(path, ".")
	last := strings.ToLower(segments[len(segments)-1])
	return last == "arn" || last == strings.ToLower(kind)+"arn"
}

// findAddedFields reports members added to operations the controller calls
// that the resource's CRD does not expose. Members the CRD already exposes are
// suppressed: codegen picked those up, so there is nothing to notify.
//
// One finding per new field per resource, not per (operation, path). A new field
// normally appears in every operation that returns the resource, and a new
// structure brings all of its children with it, so the per-path listing repeated
// itself: iam's one new SourceRoleTemplate was 12 bullets on Role alone, and
// dynamodb's 137 findings were 19 fields. The Subject is the member path, and the
// Evidence names the operations it appeared in.
//
// Each field is classed by how those operations use it — see fieldRoles.class —
// because a field a maintainer would add to Spec, one for Status, and a request
// option of a single call are different pieces of work, and some are no work.
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
			opType, _ := in.ClassifyOpWithOverrides(opName, declared)

			for _, side := range []struct {
				latestRef   *SmithyMemberRef
				baselineRef *SmithyMemberRef
				// isOutput gates the ARN suppression, which only describes what
				// codegen does with a *response* member.
				isOutput bool
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

				wrapper := ""
				if side.isOutput {
					wrapper = outputWrapper(latest, in, opName, side.latestRef)
				}
				rootNames := fieldRootNames(opName, side.latestRef, side.isOutput)

				for _, path := range paths {
					if _, existed := baselineMembers[path]; existed {
						continue
					}
					// A child of a field that is itself new is part of that
					// field's finding, whether it is reported or suppressed:
					// every suppression below that applies to the parent applies
					// to its descendants too.
					if hasNewAncestor(path, latestMembers, baselineMembers) {
						continue
					}
					if exposedInCRD(in, kind, path) {
						continue
					}
					if underDeclinedParent(in, kind, opName, path, wrapper, baselineMembers) {
						continue
					}
					// Absent from the CRD is not the same as missing. ACK has
					// five separate ways of surfacing or declining a member, and
					// a finding is only real if none of them applies — see each
					// predicate's own comment for the mechanism it encodes and
					// the evidence behind it.
					if declinedFieldPath(latest, rootNames, path, latestMembers, in.Config.Ignore.FieldPaths) {
						continue
					}
					if declinedShapeAncestor(path, latestMembers, in.Config.Ignore.ShapeNames) {
						continue
					}
					if sourcedAsCRDField(in, kind, opName, path) {
						continue
					}
					// Output side only. The convention this encodes is that
					// codegen assigns a *response* ARN onto the shared status
					// field; it says nothing about request members. An input
					// member literally named `Arn` or `<Kind>Arn` is a real
					// unmodelled field and must still be reported.
					if side.isOutput && isACKManagedARN(kind, path) {
						continue
					}
					// Keyed by the path as the CRD would see it, so one field
					// reached through different wrappers is one finding: dynamodb's
					// VectorIndexes arrives as `Table.VectorIndexes` from
					// DescribeTable, `TableDescription.VectorIndexes` from
					// Create/Update/DeleteTable, and bare `VectorIndexes` on the
					// CreateTable request. A wrapper the CRD itself models, as s3's
					// Bucket does objectLockConfiguration, stays in the path.
					key := path
					if wrapper != "" && !exposedInCRD(in, kind, wrapper) {
						key = unwrapped(path, wrapper)
					}
					// Input and Output of one operation can both carry the path;
					// name the operation once.
					if !slices.Contains(opsByPath[key], opName) {
						opsByPath[key] = append(opsByPath[key], opName)
					}
					if rolesByPath[key] == nil {
						rolesByPath[key] = &fieldRoles{}
					}
					rolesByPath[key].add(opType, side.isOutput)
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
}

func (r *fieldRoles) add(opType OpType, isOutput bool) {
	switch {
	case isOutput:
		r.output = true
	case opType == OpTypeCreate || opType == OpTypeCreateBatch:
		r.createInput = true
	case opType == OpTypeGet || opType == OpTypeList || opType == OpTypeGetAttributes:
		r.readInput = true
	case opType == OpTypeDelete:
		r.deleteInput = true
	default:
		// Update, Replace, SetAttributes, and the custom operations hooks call —
		// s3's PutBucketVersioning is how Bucket's versioning is set.
		r.updateInput = true
	}
}

// class says what kind of candidate the field is.
//
// Spec is what can be set and stays set: sent at Create, or sent at Update and
// returned again. Status is what is only ever returned. A field sent only on an
// Update or Delete, and never returned, is a parameter of that one call — ec2's
// CapacityReservation QuoteId, AcceptModificationTerms and ApplyCancellationCharges —
// and needs a maintainer to say whether it belongs in the CRD. One that appears only
// on a read request — ec2's IncludeManagedResources on DescribeInstances — is a
// filter on what the controller reads, and is dropped.
func (r *fieldRoles) class() FindingClass {
	switch {
	case r.createInput || (r.updateInput && r.output):
		return ClassSpecField
	case r.output:
		return ClassStatusField
	case r.updateInput || r.deleteInput:
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
// response onto a resource, or "" when it reads the response as-is. This mirrors
// code-generator's SetResource: the operation's configured
// output_wrapper_field_path wins; otherwise a response whose only member is a
// structure is unwrapped.
//
// Without this, every member of a wrapped response carries the wrapper as its
// leading segment — `Role.Arn` from CreateRole — while the CRD field codegen
// generates for it is `arn`, so no pre-existing parent inside a wrapped response
// could be recognised as exposed. underDeclinedParent is the only consumer: a
// member new since the pin cannot already be in a CRD generated at the pin.
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

// unwrapped strips wrapper from the front of an output member path, returning
// the path as codegen sees it. A path that is not under wrapper is returned
// unchanged.
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
// field only as part of its parent's type, so when the parent was left out — by
// a deliberate decline, or because it was never modelled — the new child cannot
// appear either, and reporting it is a finding no regeneration can resolve.
//
// Observed live on iam: InstanceProfile's responses carry `Roles`, the list of
// roles attached to the profile, which the InstanceProfile CRD does not model.
// The new `Role.SourceRoleTemplate` therefore surfaced as three InstanceProfile
// findings as well as the legitimate Role ones.
//
// Only the nearest ancestor that existed at the pin decides. An ancestor that is
// itself new is part of the same finding set; the wrapper is not a CRD field and
// never decides.
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
		// Both spellings, as for the member itself. A single-member response is
		// not always unwrapped in practice: s3's Bucket models the whole
		// GetObjectLockConfiguration response as its own `objectLockConfiguration`
		// field, so the parent of the new DefaultEventHold is exposed as
		// `objectLockConfiguration.rule.defaultRetention`, not as the unwrapped
		// `rule.defaultRetention`. Checking only the unwrapped path dropped that
		// real finding.
		return !exposedInCRD(in, kind, ancestor) &&
			!exposedInCRD(in, kind, unwrapped(ancestor, wrapper)) &&
			!sourcedAsCRDField(in, kind, opName, ancestor)
	}
	return false
}

// refOrNil returns ref only when present is true, so a missing baseline
// operation is treated as having no members rather than panicking.
func refOrNil(present bool, ref *SmithyMemberRef) *SmithyMemberRef {
	if !present {
		return nil
	}
	return ref
}

const fingerprintPrefix = "<!-- ack-api-change-fingerprint: "

// The generated region is everything renderIssueBody wrote, bounded so that a
// maintainer's additions to the same body survive a refresh and so that the
// fingerprint marker has somewhere definite to live.
//
// Before this, updateGithubIssueBody replaced the whole body: a maintainer who
// appended "CreateBucketMetadataTableConfiguration is intentionally unsupported"
// lost it on the next run, and the accompanying comment notified every subscriber
// of the loss. Bounding the region also stops a marker pasted into prose from being
// read as the live one, which used to leave a permanently unmatchable fingerprint
// and an edit-and-comment on that issue every single day.
const (
	generatedRegionBegin = "<!-- ack-api-change-begin -->"
	generatedRegionEnd   = "<!-- ack-api-change-end -->"
)

// fingerprintRE matches the marker on a line of its own, anchored with (?m).
// parseFingerprint applies it to the generated region rather than to the whole
// body, so an identical marker in prose or in a pasted fragment outside the
// region cannot be mistaken for the live one.
//
// The trailing [ \t\r]* is load-bearing rather than defensive. Go's (?m)$ matches
// only directly before \n, and HTML form submission normalises textarea content
// to CRLF, so any issue body a maintainer has edited in the GitHub web UI comes
// back \r\n-terminated. Without this the marker stops matching the moment a human
// touches the issue, and Task 14 files a duplicate on every subsequent run.
var fingerprintRE = regexp.MustCompile(
	`(?m)^` + regexp.QuoteMeta(fingerprintPrefix) + `([0-9a-f]{64}) -->[ \t\r]*$`,
)

// dedupeFindings drops exact duplicates, preserving input order.
//
// Producers 3 and 4 walk an operation's Input and Output shapes independently,
// and WalkMembers paths are relative to each root with no `input.`/`output.`
// prefix, so a member present at the same path on both sides yields two
// identical findings. This is real: on the s3-series v1.32.6 -> v1.41.5 delta,
// lambda emits 16 such pairs from findAddedFields (CapacityProviderConfig and
// its children on CreateFunction/UpdateFunctionConfiguration, LoggingConfig on
// Create/UpdateEventSourceMapping); iam emits one
// (CreateOpenIDConnectProvider -> Tags.Value). s3 and ec2 emit none, which is
// why validating on those two alone did not surface it.
//
// Deduping on the whole struct rather than on (Kind, Class, Subject) keeps this
// lossless: were the two sides' details ever to differ, both findings survive
// and the reader sees two details for one subject, which is honest. Every
// duplicate observed so far carries an identical Detail.
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
// recognise the same set in an already-open issue.
//
// Hashed: the service, and each finding's Class (by name), Kind, Subject and
// Evidence. Evidence is the operations behind a finding, so a resource gaining its
// Delete operation, or a field turning up in one more operation, refreshes the
// issue. The
// service is part of the digest because every controller files into the single
// aws-controllers-k8s/community repo, so the fingerprint is the lookup key
// across ~50 services — without it an empty finding set hashes to the same
// marker for all of them at once.
//
// Deliberately excluded:
//
//   - Detail, which carries version strings and prose. An SDK pin bump that
//     yields the same gaps must not churn the issue.
//   - NewSincePin, which is true of every finding hashed: the caller passes only
//     reportable findings, so pre-existing gaps are not hashed at all and a change
//     among them does not refresh the issue.
//
// The exact bytes fed to sha256 are a wire format: fingerprints live in GitHub
// issue bodies indefinitely, so any change to the layout below makes every open
// issue's marker un-matchable and the next run recreates all of them.
// TestFingerprintFormatLock pins it with a hardcoded digest. Change it only
// deliberately.
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

// parseFingerprint returns the fingerprint embedded in an issue body, or "" when
// the body carries none.
//
// The marker is read from the live generated region — the first one, see
// generatedRegion — and not from the body at large. Scanning the whole body and
// taking the last match meant that a maintainer who pasted another service's report
// into this one — marker included — made the pasted fingerprint the answer for ever,
// so the content comparison never matched again and the job edited and commented on
// that issue daily.
//
// A body with no region is scanned whole, for the same reason replaceGeneratedRegion
// falls back to a whole-body rewrite: an issue filed before the markers existed must
// stay findable, or the run treats it as somebody else's and files a duplicate.
func parseFingerprint(body string) string {
	if start, end, ok := generatedRegion(body); ok {
		body = body[start:end]
	}
	matches := fingerprintRE.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1][1]
}

// generatedRegion locates the first begin marker and the first end marker after
// it, returning the half-open byte range covering both.
//
// First rather than last, because renderIssueBody's region starts at byte 0: the
// first region is the live one and everything below it is human text. Convergence
// is not the discriminator here — parseFingerprint and replaceGeneratedRegion share
// this function, so any self-consistent rule eventually stops editing — what matters
// is which region is authoritative. Simulated over six days with a live s3 region at
// byte 0, a maintainer quoting a complete ec2 region below it, and a genuine new
// finding on day 4: taking the last region rewrote the quoted ec2 report with s3's
// and left the readable region at the top of the issue permanently stale, in two
// edits; taking the first updated the live region and left the quote alone, in one.
//
// Selecting the first is also what stops two other failures. A trailing bare begin
// marker — a maintainer mid-edit, or one quoting the marker — used to make the whole
// body look regionless, and replaceGeneratedRegion then replaced all of it, deleting
// the maintainer's text and sending a notifying comment about it. And a complete but
// fingerprint-less region below the live one used to make parseFingerprint return "",
// which disowned the issue and filed a duplicate.
//
// Two shapes the last-region rule handled better, both needing a maintainer to edit
// into or above the bot's text rather than append below it:
//
//   - A stale region prepended *above* the bot's text. Costs one wrong edit, which
//     then converges — measured, not assumed — rather than a live region left stale
//     for ever, which is what last-region gave us.
//   - A complete marker pair spliced *inside* the live region. The first end after
//     the first begin is then the inner one, so the rewrite spans from the live begin
//     to the inner end and deletes everything between, maintainer text included.
//     Measured at 1466 -> 516 bytes. This one is a wipe, not a convergent edit, so it
//     is the worse of the two — it is accepted only because splicing a marker pair
//     into the middle of the bot's own report is not a thing maintainers do, whereas
//     appending below it is.
//
// So this is not an exhaustive "regions are always recognised" guarantee, and the
// note on replaceGeneratedRegion's fallback should not be read as one.
//
// A begin with no end after it is not a region: half a marker pair is likelier to be
// a maintainer mid-edit or quoting than a region to rewrite.
func generatedRegion(body string) (start, end int, ok bool) {
	start = strings.Index(body, generatedRegionBegin)
	if start < 0 {
		return 0, 0, false
	}
	rel := strings.Index(body[start:], generatedRegionEnd)
	if rel < 0 {
		return 0, 0, false
	}
	return start, start + rel + len(generatedRegionEnd), true
}

// replaceGeneratedRegion substitutes newRegion for the generated region of an
// existing body, preserving every byte outside it.
//
// This is what keeps a maintainer's own text on the issue: the ownership checks in
// github.go establish that the detector filed the issue, not that nobody has edited
// it since, so a whole-body PATCH silently deleted whatever was added.
//
// The whole-body fallback is for issues filed before the markers existed, and for
// nothing else. It is not a "safe reading" of an unrecognised body: applied to a body
// that does contain a region it is the destructive reading, since it deletes the very
// human text this function exists to keep. So the burden is on generatedRegion to
// recognise a region whenever there is one; see its comment for the body shape it used
// to miss, which sent a maintainer's note through this fallback and deleted it.
func replaceGeneratedRegion(existingBody, newRegion string) string {
	start, end, ok := generatedRegion(existingBody)
	if !ok {
		return newRegion
	}
	// newRegion is a rendered body, so it ends in the newline after its end
	// marker; the existing body's suffix already carries that separator. Trimming
	// it makes replacing a body with an identical region a no-op rather than
	// something that grows a blank line per run.
	return existingBody[:start] + strings.TrimSuffix(newRegion, "\n") + existingBody[end:]
}

// githubMaxIssueBody is GitHub's hard limit on an issue body. Exceeding it fails
// the create with a 422.
const githubMaxIssueBody = 65536

// issueFooter is how every generated region ends: the comparison line, then the
// fingerprint marker on a line of its own, with only the region's end marker after
// it. It is one function rather than two inline Fprintf calls so that
// issueBodyBudget can reserve exactly its length — the marker is how Task 14 finds
// this issue again, so it is the one part of the body truncation must never reach.
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

func issueFooter(baselineVersion, latestVersion, fingerprint string) string {
	return fmt.Sprintf("---\nCompared aws-sdk-go-v2 %s -> %s\n%s%s -->\n",
		baselineVersion, latestVersion, fingerprintPrefix, fingerprint)
}

// issueBodyBudget is how many bytes the resource blocks may spend: the GitHub
// limit, less everything renderIssueBody appends after them, less one byte so the
// finished body lands strictly under the limit rather than exactly on it.
//
// Derived rather than a flat reserve, because the footer embeds two
// caller-supplied version strings with no length bound — the earlier flat 2,048
// bytes was really an assumption about how long those strings are. With
// ~1,000-byte versions and 20,000 findings the body reached 65,623 bytes, over the
// limit. Real pins are `v1.41.5`-shaped, so that was theoretical, but measuring
// the footer costs nothing and removes the magic number along with the whole class
// of failure.
//
// resourceBlocks and findingCount are the largest values the overflow summary can
// print, and %d of a smaller non-negative number is never longer, so reserving the
// summary at those counts bounds it whatever gets omitted.
//
// The region's end marker is reserved here too, but its begin marker is not: the
// begin marker is written into the builder before the block loop, so it is already
// charged against the budget through b.Len(), whereas the end marker is appended
// after the loop has stopped looking.
//
// What this cannot save is an input where the parts renderIssueBody always writes
// — the two region markers, the intro line, the summary and the footer — do not
// themselves fit. The budget bounds only what the *blocks* may spend, and the
// begin marker and intro are written unconditionally before the block loop, so
// once the budget falls below their combined length the finished body is those
// plus summary, footer and end marker, and exceeds the limit by exactly
// len(begin marker + intro) - budget. That starts while the budget is still
// positive: measured with equal-length version strings and no findings, at 32,634
// bytes each the footer alone is 65,402 and the budget is +105, yet the body is
// 65,537. With one finding the window opens at 32,583.
//
// No arithmetic here closes it — if the fixed parts do not fit, nothing can be
// rendered that does. Only truncating the caller's version strings would, and a
// mangled comparison line is worse than the 422 it avoids. Real pins are
// `v1.41.5`-shaped and come from SDK git tags, so a ~32KB version is not a thing
// this job can encounter; the point of recording the boundary is that it is the
// fixed parts, not the footer alone, that set it.
func issueBodyBudget(
	baselineVersion, latestVersion, fingerprint string,
	resourceBlocks, findingCount int,
) int {
	return githubMaxIssueBody - 1 -
		len(issueFooter(baselineVersion, latestVersion, fingerprint)) -
		len(overflowSummary(resourceBlocks, findingCount)) -
		len(generatedRegionEnd) - len("\n")
}

// overflowSummary states what the cap cost the body as a whole, or "" when it cost
// nothing. It totals everything dropped, including entries dropped from inside a
// heading that did render and carries its own note, so the full magnitude appears
// in one place — enough for a reader to decide whether to go and read the model
// instead of the issue.
//
// The resource count is named only when there is one. Truncating inside a heading
// that did render drops findings without dropping any resource, and "0 further
// resource(s) and 19198 finding(s) omitted" invites the reader to wonder what the
// zero is for.
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

// resourceSection pairs a finding class with the heading it renders under inside
// a resource's block. The order of resourceSections is the order of the headings
// in the rendered body.
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

// renderIssueBody renders the issue body and returns it along with the
// fingerprint embedded in it. Findings are grouped by resource, with any finding
// the per-resource sections cannot place under a trailing catch-all heading, and
// a footer carrying the compared versions and the fingerprint.
//
// The whole thing is wrapped in the generated-region markers, so the result is both
// a complete body for a new issue and the replacement region for an existing one —
// see replaceGeneratedRegion, which is how a refresh keeps a maintainer's own text.
//
// The fingerprint is returned rather than left for the caller to recompute
// because this function dedupes first: fingerprintFindings(service, findings) on
// the caller's raw slice disagrees with the marker in the returned body whenever
// a producer emitted a duplicate, which is most services. Task 14 compares this
// value against parseFingerprint of an existing issue's body.
//
// The body is capped at githubMaxIssueBody. Measured on the v1.32.6 -> v1.41.5
// delta, s3 renders 3,374 bytes and lambda 16,790, but ec2's 423 findings render
// 48,251 — 74% of the cap on a nine-version delta — so a controller left stale
// for longer will overflow and GitHub will answer 422.
func renderIssueBody(
	service, baselineVersion, latestVersion string,
	findings []Finding,
) (string, string) {
	findings = dedupeFindings(findings)
	// Dropped operations are listed, then set aside before anything is counted or
	// hashed: a change to what is dropped is not one anyone needs notifying of, and
	// fingerprinting them would refresh every issue whenever AWS adds a Start*
	// operation.
	dropped := preexistingBlock(findings, baselineVersion) + droppedBlock(findings)
	findings = reportable(findings)

	// Computed over the whole deduped set, not over whatever survives the cap
	// below: a body that overflows must keep the identity of the findings it was
	// derived from, or an overflowing service would churn its own issue on every
	// run.
	fingerprint := fingerprintFindings(service, findings)

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

	// One block per resource, in heading order, then the catch-all. The count of
	// resource blocks is remembered because the catch-all is not a resource: the
	// overflow summary would otherwise report "1 further resource(s)" for
	// "## Unattributed".
	resourceBlocks := len(kinds)
	blocks := make([]issueBlock, 0, resourceBlocks+1)
	for _, kind := range kinds {
		blocks = append(blocks, buildResourceBlock(kind, byKind[kind]))
	}
	blocks = append(blocks, buildCatchAllBlock(findings))

	// The dropped block is written after the findings, so it is reserved out of
	// the budget like the footer is: findings truncate, it does not.
	budget := issueBodyBudget(baselineVersion, latestVersion, fingerprint,
		resourceBlocks, len(findings)) - len(dropped)

	var b strings.Builder
	// Everything this function writes sits between the region markers, the
	// fingerprint marker included, so that a refresh can rewrite exactly this much
	// of an issue body and leave a maintainer's own additions alone.
	b.WriteString(generatedRegionBegin + "\n")
	fmt.Fprintf(&b, "AWS SDK releases since %s, the version the `%s` controller builds against, add "+
		"resources and fields the controller does not represent. These are candidate additions for "+
		"maintainer review; not every item is necessarily appropriate for the CRD API.\n\n",
		baselineVersion, service)

	// Each block in turn is offered whatever budget is left and takes what it
	// can. Two properties matter here, and each was got wrong once:
	//
	//   - A block too large for the remaining budget renders partially — its
	//     headings and as many entries as fit — rather than being dropped whole.
	//     A service whose findings all sit under one Kind is a single block, and
	//     dropping it produced a 298-byte issue with nothing actionable in it;
	//     worse, the fingerprint covers the full finding set either way, so
	//     Task 14 reported issueUnchanged and that empty issue was never
	//     refreshed.
	//   - A block that does not fit must not stop the ones behind it. One
	//     oversized block used to starve every smaller block after it, leaving
	//     ~63KB of budget unspent. Partial rendering alone does not fix that:
	//     a greedy block would eat the whole budget, so reserves[i] holds back
	//     what the blocks behind block i need to say anything at all.
	reserves := blockReserves(blocks)

	omittedResources, omittedFindings := 0, 0
	for i, blk := range blocks {
		remaining := budget - b.Len()
		text, omitted := blk.render(remaining, min(reserves[i], remaining/2))
		if text == "" && omitted > 0 && i < resourceBlocks {
			// Nothing of this block fit, not even a heading and one entry. An
			// empty block — no section could place its findings — renders as ""
			// with nothing omitted and is not counted. Nor is the catch-all, which
			// is not a resource; its findings are still charged below.
			omittedResources++
		}
		b.WriteString(text)
		omittedFindings += omitted
	}
	// Say what the cap cost rather than silently shortening: a reader has no other
	// way to tell a short list from a truncated one.
	b.WriteString(overflowSummary(omittedResources, omittedFindings))
	b.WriteString(dropped)
	b.WriteString(issueFooter(baselineVersion, latestVersion, fingerprint))
	b.WriteString(generatedRegionEnd + "\n")

	return b.String(), fingerprint
}

// droppedBlock renders the dropped operations and fields as a collapsed list with
// each reason, or "" when there are none. Collapsed because nothing in it needs
// action; listed at all so that a wrong drop — a verb list entry that does not hold
// for some service — can be seen and challenged rather than vanishing.
func droppedBlock(findings []Finding) string {
	var ops, fields []Finding
	for _, f := range findings {
		if !f.NewSincePin {
			// Already in the controller's SDK release: preexistingBlock's, if anyone's.
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
	var b strings.Builder
	fmt.Fprintf(&b, "<details>\n<summary>%s not reported, each with the reason</summary>\n\n",
		strings.Join(counts, " and "))
	for _, f := range ops {
		fmt.Fprintf(&b, "- `%s` — %s\n", f.Subject, f.Detail)
	}
	for _, f := range fields {
		fmt.Fprintf(&b, "- `%s` on %s, from %s — %s\n", f.Subject, f.Kind, quoteOps(f.evidenceOps()), f.Detail)
	}
	b.WriteString("\n</details>\n\n")
	return b.String()
}

// maxPreexistingEntries bounds preexistingBlock, which is written outside the
// truncating budget. A controller far behind its SDK can have hundreds of gaps, and
// none of them is what the notification is about.
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

// preexistingBlock renders, collapsed, the candidates that were already in the SDK
// release the controller builds against, or "" when there are none. They are real
// gaps — regeneration would pick some of them up — but not news, so they are shown
// without being counted: see reportable.
func preexistingBlock(findings []Finding, baselineVersion string) string {
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

	var b strings.Builder
	fmt.Fprintf(&b, "<details>\n<summary>%s already in %s and missing from the controller</summary>\n\n",
		plural(len(old), "candidate", "candidates"), baselineVersion)
	b.WriteString("These predate the SDK release the controller builds against, so they do not drive this notification.\n\n")
	for i, f := range old {
		if i == maxPreexistingEntries {
			fmt.Fprintf(&b, "- _and %d more_\n", len(old)-i)
			break
		}
		where := ""
		if f.Kind != "" && f.Kind != f.Subject {
			where = f.Kind + " "
		}
		fmt.Fprintf(&b, "- %s%s `%s`", where, preexistingLabels[f.Class], f.Subject)
		if ops := f.evidenceOps(); len(ops) > 0 {
			fmt.Fprintf(&b, " — %s", quoteOps(ops))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n</details>\n\n")
	return b.String()
}

// plural renders a count with the singular or plural noun to match.
func plural(n int, singular, pluralForm string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, pluralForm)
}

// issueBlock is one "## " heading's worth of the body, held as the pieces
// truncation can drop independently rather than as one rendered string: whole
// sections, and single entries within a section.
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

// issueEntry is one bullet and anything nested under it, paired with the number
// of findings it stands for. Every entry the renderer builds today stands for one
// finding; the count is kept separate so truncation charges what an entry hides
// rather than assuming it.
type issueEntry struct {
	// text is newline-terminated and may span several lines.
	text     string
	findings int
}

// blockReserves returns, for each block, the number of bytes the blocks after it
// need in order to render something. Holding that back is what keeps one
// oversized block from consuming the whole budget and starving the rest.
//
// A caller is expected to clamp these to a fraction of the budget it actually has
// left: with thousands of blocks the sum far exceeds the cap, and reserving more
// than is available would leave the body empty.
func blockReserves(blocks []issueBlock) []int {
	reserves := make([]int, len(blocks))
	behind := 0
	for i := len(blocks) - 1; i >= 0; i-- {
		reserves[i] = behind
		behind += blocks[i].minRenderSize()
	}
	return reserves
}

// minRenderSize is the fewest bytes in which the block can still say something:
// its heading, its first section's heading, the first entry, the blank line that
// closes the section, and the note admitting the rest was dropped.
//
// A block with no entries to render is worth no bytes at all, which is why the
// search below skips empty sections rather than indexing sections[0] — the
// builders never produce one, but a hand-assembled block would panic.
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

// render emits the block, or as much of it as fits, returning the markdown and
// the number of findings left out of it. It may write up to budget bytes when the
// whole block fits, and up to budget-reserve when it has to truncate, leaving
// reserve bytes for the blocks that come after.
//
// A block is dropped down to "" only when the space cannot hold a heading plus one
// entry — a heading with nothing under it tells a reader nothing and spends bytes
// the blocks behind this one can use. That is the same result as an empty block,
// which also renders "", and the two are told apart by the returned count.
func (blk issueBlock) render(budget, reserve int) (string, int) {
	// entryCount rather than len(sections): a block whose sections are all empty
	// has nothing to say either, and answering "" here is what keeps the loop below
	// from having to reason about it.
	if blk.entryCount() == 0 {
		return "", 0
	}
	if whole := blk.renderWhole(); len(whole) <= budget {
		return whole, 0
	}

	// What is left for entries after the caller's reserve and this block's own
	// note. The note is charged up front, sized as if every entry were dropped, so
	// appending it at the end can never push the block back over budget.
	totalEntries := blk.entryCount()
	avail := budget - reserve - len(blockOmissionNote(totalEntries, totalEntries))

	var b strings.Builder
	omittedEntries, omittedFindings := 0, 0
	for _, section := range blk.sections {
		if len(section.entries) == 0 {
			continue
		}
		// A "### " heading with nothing under it is as useless as a bare "## "
		// one, so its bytes — and, until something has been written, the block
		// heading's — are only spent once an entry is known to follow. The +1 is
		// the blank line that closes the section.
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

// renderWhole renders the block untruncated. A section with no entries is skipped
// rather than left as a bare "### " heading; render only calls this on a block that
// has at least one entry somewhere.
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

// blockOmissionNote states what truncation dropped from one block. It counts
// entries, which is what the reader can see above it; the summary line at the end
// of the body totals findings across the whole body.
func blockOmissionNote(omitted, total int) string {
	return fmt.Sprintf("_%d of %d entries under this heading omitted to keep this issue body within GitHub's size limit._\n\n",
		omitted, total)
}

// buildResourceBlock decomposes one resource's findings into a block. A section
// with no items is left out entirely, so a resource whose findings no section can
// place — a ClassUnknownOperation carrying a non-empty Kind, say — yields a block
// with no sections, which renders as nothing rather than as a bare
// "## Resource: X" header. buildCatchAllBlock picks those findings up instead.
// writeBullet writes a finding's bullet up to, not including, any annotation. A
// field's operations follow its path on the same line; a resource's go on a nested
// line after it, see buildResourceBlock.
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

// buildCatchAllBlock collects the findings no resource block accounts for. It is
// a true catch-all rather than just the unclassified operations: every finding is
// hashed into the fingerprint, so one that reaches the digest but not the body is
// a silent disagreement between the two that reads as a rendering bug.
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
		// A finding only lands here with a Kind when its class has no section;
		// naming the kind keeps "Unattributed" from being a lie in that case.
		if f.Kind != "" {
			fmt.Fprintf(&text, " (kind `%s`)", f.Kind)
		}
		text.WriteString("\n")
		section.entries = append(section.entries, issueEntry{text: text.String(), findings: 1})
	}
	return issueBlock{heading: "## Unattributed", sections: []issueSection{section}}
}

// filterFindings returns the findings of one class, ordered by lessFinding. The
// sort is what makes a rendered body reproducible: the producers walk models in
// map order, so their output order is not stable across runs.
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
// NewSincePin — every field of a Finding, so the order is total.
//
// Anything short of that churns the body. sort.Slice is not stable, so findings
// that tie swap places between runs, and every field here reaches the rendered
// text: Detail and Subject directly, NewSincePin as the "(new in vX)" annotation,
// Kind as the "(kind `W`)" suffix buildCatchAllBlock appends. The body would
// differ from run to run while the fingerprint — which hashes neither Detail nor
// NewSincePin — stayed identical, so Task 14 would keep reporting issueUnchanged
// and never notice. Ties are unreachable from today's producers, but
// dedupeFindings deliberately preserves the Detail case for when an operation's
// Input and Output doc traits diverge. Class is compared by ordinal, which is fine
// for ordering; only the fingerprint needs the stable name.
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

// issueOutcome is what a run decided to do about one service's issue.
// reconcileIssue is the only thing that returns one, so the type and every member
// live here beside it.
//
// They used to be split: the type and issueSuppressedByClosed sat in github.go
// next to listAPIChangeIssues, and this file continued that block's iota by hand
// with `iota + 2`. The offset was correct and still wrong to keep — iota restarts
// at 0 in every const block, so adding one member to the github.go block silently
// aliased two decisions here, with nothing for the compiler to object to, in the
// type that gates writes to a public repo. Explicit literal values make a
// collision visible at the declaration instead of inferrable from an offset two
// files away.
type issueOutcome int

const (
	// issueOutcomeNone is the zero value: no decision was reached. Every error
	// return from reconcileIssue uses it, so a non-zero outcome always means a
	// decision that completed. Returning a real outcome alongside an error was
	// actively misleading — a create that filed the issue and then failed its label
	// check reported issueUnchanged, the opposite of what happened, and a caller
	// doing bookkeeping off that outcome would have believed nothing was written.
	//
	// 0 is deliberately not a decision for the same reason it always was: a
	// reconcile that returns before deciding must not read as "a human said no".
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
	// issueStaleOpenIssue means an open issue exists for a service that has no
	// current findings. Nothing is written, but it is not a quiet no-op either: the
	// issue asserts changes that no longer exist and holds a cap slot that a service
	// with real findings could use, so the caller logs it.
	issueStaleOpenIssue issueOutcome = 6
)

// String names an outcome for the log.
//
// This job runs unattended and its only other output is a log somebody may read
// later, so `outcome=4` is not something a reader can act on. FindingClass.String
// above sets the convention. Unlike that one these names are not a wire format —
// no fingerprint hashes them — so they can be reworded freely.
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
// existing is this service's already-resolved open issue, or nil when the
// detector has none. The caller gets it from a single listAPIChangeIssues
// call for the whole run rather than one search per service: search allows 30
// requests a minute, and a per-service lookup made a run cost 1+N of them, which
// 403s about a third of the way through a full ~74-service rollout.
//
// closedFingerprints is the set of fingerprints on *closed* issues for this
// service. A finding set matching one of them is deliberately not re-filed:
// closing is the only way a maintainer can say "I have seen this and it needs no
// issue", and while the search was scoped is:open that said nothing — the next run
// found nothing open and re-filed the same fingerprint, daily, for ever. Because
// the fingerprint covers findings only, a genuinely new change produces a new
// fingerprint and still files, so closing cannot silence future changes.
//
// openCount is the number of open issues this detector already owns; maxOpen
// is the configured cap, which must be positive. The cap gates creation only:
// refreshing an existing issue does not increase the count, so a service whose
// findings changed always gets current information.
//
// The caller owns openCount. reconcileIssue never mutates it, so a caller looping
// over services must increment its own counter on every issueCreated; nothing in
// here can detect the caller forgetting, and a run where 40 services each need a
// new issue would otherwise create all 40 under a cap of 10.
//
// Every error return carries issueOutcomeNone, so an outcome is only ever a
// decision that completed and a caller can never do bookkeeping off a phantom one.
// An error wrapping errCannotLabelIssues is a property of the credential rather
// than of this service and must abort the whole run — see that sentinel.
//
// dryRun suppresses exactly three calls — createGithubIssueWithClient,
// commentOnGithubIssue and updateGithubIssueBody — and nothing else. Every decision
// above them runs for real, because the point is to preview the decision rather than
// to simulate one: closed-fingerprint suppression still applies, the cap still binds,
// the fingerprint comparison still runs, and the oversized-merged-body guard below
// still fires. That last one is deliberate on both counts — it is a pure computation,
// so a dry run can reach the same verdict a real run would, and it reports a refresh
// that genuinely cannot land, which is exactly what a preview is for.
//
// Two things a dry run cannot establish, and the caller says so in its output rather
// than leaving a reader to assume otherwise:
//
//   - Whether the token can label issues. errCannotLabelIssues is raised inside
//     createGithubIssueWithClient *after* the POST, by re-reading the created issue.
//     Dry-run never posts, so that check cannot run at all and a clean dry run is no
//     evidence that a real run will produce findable issues.
//   - Whether any write would succeed — permissions, rate limits, a locked issue.
//
// The comment and the PATCH are gated together, not separately. Commenting without
// patching is a state that tells subscribers a refresh is coming and then never
// delivers it, which is the one outcome a dry run must never produce.
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
	// Rejected rather than read as "no cap". `maxOpen > 0 && openCount >= maxOpen`
	// made 0 and negatives mean unlimited, so a flag defaulting to 0 or a caller
	// forgetting to pass one turned the only brake on a public-repo writer into a
	// no-op across ~74 services — failing open, in the unbounded direction, silently.
	// Anyone who wants an effectively absent cap sets a large number on purpose.
	if maxOpen <= 0 {
		return issueOutcomeNone, fmt.Errorf(
			"open-issue cap must be positive, got %d; 0 would mean unlimited filing", maxOpen)
	}

	if len(reportable(findings)) == 0 {
		// Nothing detected, so nothing is written — not even to close or annotate an
		// open issue. That is the one real policy question in this function, so:
		// an unattended job editing or closing issues off the *absence* of evidence
		// is the failure mode worth avoiding, since a detector bug, a model fetch
		// that returned an empty API, or a service whose model moved all produce
		// zero findings and would then quietly retract a real report. A human
		// closing the issue is cheap; a wrongly closed one is not noticed.
		//
		// The cost is real and is reported rather than hidden: the issue keeps
		// asserting changes that no longer exist and keeps counting against
		// openCount, starving creation for services that do have findings. The
		// caller logs issueStaleOpenIssue so that shows up in the run's output
		// instead of being indistinguishable from a service with nothing to say.
		if existing != nil {
			return issueStaleOpenIssue, nil
		}
		return issueUnchanged, nil
	}

	// Take the fingerprint renderIssueBody actually embedded rather than
	// recomputing one. It dedupes before hashing, and producers 3 and 4 do emit
	// exact duplicates (16 + 20 of them for lambda, 1 for iam — see
	// dedupeFindings), so fingerprintFindings(service, findings) on the raw slice
	// would not match the marker in the very body being compared against it.
	body, want := renderIssueBody(service, baselineVersion, latestVersion, findings)

	if existing == nil {
		// Consulted only when nothing is open: an open issue takes precedence over
		// a closed one carrying the same fingerprint, so a service with both gets
		// its open issue refreshed rather than going quiet.
		if closedFingerprints[want] {
			return issueSuppressedByClosed, nil
		}
		if openCount >= maxOpen {
			return issueSkippedAtCap, nil
		}
		title := fmt.Sprintf("AWS API changes detected for %s", service)
		// Both labels, and both are load-bearing for different reasons.
		// apiChangeLabel (`ack/api-change-detected`) is what the next run's
		// listing keys on, so an issue created without it is invisible for ever
		// and the job files a fresh duplicate every day. `kind/api-change` is the
		// Prow triage label humans filter by; it is `addedBy: anyone`, which is
		// precisely why it cannot serve as the ownership marker.
		labels := []string{
			apiChangeLabel,
			"kind/api-change",
			fmt.Sprintf("service/%s", service),
			defaultProwAutoGenLabel,
		}
		if !dryRun {
			if _, err := createGithubIssueWithClient(ctx, client, owner, repo, title, body, labels); err != nil {
				// issueOutcomeNone, not issueUnchanged: the label check inside runs
				// *after* the POST, so this error can mean an issue was in fact filed.
				// Reporting issueUnchanged there asserted the opposite of what happened.
				return issueOutcomeNone, err
			}
		}
		return issueCreated, nil
	}

	number := existing.GetNumber()

	// Same finding set, so nothing to notify anyone about. But the report can still
	// read differently — a reworded reason, an annotation saying a field is
	// immutable — and the fingerprint deliberately hashes only the findings, so
	// without this an issue filed before such a change kept its old text for as
	// long as its finding set held: observed live, a reviewer's requested
	// annotations never reached the open ec2 issue. The rewrite is silent, without
	// the comment below, because subscribers have nothing new to look at.
	//
	// The comparison re-renders at the versions the issue already names. A new SDK
	// release alone changes only those, and rewriting every open issue on each one
	// is churn with nothing to read; an issue whose versions cannot be read is left
	// alone for the same reason.
	if parseFingerprint(existing.GetBody()) == want {
		oldBaseline, oldLatest, ok := parseComparedVersions(existing.GetBody())
		if !ok {
			return issueUnchanged, nil
		}
		asFiled, _ := renderIssueBody(service, oldBaseline, oldLatest, findings)
		if replaceGeneratedRegion(existing.GetBody(), asFiled) == existing.GetBody() {
			return issueUnchanged, nil
		}
		merged := replaceGeneratedRegion(existing.GetBody(), body)
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

	// Built before the comment deliberately, and checked here rather than at the
	// PATCH. A merged body over the limit fails the PATCH with a 422 every run, which
	// leaves the fingerprint stale, which means the update path runs again tomorrow —
	// and since the comment now precedes the PATCH, each of those runs would notify
	// every subscriber about a refresh that cannot land. Probed: five consecutive PATCH
	// failures produced five comments. For a transient failure that is the accepted
	// cost of commenting first; for a permanent one it is daily notification on a
	// public issue, for ever.
	//
	// renderIssueBody budgets only its own output, so it cannot see this: the excess is
	// the maintainer's text outside the region, which replaceGeneratedRegion adds back.
	// Measured: a region at the truncation cap (65,457 bytes) plus a 300-byte
	// maintainer note is 65,767, over the 65,536 limit.
	//
	// There is deliberately no fallback to PATCHing the rendered body alone. That is
	// the whole-body replacement this function used to do, and it deletes the
	// maintainer's notes — the very text that pushed the merge over the limit. Failing
	// and leaving the issue untouched is correct: a human can shorten the note.
	merged := replaceGeneratedRegion(existing.GetBody(), body)
	if len(merged) > githubMaxIssueBody {
		return issueOutcomeNone, fmt.Errorf(
			"refreshing issue %s/%s#%d would produce a %d-byte body, over GitHub's %d-byte limit; "+
				"the generated report plus the text outside it no longer fit",
			owner, repo, number, len(merged), githubMaxIssueBody)
	}

	// Comment first, then rewrite the body. The order is the whole point, because
	// the fingerprint in the body is also the done-marker.
	//
	// Patching first meant the new fingerprint was already written when the comment
	// was attempted, so a failed comment left the next run matching on that
	// fingerprint and short-circuiting to issueUnchanged with zero HTTP requests:
	// the notification was never retried, not the next day, not ever. Editing a body
	// is silent (see commentOnGithubIssue), so one transient 403 meant a finding-set
	// change nobody was ever told about, on an unattended job.
	//
	// This way a failed comment leaves the fingerprint stale, the next run retries
	// both, and the worst case is a duplicate comment — noisy and self-correcting
	// rather than silent and permanent.
	//
	// The counts a reader might want here (was N findings, now M) are deliberately
	// absent. previousCount is not recoverable from the fingerprint, which is a hash,
	// and the only other source is the existing body — where truncation drops
	// entries from a body whose fingerprint still covers the full set. Counting
	// bullets would therefore put a
	// wrong number on the one message that notifies humans, so this states only the
	// fact the code actually knows. It also avoids the previous text's "New AWS API
	// changes detected", which was a false alarm whenever the set had *shrunk* —
	// findings being resolved changes the fingerprint too, and the code cannot tell
	// the two directions apart.
	//
	// Future tense, because the PATCH below can still fail: the probe captured
	// "Refreshing the report in this issue's body" immediately followed by a failed
	// PATCH, which left a reader a claim of a refresh sitting next to an unchanged
	// body. This comment must not assert a write that has not happened yet.
	comment := fmt.Sprintf(
		"The detected API change set for `%s` has changed. The report in this issue's "+
			"body will be refreshed, comparing aws-sdk-go-v2 %s -> %s.",
		service, baselineVersion, latestVersion,
	)
	// Both writes sit behind one !dryRun rather than two, which is the ordering above
	// read the other way round: the pair is what makes commenting first safe, so
	// suppressing only one of them in a preview would manufacture the exact half-done
	// state — subscribers told a refresh is coming that never arrives — that the
	// ordering exists to bound.
	if !dryRun {
		if err := commentOnGithubIssue(ctx, client, owner, repo, number, comment); err != nil {
			return issueOutcomeNone, err
		}

		// merged, not body: only the generated region is rewritten, so a maintainer's
		// notes on the issue survive. Passing `body` straight through replaced the whole
		// body and silently deleted them — and the accompanying comment then notified
		// every subscriber about it.
		if err := updateGithubIssueBody(ctx, client, owner, repo, number, merged); err != nil {
			return issueOutcomeNone, err
		}
	}
	return issueUpdated, nil
}

// getAPINotificationServices reads the api_notification_services list from
// jobs_config.yaml, preserving file order so that cap-limited runs are
// deterministic. It also returns api_notification_max_open_issues, which is
// informational: see the --max-open-issues flag for why the flag stays
// authoritative and this value only feeds a warning when the two disagree. A
// config that omits it yields 0, which the caller reads as "nothing to compare
// against".
//
// It validates the list here rather than trusting that `make prow-gen` did.
// Generation-time validation is not on this path at all: the running job is handed
// jobs_config.yaml through extra_refs or the jobs-config ConfigMap, both of which
// read the file directly, and that file is hand-maintained. An unvalidated entry
// that is not an ACK service has no controller to diff against and no
// `service/<name>` label to apply, and the post-create label check verifies only the
// ownership label — so it would file a real issue in a public repo carrying a label
// that exists in no config file.
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
