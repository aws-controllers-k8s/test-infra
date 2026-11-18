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
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod"
)

var dodKeygenPrivateOut string

var dodKeygenCmd = &cobra.Command{
	Use:   "dod-keygen",
	Short: "make the Ed25519 key that signs dod verdicts",
	Long: `Writes a new Ed25519 seed (base64) to --private-out with mode 0600, refusing
to overwrite any existing file, and prints the public key (base64) on stdout.
Store the seed as the secret autopilot-dod-signing-key (data key "key") and
the public key as AUTOPILOT_DOD_PUBLIC_KEY in the dispatch periodic.`,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runDodKeygen(dodKeygenPrivateOut, cmd.OutOrStdout())
	},
}

func init() {
	dodKeygenCmd.Flags().StringVar(&dodKeygenPrivateOut, "private-out", "", "write the private seed here (mode 0600; must not exist)")
	_ = dodKeygenCmd.MarkFlagRequired("private-out")
	Command.AddCommand(dodKeygenCmd)
}

func runDodKeygen(path string, out io.Writer) (err error) {
	if path == "" {
		return errors.New("--private-out is required")
	}
	seed, pub, err := dod.GenerateKey()
	if err != nil {
		return err
	}
	// O_EXCL: never overwrite, and never follow a symlink at path.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("--private-out: %w", err)
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	if err = f.Chmod(0o600); err != nil {
		return err
	}
	if _, err = io.WriteString(f, seed+"\n"); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, pub)
	return err
}
