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
	"strings"

	"github.com/gertd/go-pluralize"
)

// OpType categorises an AWS API operation by the role it plays for an ACK
// resource.
type OpType int

const (
	OpTypeUnknown OpType = iota
	OpTypeCreate
	OpTypeCreateBatch
	OpTypeReplace
	OpTypeUpdate
	OpTypeDelete
	OpTypeGet
	OpTypeList
	OpTypeGetAttributes
	OpTypeSetAttributes
)

// pluralizer is shared because NewClient compiles dozens of regexes, most of
// ClassifyOp's cost. Sharing is safe: IsPlural/Singular only read its tables.
var pluralizer = pluralize.NewClient()

// ClassifyOp guesses the operation type and resource name from an operation
// ID. It ports code-generator's model.GetOpTypeAndResourceNameFromOpID so
// results match the CRDs codegen generates. configResources (generator.yaml
// resource names) disambiguates "pluralized singular" names like DhcpOptions.
func ClassifyOp(opID string, configResources []string) (OpType, string) {
	// Case-insensitive, matching code-generator's resourceExistsInConfig.
	declared := func(name string) bool {
		for _, res := range configResources {
			if strings.EqualFold(res, name) {
				return true
			}
		}
		return false
	}

	// singularUnlessDeclared collapses a plural resource name to its singular
	// form, unless the plural form is itself a declared resource name.
	singularUnlessDeclared := func(name string, pluralType, singularType OpType) (OpType, string) {
		if pluralizer.IsPlural(name) {
			if declared(name) {
				return pluralType, name
			}
			return singularType, pluralizer.Singular(name)
		}
		return pluralType, name
	}

	switch {
	case strings.HasPrefix(opID, "CreateOrUpdate"):
		return OpTypeReplace, strings.TrimPrefix(opID, "CreateOrUpdate")

	case strings.HasPrefix(opID, "BatchCreate"):
		return singularUnlessDeclared(
			strings.TrimPrefix(opID, "BatchCreate"), OpTypeCreateBatch, OpTypeCreateBatch,
		)

	case strings.HasPrefix(opID, "CreateBatch"):
		return singularUnlessDeclared(
			strings.TrimPrefix(opID, "CreateBatch"), OpTypeCreateBatch, OpTypeCreateBatch,
		)

	case strings.HasPrefix(opID, "Create"):
		return singularUnlessDeclared(
			strings.TrimPrefix(opID, "Create"), OpTypeCreate, OpTypeCreateBatch,
		)

	case strings.HasPrefix(opID, "Modify"):
		return OpTypeUpdate, strings.TrimPrefix(opID, "Modify")

	case strings.HasPrefix(opID, "Update"):
		return OpTypeUpdate, strings.TrimPrefix(opID, "Update")

	case strings.HasPrefix(opID, "Delete"):
		return OpTypeDelete, strings.TrimPrefix(opID, "Delete")

	case strings.HasPrefix(opID, "Describe"):
		// Describe cannot use singularUnlessDeclared: a singular
		// Describe<X> is a Get, not a List.
		name := strings.TrimPrefix(opID, "Describe")
		if pluralizer.IsPlural(name) {
			if declared(name) {
				return OpTypeList, name
			}
			return OpTypeList, pluralizer.Singular(name)
		}
		return OpTypeGet, name

	case strings.HasPrefix(opID, "Get"):
		if strings.HasSuffix(opID, "Attributes") {
			name := strings.TrimSuffix(strings.TrimPrefix(opID, "Get"), "Attributes")
			return OpTypeGetAttributes, name
		}
		return singularUnlessDeclared(
			strings.TrimPrefix(opID, "Get"), OpTypeGet, OpTypeList,
		)

	case strings.HasPrefix(opID, "List"):
		return singularUnlessDeclared(
			strings.TrimPrefix(opID, "List"), OpTypeList, OpTypeList,
		)

	case strings.HasPrefix(opID, "Set"):
		if strings.HasSuffix(opID, "Attributes") {
			name := strings.TrimSuffix(strings.TrimPrefix(opID, "Set"), "Attributes")
			return OpTypeSetAttributes, name
		}
	}

	return OpTypeUnknown, opID
}
