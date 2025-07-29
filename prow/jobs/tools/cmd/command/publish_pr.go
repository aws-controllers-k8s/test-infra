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
	"log"

	"github.com/spf13/cobra"
)

var (
	OptBaseBranch    string
	OptPrSubject     string
	OptPrDescription string
	OptSourceFiles   string
	OptCommitBranch  string
)

var publishPrCmd = &cobra.Command{
	Use:   "publish-pr",
	Short: "publish-pr - commits changes to a new branch and opens a PR against the specified main branch",
	RunE:  publishPr,
}

func init() {
	publishPrCmd.PersistentFlags().StringVar(
		&OptCommitBranch, "commit-branch", "", "Branch to commit changes to and open a PR with",
	)
	publishPrCmd.PersistentFlags().StringVar(
		&OptPrSubject, "subject", "", "Subject of the PR",
	)
	publishPrCmd.PersistentFlags().StringVar(
		&OptPrDescription, "description", "", "path to jobs.yaml where the generated jobs will be stored",
	)
	publishPrCmd.PersistentFlags().StringVar(
		&OptSourceFiles, "source-files", "", "Source files to commit in the PR branch",
	)
	rootCmd.AddCommand(publishPrCmd)
}

func publishPr(cmd *cobra.Command, args []string) error {
	log.SetPrefix("publish-pr")
	log.Printf("Attempting to publish PR for branch %s to %s/%s", OptCommitBranch, OptSourceOwner, OptSourceRepo)
	err := commitAndSendPR(
		OptSourceOwner,
		OptSourceRepo,
		OptCommitBranch,
		OptSourceFiles,
		baseBranch,
		OptPrSubject,
		OptPrDescription,
	)
	if err != nil {
		return err
	}
	log.Printf("Successfully published PR for branch %s to %s/%s", OptCommitBranch, OptSourceOwner, OptSourceRepo)
	return nil
}
