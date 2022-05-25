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
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"
	"strings"

	"github.com/sirupsen/logrus"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
)

var (
	ErrorGoModFileNotFound = errors.New("module file (go.mod) not found")
	ErrorLicenseNotFound   = errors.New("LICENSE file not found")
)

// graphBuilder helps building the dependency graph of a given go module.
type graphBuilder struct {
	logger *logrus.Logger
	// modulesCache is used to avoid downloading/parsing already seen modules.
	modulesCache map[string]*Module
	// classifier for licenses
	lc *licenseClassifierWrapper
}

// newGraphBuilder instantiate a new graphBuilder
func newGraphBuilder(logger *logrus.Logger, licenseClassificationTreshold float64) (*graphBuilder, error) {
	lc, err := newLicenseClassifier(licenseClassificationTreshold)
	if err != nil {
		return nil, err
	}
	return &graphBuilder{
		modulesCache: make(map[string]*Module),
		logger:       logger,
		lc:           lc,
	}, nil
}

// buildGraph takes a modfile a max depth and proceeds into building the
// dependency Tree. maxDepth is the depth at which the graphBuilder will
// will stop exploring the dependency graph.
func (gb *graphBuilder) buildGraph(mod *modfile.File, maxDepth int) (*Tree, error) {
	requiredModules := []*module.Version{}
	for _, r := range mod.Require {
		requiredModules = append(requiredModules, &r.Mod)
	}

	gb.logger.Debug("Started building the dependency graph")

	modules, err := gb.buildModulesDependencyGraph(requiredModules, 0, maxDepth)
	if err != nil {
		return nil, fmt.Errorf("cannot build modules tree: %v", err)
	}

	gb.logger.Debug("Dependency graph built successfully")

	return &Tree{
		Root: &Module{
			Version:      &mod.Module.Mod,
			Dependencies: modules,
		},
	}, nil
}

func (gb *graphBuilder) getModuleFromCache(mod *module.Version) (*Module, bool) {
	m, found := gb.modulesCache[moduleID(mod)]
	return m, found
}

func (gb *graphBuilder) cacheModule(m *Module) {
	gb.modulesCache[moduleID(m.Version)] = m
}

// buildModulesDependencyGraph takes a list module IDs (version and path) and
// returns a list *Module objects, containing the corresponding licenses and
// dependencies.
func (gb *graphBuilder) buildModulesDependencyGraph(
	mods []*module.Version,
	depth int,
	maxDepth int,
) ([]*Module, error) {
	var modules []*Module
	for _, mod := range mods {
		if depth == maxDepth {
			continue
		}

		gb.logger.Debugf("Exploring module %s", mod.String())

		// first check the cache
		module, cached := gb.getModuleFromCache(mod)
		if cached {
			modules = append(modules, module)
			continue
		}

		// else, recursively build the Module object and cache it
		license, requiredModules, err := gb.extractLicenseAndRequiredModules(mod)
		if err != nil {
			return nil, err
		}
		gb.logger.Debugf("Found %s license and %d required modules", mod.String(), len(requiredModules))

		licenseType, err := gb.lc.detectLicense(license)
		if err != nil {
			return nil, err
		}
		module = &Module{
			Version: mod,
			License: &License{
				Data: license,
				Name: licenseType,
			},
		}
		moduleDependencies, err := gb.buildModulesDependencyGraph(
			requiredModules, depth+1, maxDepth,
		)
		if err != nil {
			return nil, err
		}
		module.Dependencies = moduleDependencies

		// cache the module
		gb.cacheModule(module)
		gb.logger.Debugf("Cached %s module", mod.String())

		modules = append(modules, module)
	}

	return modules, nil
}

// extractLicenseAndRequiredModules downloads a module from the configured
// go proxy and extract the license and the required modules from it go.mod
// file.
func (gb *graphBuilder) extractLicenseAndRequiredModules(
	mod *module.Version,
) ([]byte, []*module.Version, error) {
	// Download the module
	gb.logger.Debugf("Downloading %v content", mod.String())
	moduleZip, err := downloadModule(mod)
	if err != nil {
		return nil, nil, err
	}

	// extract the license bytes
	gb.logger.Debugf("Extracting %v license", mod.String())
	license, err := extractLicense(mod.String(), moduleZip)
	if err != nil {
		return nil, nil, err
	}

	// extract the required modules from go.mod file
	gb.logger.Debugf("Extracting %v required modules", mod.String())
	requiredModules, err := getRequiredModules(mod.String(), moduleZip)
	if err != nil && err != ErrorGoModFileNotFound {
		return nil, nil, err
	}

	return license, requiredModules, nil
}

// extractLicense looks in a module zipFile and returns the content of its
// license
func extractLicense(moduleFullName string, zipfile []byte) ([]byte, error) {
	zipReader, err := zip.NewReader(bytes.NewReader(zipfile), int64(len(zipfile)))
	if err != nil {
		return nil, err
	}

	for _, file := range zipReader.File {
		cleanFileName := strings.TrimPrefix(file.Name, strings.ToLower(moduleFullName))
		if isLicenseFilename(cleanFileName) {
			f, err := file.Open()
			if err != nil {
				return nil, fmt.Errorf("cannot open license file: %v", err)
			}
			defer f.Close()
			b, err := ioutil.ReadAll(f)
			if err != nil {
				return nil, fmt.Errorf("cannot read license file content: %v", err)
			}
			return b, nil
		}
	}
	return nil, ErrorLicenseNotFound
}

// isLicenseFilename returns true if the filename most likely contains a license.
func isLicenseFilename(filename string) bool {
	name := strings.ToLower(filename)
	// NOTE(a-hilaly) are we missing any other cases?
	return name == "/license.txt" ||
		name == "/license" ||
		name == "/license.md" ||
		name == "/copying"
}

// extractLicense looks in a module zipFile and returns the list of required modules.
func getRequiredModules(moduleFullName string, zipfile []byte) ([]*module.Version, error) {
	zipReader, err := zip.NewReader(bytes.NewReader(zipfile), int64(len(zipfile)))
	if err != nil {
		return nil, err
	}
	for _, file := range zipReader.File {
		cleanFileName := strings.TrimPrefix(file.Name, moduleFullName)
		if cleanFileName == "/go.mod" {
			f, err := file.Open()
			if err != nil {
				return nil, fmt.Errorf("cannot open mod file: %v", err)
			}
			defer f.Close()
			b, err := ioutil.ReadAll(f)
			if err != nil {
				return nil, fmt.Errorf("cannot read file content: %v", err)
			}
			return getRequiredModulesFromBytes(b)
		}
	}

	return nil, ErrorGoModFileNotFound
}

// extractLicense parses a go module file and returns the list of required modules.
func getRequiredModulesFromBytes(bytes []byte) ([]*module.Version, error) {
	goMod, err := modfile.Parse("", bytes, nil)
	if err != nil {
		return nil, err
	}

	modules := []*module.Version{}
	for _, r := range goMod.Require {
		modules = append(modules, &r.Mod)
	}
	return modules, nil
}
