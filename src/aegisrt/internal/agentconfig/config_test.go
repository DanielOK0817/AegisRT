package agentconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestLoadValidConfig(t *testing.T) {
	entrypoint := currentExecutable(t)
	path := writeDocument(t, []map[string]any{validAgent(entrypoint)})
	config, err := Load(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	agents := config.Agents()
	if len(agents) != 1 {
		t.Fatalf("agent count = %d, want 1", len(agents))
	}
	resolved, err := filepath.EvalSymlinks(entrypoint)
	if err != nil {
		t.Fatal(err)
	}
	agent := agents[0]
	if agent.ID != "agent-1" || agent.Entrypoint != filepath.Clean(resolved) ||
		agent.ExecutionTimeout != 2*time.Second || agent.MaxConcurrency != 2 {
		t.Fatalf("unexpected agent: %+v", agent)
	}
}

func TestLoadRejectsDuplicateAgent(t *testing.T) {
	agent := validAgent(currentExecutable(t))
	path := writeDocument(t, []map[string]any{agent, agent})
	if _, err := Load(path, Options{}); !errors.Is(err, ErrDuplicateAgent) {
		t.Fatalf("error = %v, want ErrDuplicateAgent", err)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	agent := validAgent(currentExecutable(t))
	agent["environment"] = map[string]string{"SECRET": "not-allowed"}
	path := writeDocument(t, []map[string]any{agent})
	if _, err := Load(path, Options{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v, want ErrInvalidConfig", err)
	}
}

func TestLoadRejectsBadDuration(t *testing.T) {
	agent := validAgent(currentExecutable(t))
	agent["execution_timeout"] = "eventually"
	path := writeDocument(t, []map[string]any{agent})
	if _, err := Load(path, Options{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v, want ErrInvalidConfig", err)
	}
}

func TestLoadRejectsRelativeEntrypoint(t *testing.T) {
	agent := validAgent("relative-agent")
	path := writeDocument(t, []map[string]any{agent})
	_, err := Load(path, Options{})
	if !errors.Is(err, ErrInvalidEntrypoint) {
		t.Fatalf("error = %v, want ErrInvalidEntrypoint", err)
	}
}

func TestLoadRejectsEntrypointOutsideRoot(t *testing.T) {
	agent := validAgent(currentExecutable(t))
	path := writeDocument(t, []map[string]any{agent})
	_, err := Load(path, Options{AgentRoot: t.TempDir()})
	if !errors.Is(err, ErrOutsideAgentRoot) {
		t.Fatalf("error = %v, want ErrOutsideAgentRoot", err)
	}
}

func TestLoadRejectsOversizedFile(t *testing.T) {
	path := writeDocument(t, []map[string]any{validAgent(currentExecutable(t))})
	_, err := Load(path, Options{MaxFileBytes: 16})
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("error = %v, want ErrFileTooLarge", err)
	}
	if _, err = Load(path, Options{MaxFileBytes: MaximumMaxFileBytes + 1}); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("error = %v, want ErrInvalidOptions", err)
	}
}

func TestAgentsReturnsDeepCopies(t *testing.T) {
	path := writeDocument(t, []map[string]any{validAgent(currentExecutable(t))})
	config, err := Load(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	first := config.Agents()
	first[0].Args[0] = "changed"
	first[0].Capabilities[0] = "changed"
	first[0].Resources.ToolSlots["scanner"] = 99
	first = append(first, first[0])

	second := config.Agents()
	if len(second) != 1 || second[0].Args[0] != "--stdio" ||
		second[0].Capabilities[0] != "analyze" || second[0].Resources.ToolSlots["scanner"] != 1 {
		t.Fatalf("stored config was mutated: %+v", second)
	}
}

func TestLoadRejectsTrailingJSON(t *testing.T) {
	path := writeDocument(t, []map[string]any{validAgent(currentExecutable(t))})
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.WriteString("\n{}\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = Load(path, Options{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v, want ErrInvalidConfig", err)
	}
}

func TestLoadRejectsUnsupportedRuntimeAndEmptyRegistry(t *testing.T) {
	t.Run("runtime", func(t *testing.T) {
		agent := validAgent(currentExecutable(t))
		agent["runtime"] = "container"
		path := writeDocument(t, []map[string]any{agent})
		if _, err := Load(path, Options{}); !errors.Is(err, ErrUnsupportedRuntime) {
			t.Fatalf("error = %v, want ErrUnsupportedRuntime", err)
		}
	})
	t.Run("empty", func(t *testing.T) {
		path := writeDocument(t, []map[string]any{})
		if _, err := Load(path, Options{}); !errors.Is(err, ErrNoAgents) {
			t.Fatalf("error = %v, want ErrNoAgents", err)
		}
	})
}

func TestLoadRejectsUnboundedProcessResources(t *testing.T) {
	for _, field := range []string{"cpu_milli", "memory_mib"} {
		t.Run(field, func(t *testing.T) {
			agent := validAgent(currentExecutable(t))
			resources := agent["resources"].(map[string]any)
			resources[field] = 0
			path := writeDocument(t, []map[string]any{agent})
			if _, err := Load(path, Options{}); !errors.Is(err, ErrUnsafeResourcePolicy) {
				t.Fatalf("error = %v, want ErrUnsafeResourcePolicy", err)
			}
		})
	}
}

func TestLoadResolvesEntrypointSymlink(t *testing.T) {
	target := currentExecutable(t)
	link := filepath.Join(t.TempDir(), "agent-link")
	if runtime.GOOS == "windows" {
		link += ".exe"
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink is unavailable: %v", err)
	}
	path := writeDocument(t, []map[string]any{validAgent(link)})
	config, err := Load(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := config.Agents()[0].Entrypoint; got != filepath.Clean(want) {
		t.Fatalf("entrypoint = %q, want %q", got, want)
	}
}

func TestStrictPermissionsRejectUnsafeFilesOnUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not authoritative on Windows")
	}
	t.Run("config", func(t *testing.T) {
		path := writeDocument(t, []map[string]any{validAgent(currentExecutable(t))})
		if err := os.Chmod(path, 0o622); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path, Options{StrictPermissions: true}); !errors.Is(err, ErrUnsafePermissions) {
			t.Fatalf("error = %v, want ErrUnsafePermissions", err)
		}
	})
	t.Run("entrypoint", func(t *testing.T) {
		entrypoint := filepath.Join(t.TempDir(), "agent")
		if err := os.WriteFile(entrypoint, []byte("#!/bin/sh\nexit 0\n"), 0o777); err != nil {
			t.Fatal(err)
		}
		// WriteFile applies the process umask, so force the unsafe mode that
		// this strict-permission test is intended to exercise.
		if err := os.Chmod(entrypoint, 0o777); err != nil {
			t.Fatal(err)
		}
		path := writeDocument(t, []map[string]any{validAgent(entrypoint)})
		if _, err := Load(path, Options{StrictPermissions: true}); !errors.Is(err, ErrUnsafePermissions) {
			t.Fatalf("error = %v, want ErrUnsafePermissions", err)
		}
	})
}

func validAgent(entrypoint string) map[string]any {
	return map[string]any{
		"id": "agent-1", "name": "Test Agent", "runtime": "process",
		"entrypoint": entrypoint, "args": []string{"--stdio"},
		"capabilities": []string{"analyze"},
		"resources": map[string]any{
			"cpu_milli": 250, "memory_mib": 128,
			"tool_slots": map[string]int64{"scanner": 1},
		},
		"max_concurrency": 2, "execution_timeout": "2s",
	}
}

func writeDocument(t *testing.T, agents []map[string]any) string {
	t.Helper()
	content, err := json.Marshal(map[string]any{"agents": agents})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "agents.json")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func currentExecutable(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return path
}
