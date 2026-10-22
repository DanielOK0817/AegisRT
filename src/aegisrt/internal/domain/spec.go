package domain

import (
	"fmt"
	"strings"
	"time"
)

// AgentSpec declares a reusable agent role without embedding business names in
// the runtime. Runtime identifies the executor implementation (for example,
// process or container) and Entrypoint identifies what it launches.
type AgentSpec struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	Runtime          string         `json:"runtime"`
	Entrypoint       string         `json:"entrypoint"`
	Args             []string       `json:"args,omitempty"`
	Capabilities     []string       `json:"capabilities,omitempty"`
	Resources        ResourceVector `json:"resources"`
	MaxConcurrency   int            `json:"max_concurrency"`
	ExecutionTimeout time.Duration  `json:"execution_timeout"`
}

func (s AgentSpec) Validate() error {
	if err := validateIdentifier("agent id", s.ID); err != nil {
		return err
	}
	if err := validateText("agent name", s.Name, maxNameLength); err != nil {
		return err
	}
	if err := validateIdentifier("runtime", s.Runtime); err != nil {
		return err
	}
	if err := validateText("entrypoint", s.Entrypoint, 2048); err != nil {
		return err
	}
	for index, arg := range s.Args {
		if strings.TrimSpace(arg) == "" {
			return fmt.Errorf("args[%d] must not be blank", index)
		}
	}
	if err := validateUniqueIdentifiers("capability", s.Capabilities); err != nil {
		return err
	}
	if err := s.Resources.Validate(); err != nil {
		return fmt.Errorf("resources: %w", err)
	}
	if s.MaxConcurrency <= 0 {
		return fmt.Errorf("max_concurrency must be positive")
	}
	if s.ExecutionTimeout <= 0 {
		return fmt.Errorf("execution_timeout must be positive")
	}
	return nil
}

// TaskSpec is immutable once admitted to a TaskGraph.
type TaskSpec struct {
	ID             string         `json:"id"`
	WorkflowID     string         `json:"workflow_id"`
	AgentID        string         `json:"agent_id"`
	Goal           string         `json:"goal"`
	Dependencies   []string       `json:"dependencies,omitempty"`
	Priority       int            `json:"priority"`
	Resources      ResourceVector `json:"resources"`
	MaxAttempts    int            `json:"max_attempts"`
	Timeout        time.Duration  `json:"timeout"`
	IdempotencyKey string         `json:"idempotency_key"`
}

func (s TaskSpec) Validate() error {
	if err := validateIdentifier("task id", s.ID); err != nil {
		return err
	}
	if err := validateIdentifier("workflow id", s.WorkflowID); err != nil {
		return err
	}
	if err := validateIdentifier("agent id", s.AgentID); err != nil {
		return err
	}
	if err := validateText("goal", s.Goal, 8192); err != nil {
		return err
	}
	if err := validateUniqueIdentifiers("dependency", s.Dependencies); err != nil {
		return err
	}
	for _, dependency := range s.Dependencies {
		if dependency == s.ID {
			return fmt.Errorf("task %q must not depend on itself", s.ID)
		}
	}
	if s.Priority < 0 {
		return fmt.Errorf("priority must be non-negative")
	}
	if err := s.Resources.Validate(); err != nil {
		return fmt.Errorf("resources: %w", err)
	}
	if s.MaxAttempts <= 0 {
		return fmt.Errorf("max_attempts must be positive")
	}
	if s.Timeout <= 0 {
		return fmt.Errorf("timeout must be positive")
	}
	if err := validateIdentifier("idempotency key", s.IdempotencyKey); err != nil {
		return err
	}
	return nil
}

func (s TaskSpec) Clone() TaskSpec {
	result := s
	result.Dependencies = append([]string(nil), s.Dependencies...)
	result.Resources = s.Resources.Clone()
	return result
}
