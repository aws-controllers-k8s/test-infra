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
	Description   string            `yaml:"description" json:"description"`
	Image         string            `yaml:"image" json:"image,omitempty"`
	Command       []string          `yaml:"command" json:"command"`
	RequiredArgs  []string          `yaml:"required_args" json:"required_args"`
	OptionalArgs  []string          `yaml:"optional_args" json:"optional_args"`
	Timeout       string            `yaml:"timeout" json:"timeout"`
	TimeoutDur    time.Duration     `yaml:"-" json:"-"`
	Environment   map[string]string `yaml:"environment,omitempty" json:"environment,omitempty"`
	Resources     *ResourceLimits   `yaml:"resources,omitempty" json:"resources,omitempty"`
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