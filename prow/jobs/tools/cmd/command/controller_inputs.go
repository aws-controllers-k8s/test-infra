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
	// GoModServiceVersion is the service module version go.mod requires: the SDK
	// release the controller builds against, and so the baseline for what is new.
	// Empty when go.mod does not require the module.
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
	// paths its CRD exposes.
	CRDFields map[string]map[string]bool
	// UsedOps maps normalizeResourceKey of a resource package directory name to
	// the SDK operations that package invokes.
	//
	// Not every key is a CRD kind. Seven controllers (bedrockagent, cloudfront,
	// elbv2, emrcontainers, firehose, kinesis, secretsmanager) ship a shared
	// pkg/resource/tags helper package with no corresponding CRD. Conversely a
	// kind can have no package at all: prometheusservice ships CRDs for
	// AnomalyDetector and QueryLoggingConfiguration with no resource package, so
	// those appear in CRDFields but not here. Consumers must tolerate both
	// directions.
	UsedOps map[string]map[string]bool

	// kindsByLower maps a lowercased kind to the CRD's canonical kind. AWS
	// operation names yield "VpcEndpoint"-style casing while ACK generates
	// "VPCEndpoint"-style kinds, so kind matching must ignore case.
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
	crdFields, err := readCRDFields(controllerPath)
	if err != nil {
		return nil, err
	}
	usedOps, err := scanUsedOps(controllerPath)
	if err != nil {
		return nil, err
	}

	// model_name and package_name are both optional; they default to the
	// service alias.
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
// CRD kind ACK actually generated, ignoring case, and reports whether such a
// CRD exists. This matters more than it looks: ec2 alone ships DHCPOptions,
// NATGateway, NetworkACL, VPC, VPCEndpoint, VPCEndpointServiceConfiguration,
// VPCPeeringConnection, and TransitGatewayVPCAttachment, while the AWS API
// names them DhcpOptions, NatGateway, NetworkAcl, Vpc, VpcEndpoint, and so on.
// An exact-match lookup would report 8 of ec2's 20 resources as missing on
// every single run.
func (in *ControllerInputs) CanonicalKind(kind string) (string, bool) {
	canonical, ok := in.kindsByLower[strings.ToLower(kind)]
	return canonical, ok
}

// ClassifyOpWithOverrides classifies an operation, consulting the controller's
// generator.yaml `operations:` overrides before falling back to name
// inference. code-generator does the same in GetOperationMap, which calls
// getOpTypesAndResourcesMapping(opID, cfg) — the config wins over the prefix
// heuristic.
//
// This matters for operations whose names carry no recognisable prefix.
// route53's `ChangeResourceRecordSets` infers to OpTypeUnknown, but its
// generator.yaml declares `operation_type: [Create, Delete]`, so codegen treats
// it as the create operation for a resource. Without reading the override we
// would drop it into the "needs review" bucket on every run.
//
// When an override lists several operation types, the first that maps onto a
// type this tool understands wins; ordering in generator.yaml is significant
// only in that we report one classification per operation.
//
// Two deliberate divergences from code-generator, both verified to have no
// effect on any controller in the corpus:
//
//   - codegen registers a multi-typed operation under *every* listed type, so
//     route53's ChangeResourceRecordSets is simultaneously RecordSet's Create
//     and Delete operation. 12 controllers declare a multi-valued
//     operation_type. We return one classification, so a consumer asking "what
//     is this resource's Delete operation?" can get a false negative. Acceptable
//     here because the consumers only ask whether an operation is a Create (to
//     infer a CRD) and which resource it belongs to.
//   - codegen ignores an override entirely unless both operation_type and
//     resource_name are set; we apply whichever is present. No controller
//     declares one without the other.
func (in *ControllerInputs) ClassifyOpWithOverrides(
	opID string,
	configResources []string,
) (OpType, string) {
	if in.Config == nil {
		return ClassifyOp(opID, configResources)
	}

	override, ok := in.Config.Operations[opID]
	if !ok {
		return ClassifyOp(opID, configResources)
	}

	inferredType, inferredName := ClassifyOp(opID, configResources)

	// Resolve the resource name. When the override lists several, prefer the
	// one name inference already arrived at, and only fall back to the first
	// declared name otherwise.
	//
	// Taking [0] unconditionally is actively wrong on real data. lambda declares:
	//
	//	DeleteFunction:
	//	  operation_type: [Delete]
	//	  resource_name: [Version, Function]
	//
	// Name inference alone yields "Function", which is right. Taking [0] would
	// substitute "Version", making the override worse than no override at all.
	// code-generator registers such an operation under *every* listed resource;
	// this function returns a single classification, so preferring the inferred
	// name is the most faithful single answer available. This is the only
	// multi-valued resource_name in the corpus — 223 overrides across 78
	// generator.yaml files — so the narrow rule suffices.
	resName := inferredName
	if len(override.ResourceName) > 0 {
		resName = firstMatchOrDefault(override.ResourceName, inferredName)
	}

	for _, declared := range override.OperationType {
		if opType, known := opTypeFromConfigString(declared); known {
			return opType, resName
		}
	}
	return inferredType, resName
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
// OpType. The spellings are those code-generator accepts in its
// OpTypeFromString.
//
// Underscores are stripped as well as case folded, because real controllers use
// upper-snake spellings: 20 operations across acm, apigateway, cloudfront,
// kinesis, route53resolver, and sns declare READ_ONE, GET_ATTRIBUTES, or
// SET_ATTRIBUTES. Those happen to reach the same OpType through name inference
// today, so recognising them here changes nothing yet — but relying on that
// coincidence is how a latent gap becomes a bug.
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
