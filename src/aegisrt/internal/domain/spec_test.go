package domain

import (
	"testing"
	"time"
)

func validAgentSpec() AgentSpec {
	return AgentSpec{
		ID:               "agent-1",
		Name:             "General Agent",
		Runtime:          "process",
		Entrypoint:       "python worker.py",
		Args:             []string{"--once"},
		Capabilities:     []string{"code-analysis", "shell"},
		Resources:        ResourceVector{CPUMilli: 100, MemoryMiB: 64},
		MaxConcurrency:   1,
		ExecutionTimeout: time.Minute,
	}
}

func validTaskSpec(id string, dependencies ...string) TaskSpec {
	return TaskSpec{
		ID:             id,
		WorkflowID:     "workflow-1",
		AgentID:        "agent-1",
		Goal:           "Perform a deterministic unit of work",
		Dependencies:   dependencies,
		Priority:       10,
		Resources:      ResourceVector{CPUMilli: 100, ToolSlots: map[string]int64{"shell": 1}},
		MaxAttempts:    2,
		Timeout:        time.Minute,
		IdempotencyKey: "key-" + id,
	}
}

func TestAgentSpecValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*AgentSpec)
		valid  bool
	}{
		{name: "valid", mutate: func(*AgentSpec) {}, valid: true},
		{name: "missing id", mutate: func(s *AgentSpec) { s.ID = "" }},
		{name: "invalid id", mutate: func(s *AgentSpec) { s.ID = "agent/1" }},
		{name: "blank name", mutate: func(s *AgentSpec) { s.Name = " " }},
		{name: "missing runtime", mutate: func(s *AgentSpec) { s.Runtime = "" }},
		{name: "missing entrypoint", mutate: func(s *AgentSpec) { s.Entrypoint = "" }},
		{name: "blank arg", mutate: func(s *AgentSpec) { s.Args = []string{" "} }},
		{name: "duplicate capability", mutate: func(s *AgentSpec) { s.Capabilities = []string{"shell", "shell"} }},
		{name: "bad resources", mutate: func(s *AgentSpec) { s.Resources.CPUMilli = -1 }},
		{name: "zero concurrency", mutate: func(s *AgentSpec) { s.MaxConcurrency = 0 }},
		{name: "zero timeout", mutate: func(s *AgentSpec) { s.ExecutionTimeout = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := validAgentSpec()
			test.mutate(&spec)
			if err := spec.Validate(); (err == nil) != test.valid {
				t.Fatalf("Validate() error = %v, valid = %v", err, test.valid)
			}
		})
	}
}

func TestTaskSpecValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*TaskSpec)
		valid  bool
	}{
		{name: "valid", mutate: func(*TaskSpec) {}, valid: true},
		{name: "missing task id", mutate: func(s *TaskSpec) { s.ID = "" }},
		{name: "missing workflow", mutate: func(s *TaskSpec) { s.WorkflowID = "" }},
		{name: "missing agent", mutate: func(s *TaskSpec) { s.AgentID = "" }},
		{name: "blank goal", mutate: func(s *TaskSpec) { s.Goal = "\n" }},
		{name: "duplicate dependency", mutate: func(s *TaskSpec) { s.Dependencies = []string{"root", "root"} }},
		{name: "self dependency", mutate: func(s *TaskSpec) { s.Dependencies = []string{s.ID} }},
		{name: "negative priority", mutate: func(s *TaskSpec) { s.Priority = -1 }},
		{name: "bad resources", mutate: func(s *TaskSpec) { s.Resources.TokenBudget = -1 }},
		{name: "zero attempts", mutate: func(s *TaskSpec) { s.MaxAttempts = 0 }},
		{name: "zero timeout", mutate: func(s *TaskSpec) { s.Timeout = 0 }},
		{name: "bad idempotency key", mutate: func(s *TaskSpec) { s.IdempotencyKey = "key/value" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := validTaskSpec("task-1", "root")
			test.mutate(&spec)
			if err := spec.Validate(); (err == nil) != test.valid {
				t.Fatalf("Validate() error = %v, valid = %v", err, test.valid)
			}
		})
	}
}

func TestTaskSpecCloneDoesNotAlias(t *testing.T) {
	source := validTaskSpec("task-1", "root")
	clone := source.Clone()
	clone.Dependencies[0] = "changed"
	clone.Resources.ToolSlots["shell"] = 9
	if source.Dependencies[0] != "root" || source.Resources.ToolSlots["shell"] != 1 {
		t.Fatal("Clone aliases mutable TaskSpec fields")
	}
}
