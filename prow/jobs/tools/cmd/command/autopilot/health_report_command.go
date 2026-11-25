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
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/healthreport"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/policy"
)

// reportsDir is where the ledger keeps the daily reports:
// <ledger-dir>/reports/<date>.md. ledger.Lint and policy.Lint read only
// workitems/ and policy/, so the reports never fail the lint.
const reportsDir = "reports"

type healthReportOptions struct {
	LedgerDir, Date, Out string
	// Once does nothing when the ledger already holds the date's report.
	Once bool
}

var healthReportOpts healthReportOptions

var healthReportCmd = &cobra.Command{
	Use:   "health-report",
	Short: "write the daily health report for one UTC day from the ledger (read-only)",
	Long: `Reads the ledger's work items and policy (nothing else, no network) and writes
the deterministic part of the daily health report (design 10) for the UTC day
--date (default: yesterday) to --out (default: <ledger-dir>/reports/<date>.md).
The same ledger and date always give the same report. --once does nothing when
<ledger-dir>/reports/<date>.md already exists, so each day's file is written
once. Prints one summary line.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return healthReport(healthReportOpts, time.Now().UTC(), cmd.OutOrStdout())
	},
}

func init() {
	f := healthReportCmd.Flags()
	f.StringVar(&healthReportOpts.LedgerDir, "ledger-dir", "", "the autopilot/ directory of an ack-private checkout")
	f.StringVar(&healthReportOpts.Date, "date", "", "UTC day to report, YYYY-MM-DD (default: yesterday)")
	f.StringVar(&healthReportOpts.Out, "out", "", "file to write (default: <ledger-dir>/reports/<date>.md)")
	f.BoolVar(&healthReportOpts.Once, "once", false, "do nothing if <ledger-dir>/reports/<date>.md exists")
	_ = healthReportCmd.MarkFlagRequired("ledger-dir")
	Command.AddCommand(healthReportCmd)
}

// healthReport writes the report; now only picks the default date.
func healthReport(o healthReportOptions, now time.Time, out io.Writer) error {
	if err := requireDir(o.LedgerDir); err != nil {
		return err
	}
	date := now.UTC().AddDate(0, 0, -1)
	if o.Date != "" {
		d, err := time.Parse(healthreport.DateLayout, o.Date)
		if err != nil {
			return fmt.Errorf("--date %q: want YYYY-MM-DD", o.Date)
		}
		date = d
	}
	day := date.Format(healthreport.DateLayout)
	ledgerPath := filepath.Join(o.LedgerDir, reportsDir, day+".md")
	if o.Once {
		switch _, err := os.Lstat(ledgerPath); {
		case err == nil:
			fmt.Fprintf(out, "health-report %s exists; nothing written\n", ledgerPath)
			return nil
		case !errors.Is(err, fs.ErrNotExist):
			return err
		}
	}
	items, err := ledger.Load(o.LedgerDir)
	if err != nil {
		return err
	}
	var polPtr *policy.Policy
	pol, polErr := policy.Load(o.LedgerDir)
	if polErr == nil {
		polPtr = &pol
	}
	text, sum := healthreport.Render(items, polPtr, polErr, date)
	path := o.Out
	if path == "" {
		path = ledgerPath
		if err := ensureReportsDir(filepath.Join(o.LedgerDir, reportsDir)); err != nil {
			return err
		}
	}
	if err := writeReport(path, []byte(text)); err != nil {
		return err
	}
	fmt.Fprintf(out, "health-report %s date=%s items=%d runs-completed=%d runs-failed=%d escalated=%d gate-first-pass=%d gate-first-fail=%d\n",
		path, sum.Date, sum.Items, sum.RunsCompleted, sum.RunsFailed, sum.Escalated, sum.GateFirstPass, sum.GateFirstFail)
	return nil
}

// ensureReportsDir creates dir, refusing a symlink (or file) in its place so
// a hand-edited ledger cannot redirect the write, as ledger.Save refuses.
func ensureReportsDir(dir string) error {
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return os.Mkdir(dir, 0o755)
	case err != nil:
		return err
	case !fi.IsDir():
		return fmt.Errorf("%s: must be a real directory, not a symlink or file", dir)
	}
	return nil
}

// writeReport writes b to path through a temp file and a rename, replacing a
// regular file but never following a symlink at path.
func writeReport(path string, b []byte) error {
	if fi, err := os.Lstat(path); err == nil && !fi.Mode().IsRegular() {
		return fmt.Errorf("%s: exists and is not a regular file", path)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".report-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o644)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}
