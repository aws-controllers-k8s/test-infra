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
	"path/filepath"
	"slices"
	"strings"
)

// ControllerInputs is everything this tool reads out of one ACK controller
// repository.
type ControllerInputs struct {
	// Service is the ACK service alias, e.g. "s3".
	Service string
	// SDKVersion is the core aws-sdk-go-v2 tag the controller was generated
	// against.
	SDKVersion string
	// ServiceSDKVersion is the per-service tag, empty for most controllers.
	ServiceSDKVersion string
	// GoModServiceVersion is the service module version go.mod requires, the
	// baseline for what is new. Empty when go.mod does not require it.
	GoModServiceVersion string
	// ModelName is the aws-models JSON file base name, which can differ from
	// the service alias (route53 -> route-53, opensearchservice -> opensearch).
	ModelName string
	// PackageName is the aws-sdk-go-v2 service package directory, used as the
	// per-service tag path segment (documentdb -> docdb).
	PackageName string
	// Config is the generator.yaml subset.
	Config *generatorConfig
	// CRDFields maps resource kind to the set of lowercased dotted field
	// paths its CRD's Spec exposes. It has an entry for every kind.
	CRDFields map[string]map[string]bool
	// CRDStatusFields is CRDFields for the CRD's Status.
	CRDStatusFields map[string]map[string]bool
	// UsedOps maps normalizeResourceKey of a resource package directory name to
	// the SDK operations that package invokes. Keys and CRD kinds don't line up
	// one-to-one: some controllers have a pkg/resource/tags helper with no CRD,
	// and some CRDs (e.g. prometheusservice's) have no resource package.
	UsedOps map[string]map[string]bool

	// kindsByLower maps a lowercased kind to the CRD's canonical kind. AWS names
	// use "VpcEndpoint" casing while ACK kinds use "VPCEndpoint".
	kindsByLower map[string]string
}

// ReadControllerInputs reads every input for one service from
// <root>/<service>-controller.
func ReadControllerInputs(root, service string) (*ControllerInputs, error) {
	controllerPath := filepath.Join(root, service+"-controller")

	sdkVersion, serviceSDKVersion, err := readGenerateMetadata(controllerPath)
	if err != nil {
		return nil, err
	}
	cfg, err := readGeneratorConfig(controllerPath)
	if err != nil {
		return nil, err
	}
	crdFields, crdStatusFields, err := readCRDFields(controllerPath)
	if err != nil {
		return nil, err
	}
	usedOps, err := scanUsedOps(controllerPath)
	if err != nil {
		return nil, err
	}

	// model_name and package_name default to the service alias.
	modelName := cfg.SDKNames.ModelName
	if modelName == "" {
		modelName = service
	}
	packageName := cfg.SDKNames.PackageName
	if packageName == "" {
		packageName = service
	}

	goModServiceVersion, err := readGoModServiceVersion(controllerPath, packageName)
	if err != nil {
		return nil, err
	}

	kindsByLower := make(map[string]string, len(crdFields))
	for kind := range crdFields {
		kindsByLower[strings.ToLower(kind)] = kind
	}

	return &ControllerInputs{
		Service:             service,
		SDKVersion:          sdkVersion,
		ServiceSDKVersion:   serviceSDKVersion,
		GoModServiceVersion: goModServiceVersion,
		ModelName:           modelName,
		PackageName:         packageName,
		Config:              cfg,
		CRDFields:           crdFields,
		CRDStatusFields:     crdStatusFields,
		UsedOps:             usedOps,
		kindsByLower:        kindsByLower,
	}, nil
}

// HasCRD reports whether the controller ships a CRD for the given kind,
// ignoring case.
func (in *ControllerInputs) HasCRD(kind string) bool {
	_, ok := in.kindsByLower[strings.ToLower(kind)]
	return ok
}

// CanonicalKind resolves a resource name inferred from an AWS operation to the
// CRD kind ACK generated, ignoring case (AWS "NatGateway" vs ACK "NATGateway"),
// and reports whether such a CRD exists.
func (in *ControllerInputs) CanonicalKind(kind string) (string, bool) {
	canonical, ok := in.kindsByLower[strings.ToLower(kind)]
	return canonical, ok
}

// ClassifyOpWithOverrides classifies an operation, letting generator.yaml
// `operations:` overrides win over name inference, as code-generator's
// GetOperationMap does (e.g. route53 ChangeResourceRecordSets has no
// recognisable prefix but is declared [Create, Delete]).
//
// Like code-generator, an operation holds every declared type, so callers
// should test roles with OpTypes.Has. Unlike code-generator, an override with
// only one of operation_type/resource_name is still applied; no controller
// declares one without the other.
func (in *ControllerInputs) ClassifyOpWithOverrides(
	opID string,
	configResources []string,
) (OpTypes, string) {
	inferredType, inferredName := ClassifyOp(opID, configResources)
	if in.Config == nil {
		return OpTypes{inferredType}, inferredName
	}

	override, ok := in.Config.Operations[opID]
	if !ok {
		return OpTypes{inferredType}, inferredName
	}

	// code-generator registers the operation under every listed resource_name;
	// we return one, so prefer the inferred name when it is listed. lambda's
	// DeleteFunction declares [Version, Function], and [0] would be wrong.
	resName := inferredName
	if len(override.ResourceName) > 0 {
		resName = firstMatchOrDefault(override.ResourceName, inferredName)
	}

	var types OpTypes
	for _, declared := range override.OperationType {
		if opType, known := opTypeFromConfigString(declared); known && !types.Has(opType) {
			types = append(types, opType)
		}
	}
	if len(types) == 0 {
		return OpTypes{inferredType}, resName
	}
	return types, resName
}

// OpTypes is every operation type an operation is registered under. It is never
// empty: an operation that does not classify is {OpTypeUnknown}.
type OpTypes []OpType

// Has reports whether the operation holds any of the given types.
func (t OpTypes) Has(want ...OpType) bool {
	return slices.ContainsFunc(t, func(opType OpType) bool { return slices.Contains(want, opType) })
}

// Only reports whether every type the operation holds is one of the given types.
func (t OpTypes) Only(want ...OpType) bool {
	return !slices.ContainsFunc(t, func(opType OpType) bool { return !slices.Contains(want, opType) })
}

// firstMatchOrDefault returns the declared name matching want, case-insensitively,
// or the first declared name when none matches.
func firstMatchOrDefault(declared []string, want string) string {
	for _, name := range declared {
		if strings.EqualFold(name, want) {
			return name
		}
	}
	return declared[0]
}

// opTypeFromConfigString maps a generator.yaml `operation_type` value onto an
// OpType, accepting the spellings of code-generator's OpTypeFromString.
// Underscores are ignored because controllers use forms like READ_ONE.
func opTypeFromConfigString(s string) (OpType, bool) {
	switch strings.ReplaceAll(strings.ToLower(s), "_", "") {
	case "create":
		return OpTypeCreate, true
	case "createbatch":
		return OpTypeCreateBatch, true
	case "replace":
		return OpTypeReplace, true
	case "update":
		return OpTypeUpdate, true
	case "delete":
		return OpTypeDelete, true
	case "get", "readone":
		return OpTypeGet, true
	case "list", "readmany":
		return OpTypeList, true
	case "getattributes":
		return OpTypeGetAttributes, true
	case "setattributes":
		return OpTypeSetAttributes, true
	}
	return OpTypeUnknown, false
}
