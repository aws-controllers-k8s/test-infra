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
	"io/ioutil"
	"os"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"golang.org/x/mod/modfile"
)

const (
	defaultOutputFileName = "ATTRIBUTIONS.md"
	defaultModuleFileName = "go.mod"
	defaultDepth          = 2
)

var (
	moduleFilePathOpt                 string
	depthOpt                          int
	outputFilePathOpt                 string
	goProxyURLOpt                     string
	attributionsFileHeaderTemplateOpt string
	attributionsFileBlockTemplateOpt  string
	debugOpt                          bool
	showGraphOpt                      bool
)

func init() {
	rootCmd.PersistentFlags().BoolVar(&debugOpt, "debug", false, "Show debug output")
	rootCmd.PersistentFlags().BoolVar(&showGraphOpt, "show-graph", false, "Show the dependency graph in stdout")
	rootCmd.PersistentFlags().IntVar(&depthOpt, "depth", defaultDepth, "Depth of the dependency tree to explore")
	rootCmd.PersistentFlags().StringVar(&goProxyURLOpt, "go-proxy-url", defaultGoProxyURL, "Go proxy used to fetch module versions and licenses")
	rootCmd.PersistentFlags().StringVar(
		&attributionsFileHeaderTemplateOpt, "attr-header-template",
		defaultAttributionFileHeaderTemplate, "Header template used to generate the attribution file",
	)
	rootCmd.PersistentFlags().StringVar(
		&attributionsFileBlockTemplateOpt, "attr-block-template",
		defaultAttributionModuleBlockTemplate, "Module block template used to generate the attribution file",
	)
	rootCmd.PersistentFlags().StringVar(&moduleFilePathOpt, "modfile", defaultModuleFileName, "Go module file path")
	rootCmd.PersistentFlags().StringVarP(&outputFilePathOpt, "output", "o", defaultOutputFileName, "Output file name")
}

var rootCmd = &cobra.Command{
	Use:           "gen-attributions",
	SilenceUsage:  true,
	SilenceErrors: true,
	Short:         "A tool to generate attributions file for Go projects",
	RunE:          generateAttributionsFile,
}

func generateAttributionsFile(cmd *cobra.Command, args []string) error {
	logger := logrus.New()
	if debugOpt {
		logger.SetLevel(logrus.DebugLevel)
	}

	// parse the module file
	bytes, err := ioutil.ReadFile(moduleFilePathOpt)
	if err != nil {
		return fmt.Errorf("cannot read modfile %v: %v", moduleFilePathOpt, err)
	}
	goMod, err := modfile.Parse("", bytes, nil)
	if err != nil {
		return fmt.Errorf("cannot parse modfile: %v", err)
	}

	// build the dependency graph
	gb, err := newGraphBuilder(logger, defaultConfidenceTreshHold)
	if err != nil {
		return err
	}
	tree, err := gb.buildGraph(goMod, depthOpt)
	if err != nil {
		return err
	}

	if showGraphOpt {
		logger.Infof(tree.String())
	}

	// render the graph into a markdown file
	r := newRenderer(logger)
	output, err := r.generateAttributionsFiles(&AttributionsFile{
		Tree:   tree,
		Header: defaultHeader,
	})
	if err != nil {
		return fmt.Errorf("cannot generate attributions file: %v", err)
	}

	err = os.WriteFile(outputFilePathOpt, []byte(output), 0666)
	if err != nil {
		return fmt.Errorf("cannot write output to file: %v", err)
	}
	return nil
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
