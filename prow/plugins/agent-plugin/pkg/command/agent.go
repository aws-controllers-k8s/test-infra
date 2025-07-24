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

package command

import (
	"fmt"
	"strings"
	"time"
)

// AgentCommand represents a parsed /agent command
type AgentCommand struct {
	WorkflowName string
	Args         map[string]string
	Flags        []string
}

// ValidateRequiredArgs validates that all required arguments are present
func (c *AgentCommand) ValidateRequiredArgs(required []string) error {
	var missing []string
	for _, arg := range required {
		if val, exists := c.Args[arg]; !exists || strings.TrimSpace(val) == "" {
			missing = append(missing, arg)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required arguments: %s", strings.Join(missing, ", "))
	}
	return nil
}

// ValidateTimeout validates the timeout format if present
func (c *AgentCommand) ValidateTimeout() error {
	timeout := c.GetTimeout()
	if timeout != "" {
		if _, err := time.ParseDuration(timeout); err != nil {
			return fmt.Errorf("invalid timeout format '%s': %v", timeout, err)
		}
	}
	return nil
}

// GetTimeout extracts timeout value from flags
func (c *AgentCommand) GetTimeout() string {
	for i, flag := range c.Flags {
		if flag == "--timeout" && i+1 < len(c.Flags) {
			return c.Flags[i+1]
		}
		if strings.HasPrefix(flag, "--timeout=") {
			return strings.TrimPrefix(flag, "--timeout=")
		}
	}
	return ""
}

// ParseAgentCommand parses a GitHub comment for /agent commands
// Format: /agent <workflow-name> [key=value ...] [--timeout 30m]
func ParseAgentCommand(comment string) (*AgentCommand, error) {
	comment = strings.TrimSpace(comment)
	parts := strings.Fields(comment)

	if len(parts) < 2 || parts[0] != "/agent" {
		return nil, fmt.Errorf("not a valid agent command")
	}

	cmd := &AgentCommand{
		WorkflowName: parts[1],
		Args:         make(map[string]string),
		Flags:        []string{},
	}

	// Parse remaining parts as arguments and flags
	for i := 2; i < len(parts); i++ {
		part := parts[i]
		if strings.HasPrefix(part, "--timeout") {
			if strings.Contains(part, "=") {
				cmd.Flags = append(cmd.Flags, part)
			} else if i+1 < len(parts) && !strings.HasPrefix(parts[i+1], "--") {
				cmd.Flags = append(cmd.Flags, part, parts[i+1])
				i++
			} else {
				return nil, fmt.Errorf("--timeout flag requires a value")
			}
		} else if strings.HasPrefix(part, "--") {
			// Other flags
			cmd.Flags = append(cmd.Flags, part)
		} else if strings.Contains(part, "=") {
			// Key=value arguments
			kv := strings.SplitN(part, "=", 2)
			if len(kv) == 2 && strings.TrimSpace(kv[0]) != "" && strings.TrimSpace(kv[1]) != "" {
				cmd.Args[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
			} else {
				return nil, fmt.Errorf("invalid argument format: %s", part)
			}
		} else {
			return nil, fmt.Errorf("invalid argument format: %s (expected key=value or --flag)", part)
		}
	}

	return cmd, nil
}
