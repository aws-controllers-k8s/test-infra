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

package autopilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dispatcher"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/policy"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/prowlaunch"
)

// launcherEnabledEnv must be "true" in the job's environment before the prow
// launcher is used. The autopilot-dispatch periodic sets it to "false".
// Flipping it is a reviewed change to test-infra, separate from the ledger's
// policy/autopilot.yaml, so neither alone starts real runs.
const launcherEnabledEnv = "AUTOPILOT_LAUNCHER_ENABLED"

// getenv and newProwLauncher are replaced in tests.
var (
	getenv          = os.Getenv
	newProwLauncher = func(ctx context.Context, testInfraDir string, env func(string) string) (dispatcher.Launcher, error) {
		l, err := prowlaunch.NewInCluster(ctx, testInfraDir, env)
		if err != nil {
			return nil, err
		}
		return l, nil
	}
)

// launcherFor is the launcher the policy names, if this job may use it.
func launcherFor(ctx context.Context, p policy.Policy, testInfraDir string, env func(string) string) (dispatcher.Launcher, error) {
	if p.Switches.Launcher != policy.LauncherProw {
		return dispatcher.NewLauncher(p)
	}
	if env(launcherEnabledEnv) != "true" {
		return nil, fmt.Errorf("policy names launcher %q but %s is not \"true\" in this job", policy.LauncherProw, launcherEnabledEnv)
	}
	if testInfraDir == "" {
		return nil, errors.New("the prow launcher needs --test-infra-dir")
	}
	return newProwLauncher(ctx, testInfraDir, env)
}

type previewOptions struct {
	LedgerDir, KeyHash, TestInfraDir, ImagesRepo, Org, Bucket string
}

var previewOpts previewOptions

var launchPreviewCmd = &cobra.Command{
	Use:   "launch-preview",
	Short: "print the ProwJob the prow launcher would create for a QUEUED item's next run",
	Long: `Reads the ledger and policy (read only) and prints, as JSON, the ProwJob the
prow launcher would build for the item's next run, ignoring caps. Talks to no
cluster and no GitHub. --images-repo, --org and --bucket stand in for the
values a Dispatcher job reads from its environment.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return launchPreview(previewOpts, cmd.OutOrStdout())
	},
}

func init() {
	dispatchCmd.Flags().StringVar(&dispatchOpts.TestInfraDir, "test-infra-dir", "", "test-infra checkout the prow launcher reads agent-workflows.yaml and images_config.yaml from")
	f := launchPreviewCmd.Flags()
	f.StringVar(&previewOpts.LedgerDir, "ledger-dir", "", "the autopilot/ directory of an ack-private checkout")
	f.StringVar(&previewOpts.KeyHash, "key-hash", "", "the item's key hash (sha256:<16 hex>)")
	f.StringVar(&previewOpts.TestInfraDir, "test-infra-dir", ".", "test-infra checkout")
	f.StringVar(&previewOpts.ImagesRepo, "images-repo", "registry.example/prow", "stands in for PROW_IMAGES_REPO_URI")
	f.StringVar(&previewOpts.Org, "org", "aws-controllers-k8s", "stands in for TEST_INFRA_ORG")
	f.StringVar(&previewOpts.Bucket, "bucket", "s3://example-prow-logs", "stands in for the Prow logs bucket")
	_ = launchPreviewCmd.MarkFlagRequired("ledger-dir")
	_ = launchPreviewCmd.MarkFlagRequired("key-hash")
	Command.AddCommand(launchPreviewCmd)
}

func launchPreview(o previewOptions, out io.Writer) error {
	if err := requireDir(o.LedgerDir); err != nil {
		return err
	}
	items, err := ledger.Load(o.LedgerDir)
	if err != nil {
		return err
	}
	pol, err := policy.Load(o.LedgerDir)
	if err != nil {
		return err
	}
	var w *ledger.WorkItem
	for _, it := range items {
		if it.KeyHash == o.KeyHash {
			w = it
		}
	}
	if w == nil {
		return fmt.Errorf("no item with key hash %s", o.KeyHash)
	}
	req, ok := dispatcher.NextLaunch(w, pol.Budgets)
	if !ok {
		return fmt.Errorf("%s is %s with class %s: nothing to launch", o.KeyHash, w.State, w.ChangeClass)
	}
	cat, err := prowlaunch.FromCheckout(o.TestInfraDir, prowlaunch.Vars{ImagesRepo: o.ImagesRepo, TestInfraOrg: o.Org})
	if err != nil {
		return err
	}
	pj, err := cat.Build(prowlaunch.Config{Bucket: o.Bucket, Org: o.Org, Now: time.Now}, req)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(pj, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "%s\n", b)
	return err
}
