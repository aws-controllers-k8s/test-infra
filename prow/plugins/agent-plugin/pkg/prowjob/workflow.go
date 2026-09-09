// Copyright 2020 Amazon.com Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package prowjob

import (
	"fmt"
	"os"
	"time"

	"sigs.k8s.io/yaml"
)

// WorkflowConfig represents the configuration for all workflows
type WorkflowConfig struct {
	Workflows map[string]*Workflow `yaml:"workflows"`
}

// Workflow represents a single workflow configuration
type Workflow struct {
	Description            string                   `yaml:"description" json:"description"`
	Image                  string                   `yaml:"image" json:"image,omitempty"`
	Command                []string                 `yaml:"command" json:"command"`
	RequiredArgs           []string                 `yaml:"required_args" json:"required_args"`
	OptionalArgs           []string                 `yaml:"optional_args" json:"optional_args"`
	Timeout                string                   `yaml:"timeout" json:"timeout"`
	TimeoutDur             time.Duration            `yaml:"-" json:"-"`
	Environment            map[string]string        `yaml:"environment,omitempty" json:"environment,omitempty"`
	EnvironmentFromSecrets map[string]*SecretKeyRef `yaml:"environmentFromSecrets,omitempty" json:"environmentFromSecrets,omitempty"`
	Resources              *ResourceLimits          `yaml:"resources,omitempty" json:"resources,omitempty"`
	// ExtraRefs are stable repo dependencies (e.g. code-generator, runtime,
	// ack-dev-skills) mounted into the workflow pod by Prow's clonerefs init
	// container. The dynamic service controller is NOT listed here — prow-job.sh
	// forks and clones it per run. See ExtraRef.
	ExtraRefs []ExtraRef `yaml:"extra_refs,omitempty" json:"extra_refs,omitempty"`
	// E2E, when true, has the generator provision the pod for kind-in-Docker-in-Docker:
	// privileged securityContext, DinD/kind volumes, and RUN_E2E. These mirror the
	// preset-dind-enabled / preset-kind-volume-mounts presets but must be inlined —
	// preset merging runs only in Prow's config loader, which submitting ProwJob CRs
	// directly bypasses.
	E2E bool `yaml:"e2e,omitempty" json:"e2e,omitempty"`
}

type SecretKeyRef struct {
	Name string `yaml:"name" json:"name"`
	Key  string `yaml:"key" json:"key"`
}

// ExtraRef declares a stable git repository dependency to clone into the
// workflow pod as a Prow extra_ref (via the clonerefs init container).
type ExtraRef struct {
	Org  string `yaml:"org" json:"org"`
	Repo string `yaml:"repo" json:"repo"`
	// BaseRef is the branch/tag/sha to check out. Defaults to "main".
	BaseRef string `yaml:"base_ref,omitempty" json:"base_ref,omitempty"`
	// PathAlias overrides the checkout subpath under the clone root. Defaults to
	// "github.com/<org>/<repo>".
	PathAlias string `yaml:"path_alias,omitempty" json:"path_alias,omitempty"`
	// Env, if set, names an environment variable injected into the workflow
	// container with this ref's absolute checkout path. This keeps the path the
	// workflow reads (e.g. CODEGEN_DIR) in sync with where clonerefs checks the
	// repo out — both are derived from the same org/repo/path_alias.
	Env string `yaml:"env,omitempty" json:"env,omitempty"`
}

// ResourceLimits defines resource constraints for workflows
type ResourceLimits struct {
	CPU    string `yaml:"cpu,omitempty"`
	Memory string `yaml:"memory,omitempty"`
}

// LoadWorkflowConfig loads a workflow configuration from a file
func LoadWorkflowConfig(path string) (*WorkflowConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
	}

	var config WorkflowConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	for name := range config.Workflows {
		w := config.Workflows[name]

		if w.Timeout != "" {
			dur, err := time.ParseDuration(w.Timeout)
			if err != nil {
				return nil, fmt.Errorf("invalid timeout for workflow %s: %w", name, err)
			}
			w.TimeoutDur = dur
		} else {
			w.TimeoutDur = 30 * time.Minute
		}

		if err := w.ValidateWorkflow(); err != nil {
			return nil, fmt.Errorf("invalid workflow %s: %w", name, err)
		}
	}

	return &config, nil
}

// ValidateWorkflow validates the workflow configuration
func (w *Workflow) ValidateWorkflow() error {
	if w == nil {
		return fmt.Errorf("workflow is nil")
	}
	if w.Image == "" {
		return fmt.Errorf("workflow image cannot be empty")
	}
	if len(w.Command) == 0 {
		return fmt.Errorf("workflow command cannot be empty")
	}
	return nil
}

// GetWorkflowByName retrieves a workflow by name
func (wc *WorkflowConfig) GetWorkflowByName(name string) (*Workflow, error) {
	workflow, exists := wc.Workflows[name]
	if !exists {
		return nil, fmt.Errorf("workflow %s not found", name)
	}
	return workflow, nil
}

// GetWorkflowsMap returns the map of workflows
func (wc *WorkflowConfig) GetWorkflowsMap() map[string]*Workflow {
	return wc.Workflows
}

// WorkflowConfigLoader reads and parses the workflow config from disk on every
// Get(), so the plugin picks up ConfigMap updates without a pod restart:
// kubelet swaps the mounted file atomically via a symlink, and the next Get()
// reads the new content.
//
// It deliberately does NOT cache a previously-parsed config. Serving a stale
// config on a read/parse error would hide a broken ConfigMap — the plugin would
// keep generating jobs from old config while the new one silently failed to
// load. Instead every error is surfaced so a bad config fails the /agent
// command loudly. Reads are cheap: /agent commands are infrequent and the file
// is small.
type WorkflowConfigLoader struct {
	path string
}

// NewWorkflowConfigLoader returns a loader for the given path. It does not read
// the file; callers should invoke Get() once at startup to fail fast on a bad
// path or content.
func NewWorkflowConfigLoader(path string) *WorkflowConfigLoader {
	return &WorkflowConfigLoader{path: path}
}

// Get reads and parses the workflow config from disk. Any read/parse/validation
// error is returned so a broken ConfigMap is not masked by a stale config.
func (l *WorkflowConfigLoader) Get() (*WorkflowConfig, error) {
	return LoadWorkflowConfig(l.path)
}

// GetWorkflowByName reads the current config from disk and looks up a workflow
// by name.
func (l *WorkflowConfigLoader) GetWorkflowByName(name string) (*Workflow, error) {
	cfg, err := l.Get()
	if err != nil {
		return nil, err
	}
	return cfg.GetWorkflowByName(name)
}
