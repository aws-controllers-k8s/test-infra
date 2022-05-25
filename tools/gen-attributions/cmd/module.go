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

package main

import (
	"fmt"

	"github.com/xlab/treeprint"
	"golang.org/x/mod/module"
)

// Module represent a Go module version, license and dependencies.
type Module struct {
	// Version contains the identifiers of a public Go module.
	// The unique identifier of a module is in format $path@$version
	Version *module.Version
	// LicenseBytes is the license of the module.
	License *License
	// Is the list of the required modules found in a go.mod file.
	Dependencies []*Module
}

// Tree represents the dependency tree of a Go module
type Tree struct {
	Root *Module
}

// AttribnutionsFile represent the data that should be rendered in a
// ATTRIBUTIONS.md file.
type AttributionsFile struct {
	// Header template
	Header string
	// The module dependency tree
	Tree *Tree
}

func (tree *Tree) String() string {
	root := treeprint.New()
	root.SetValue(tree.Root.Version.Path)
	addChildNodes(root, tree.Root.Dependencies)
	return root.String()
}

func addChildNodes(parent treeprint.Tree, modules []*Module) {
	for _, m := range modules {
		child := parent.AddBranch(m.Version.String() + " " + m.License.Name)
		addChildNodes(child, m.Dependencies)
	}
}

func moduleID(mod *module.Version) string {
	return fmt.Sprintf("%v@%v", mod.Path, mod.Version)
}
