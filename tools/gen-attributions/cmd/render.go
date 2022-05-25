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
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"github.com/sirupsen/logrus"
	"golang.org/x/mod/module"
)

func newRenderer(logger *logrus.Logger) *renderer {
	return &renderer{
		visitedModules: make(map[string]bool),
		logger:         logger,
	}
}

// renderer helps generating the attributions file. The current
// implementation generate ACK similar ATTRIBUTIONS.md files.
type renderer struct {
	logger         *logrus.Logger
	visitedModules map[string]bool
}

func (r *renderer) isVisitedModule(m *module.Version) bool {
	_, visited := r.visitedModules[moduleID(m)]
	return visited
}
func (r *renderer) markModuleAsVisited(m *module.Version) {
	r.visitedModules[moduleID(m)] = true
}

// generateAttributionsFiles parses returns the content of the attributions files.
func (r *renderer) generateAttributionsFiles(attributionsFile *AttributionsFile) (string, error) {
	buf := &bytes.Buffer{}

	// generate the header
	tmp := template.New("header_tmp")
	t, err := tmp.Parse(attributionsFileHeaderTemplateOpt)
	if err != nil {
		return "", err
	}
	err = t.Execute(buf, attributionsFile)
	if err != nil {
		return "", err
	}

	// generate the modules licenses and subdependencies blocks
	out := buf.String()
	for _, m := range attributionsFile.Tree.Root.Dependencies {
		moduleOut, err := r.renderModule(m, 0)
		if err != nil {
			return "", err
		}
		out += trimEmptyLines(moduleOut)
	}
	return out, nil
}

// renderModule renders the content of a module block in the attributions file.
// The module block contains the module License and its subdependencies.
func (r *renderer) renderModule(m *Module, indentLevel int) (string, error) {
	// We avoid to generated a module block twice in the attributions file.
	if r.isVisitedModule(m.Version) {
		return "", nil
	}
	r.markModuleAsVisited(m.Version)

	// generate the modules block
	tmp := template.New("module_tmp")
	t, err := tmp.Parse(attributionsFileBlockTemplateOpt)
	if err != nil {
		return "", err
	}
	indent := strings.Repeat("#", indentLevel)
	buf := &bytes.Buffer{}

	licenseData := ""
	if getLicenseType(m.License.Name) == LicenseApache20 {
		licenseData = "Apache License version 2.0"
	} else {
		licenseData = string(m.License.Data)
	}
	err = t.Execute(buf, &attributionModuleVars{
		TitlePrefix:  fmt.Sprintf("%s###", indent),
		License:      licenseData,
		Name:         m.Version.Path,
		Dependencies: m.Dependencies,
	})
	if err != nil {
		return "", err
	}

	// now generate the modules blocks for the subdependencies.
	out := trimEmptyLines(buf.String())
	if m.Dependencies != nil {
		for _, m := range m.Dependencies {
			moduleOut, err := r.renderModule(m, indentLevel+1)
			if err != nil {
				return "", err
			}
			out += trimEmptyLines(moduleOut)
		}
	}

	return out, nil
}

func trimEmptyLines(out string) string {
	out = strings.Trim(out, "\n")
	return "\n" + out + "\n"
}
