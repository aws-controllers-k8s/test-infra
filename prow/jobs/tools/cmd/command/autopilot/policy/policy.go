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

// Package policy reads the ACK Autopilot Dispatcher's policy files from the
// ledger: policy/autopilot.yaml (switches) and policy/budgets.yaml (caps).
// People change them by pull request; jobs only read them. Anything missing
// or malformed is an error, and the Dispatcher then launches nothing.
package policy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	SwitchesFile = "policy/autopilot.yaml"
	BudgetsFile  = "policy/budgets.yaml"
	// LauncherShadow records launches and starts nothing.
	LauncherShadow = "shadow"
	// LauncherProw creates ProwJobs (Plan 7). The dispatch command also
	// requires AUTOPILOT_LAUNCHER_ENABLED=true in the job's environment, so
	// the ledger's policy alone cannot start real runs.
	LauncherProw = "prow"
	// maxJobTimeoutMinutes is Prow's hard limit for an agent job (design 5.4).
	maxJobTimeoutMinutes  = 360
	minReaperGraceMinutes = 30
)

type Switches struct {
	// Paused stops every launch. Ingest, reconcile and routing still run.
	Paused bool `yaml:"paused"`
	// PausedRepos ("owner/name") are never launched.
	PausedRepos []string `yaml:"paused_repos"`
	Launcher    string   `yaml:"launcher"`
	// Services is the allowlist of services whose items may be launched.
	Services []string `yaml:"services"`
	// AbortInflight aborts every run in flight on each tick, escalates its
	// item (ABORTED_BY_POLICY) and launches nothing. Only the prow launcher
	// aborts; the shadow launcher reports what it would abort.
	AbortInflight bool `yaml:"abort_inflight"`
}

type Budgets struct {
	MaxConcurrentLLMRuns       int      `yaml:"max_concurrent_llm_runs"`
	MaxConcurrentRunsPerRepo   int      `yaml:"max_concurrent_runs_per_repo"`
	MaxOpenPRsPerRepoClass     int      `yaml:"max_open_prs_per_repo_class"`
	MaxPRsAwaitingReview       int      `yaml:"max_prs_awaiting_review"`
	MaxSDKBumpLaunchesPerDay   int      `yaml:"max_sdk_bump_launches_per_day"`
	ExpensiveServices          []string `yaml:"expensive_services"`
	MaxExpensiveLaunchesPerDay int      `yaml:"max_expensive_launches_per_service_per_day"`
	MaxRunsPerItem             int      `yaml:"max_runs_per_item"`
	JobTimeoutMinutes          int      `yaml:"job_timeout_minutes"`
	ReaperGraceMinutes         int      `yaml:"reaper_grace_minutes"`
}

type Policy struct {
	Switches Switches
	Budgets  Budgets
}

func (s Switches) ServiceEnabled(svc string) bool { return slices.Contains(s.Services, svc) }
func (s Switches) RepoPaused(repo string) bool    { return slices.Contains(s.PausedRepos, repo) }
func (b Budgets) Expensive(svc string) bool       { return slices.Contains(b.ExpensiveServices, svc) }
func (b Budgets) JobTimeout() time.Duration       { return time.Duration(b.JobTimeoutMinutes) * time.Minute }
func (b Budgets) ReaperGrace() time.Duration {
	return time.Duration(b.ReaperGraceMinutes) * time.Minute
}

// Load reads and validates both files under root (the ledger's autopilot/
// directory).
func Load(root string) (Policy, error) {
	p, errs := load(root)
	if len(errs) > 0 {
		return Policy{}, errors.Join(errs...)
	}
	return p, nil
}

// Lint returns one error per policy problem, for lint-ledger: the decode error,
// or every validation error.
func Lint(root string) []error {
	_, errs := load(root)
	return errs
}

func load(root string) (Policy, []error) {
	var p Policy
	// Lstat only checks the last path element, so check the directory too.
	di, err := os.Lstat(filepath.Join(root, "policy"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Policy{}, []error{err}
	}
	// A missing directory falls through so the error names the missing file.
	if err == nil && !di.IsDir() {
		return Policy{}, []error{fmt.Errorf("%s: must be a regular directory, not a symlink", filepath.Join(root, "policy"))}
	}
	if err := decodeFile(filepath.Join(root, SwitchesFile), &p.Switches); err != nil {
		return Policy{}, []error{err}
	}
	if err := decodeFile(filepath.Join(root, BudgetsFile), &p.Budgets); err != nil {
		return Policy{}, []error{err}
	}
	if errs := p.validate(); len(errs) > 0 {
		return Policy{}, errs
	}
	return p, nil
}

func decodeFile(path string, v any) error {
	// Like the work-item store, never follow a link out of the checkout.
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s: must be a regular file, not a symlink or directory", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.NewDecoder(bytes.NewReader(raw)).Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: %w", path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%s: empty", path)
		}
		return fmt.Errorf("%s: %w", path, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: more than one YAML document", path)
	}
	return requireKeys(path, &doc, v)
}

// requireKeys fails if the document lacks any key the struct's yaml tags name,
// so a deleted switch or cap never silently becomes its zero value.
func requireKeys(path string, doc *yaml.Node, v any) error {
	top := doc
	if top.Kind == yaml.DocumentNode && len(top.Content) > 0 {
		top = top.Content[0]
	}
	if top.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: top level must be a mapping", path)
	}
	present := map[string]bool{}
	for i := 0; i+1 < len(top.Content); i += 2 {
		present[top.Content[i].Value] = true
	}
	t := reflect.TypeOf(v).Elem()
	var errs []error
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
		if name == "" || name == "-" {
			continue
		}
		if !present[name] {
			errs = append(errs, fmt.Errorf("%s: missing key %q", path, name))
		}
	}
	return errors.Join(errs...)
}

func (p Policy) validate() []error {
	var errs []error
	bad := func(file, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", file, fmt.Sprintf(format, args...)))
	}
	s, b := p.Switches, p.Budgets
	if s.Launcher != LauncherShadow && s.Launcher != LauncherProw {
		bad(SwitchesFile, "launcher %q is not supported (%q or %q)", s.Launcher, LauncherShadow, LauncherProw)
	}
	for _, svc := range s.Services {
		if svc == "" || strings.ContainsAny(svc, "|/ \t") {
			bad(SwitchesFile, "service %q is not a bare service name", svc)
		}
	}
	for _, svc := range b.ExpensiveServices {
		if svc == "" || strings.ContainsAny(svc, "|/ \t") {
			bad(BudgetsFile, "expensive service %q is not a bare service name", svc)
		}
	}
	for _, r := range s.PausedRepos {
		if owner, name, ok := strings.Cut(r, "/"); !ok || owner == "" || name == "" || strings.Contains(name, "/") {
			bad(SwitchesFile, "paused repo %q is not owner/name", r)
		}
	}
	for _, c := range []struct {
		name string
		v    int
	}{
		{"max_concurrent_llm_runs", b.MaxConcurrentLLMRuns},
		{"max_concurrent_runs_per_repo", b.MaxConcurrentRunsPerRepo},
		{"max_open_prs_per_repo_class", b.MaxOpenPRsPerRepoClass},
		{"max_prs_awaiting_review", b.MaxPRsAwaitingReview},
		{"max_sdk_bump_launches_per_day", b.MaxSDKBumpLaunchesPerDay},
		{"max_expensive_launches_per_service_per_day", b.MaxExpensiveLaunchesPerDay},
	} {
		if c.v < 0 {
			bad(BudgetsFile, "%s must not be negative", c.name)
		}
	}
	if b.MaxRunsPerItem < 1 {
		bad(BudgetsFile, "max_runs_per_item must be at least 1")
	}
	if b.JobTimeoutMinutes < 1 || b.JobTimeoutMinutes > maxJobTimeoutMinutes {
		bad(BudgetsFile, "job_timeout_minutes must be 1..%d", maxJobTimeoutMinutes)
	}
	if b.ReaperGraceMinutes < minReaperGraceMinutes {
		bad(BudgetsFile, "reaper_grace_minutes must be at least %d", minReaperGraceMinutes)
	}
	return errs
}
