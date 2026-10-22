package main

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var daemonTestToken = strings.Repeat("d", 32)

const (
	daemonEventArtifactSecret = "artifact-secret-not-for-event-api"
	daemonEventStderrSecret   = "stderr-secret-not-for-event-api"
)

func daemonArgs(t *testing.T, mode, address string) []string {
	t.Helper()
	return daemonArgsInDirectory(t, mode, address, t.TempDir())
}

func daemonArgsInDirectory(t *testing.T, mode, address, directory string) []string {
	t.Helper()
	tokenPath := filepath.Join(directory, "api.token")
	if err := os.WriteFile(tokenPath, []byte(daemonTestToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	agentsPath := filepath.Join(directory, "agents.json")
	statePath := filepath.Join(directory, "runtime.state")
	document := map[string]any{"agents": []map[string]any{{
		"id": "test-agent", "name": "Daemon Test Agent", "runtime": "process",
		"entrypoint": os.Args[0],
		"args":       []string{"-test.run=TestDaemonAgentHelperProcess", "--", mode},
		"resources": map[string]any{
			"cpu_milli": 250, "memory_mib": 128, "token_budget": 1000,
		},
		"max_concurrency": 2, "execution_timeout": "2s",
	}}}
	content, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agentsPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return []string{
		"-listen", address,
		"-agents-file", agentsPath,
		"-auth-token-file", tokenPath,
		"-state-file", statePath,
		"-max-workers", "2",
		"-poll-interval", "10ms",
		"-event-capacity", "128",
		"-shutdown-timeout", "3s",
	}
}

func freeLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func TestDaemonAgentHelperProcess(t *testing.T) {
	mode := ""
	for index, argument := range os.Args {
		if argument == "--" && index+1 < len(os.Args) {
			mode = os.Args[index+1]
			break
		}
	}
	if mode == "" {
		return
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(8)
	}
	var request struct {
		ContextRef    string `json:"context_ref"`
		Context       string `json:"context"`
		AttemptNumber int    `json:"attempt_number"`
	}
	if json.Unmarshal(input, &request) != nil || request.ContextRef == "" {
		os.Exit(9)
	}
	switch mode {
	case "success":
		writeDaemonAgentSuccess("ok")
		os.Exit(0)
	case "event-secrets":
		_, _ = os.Stderr.Write([]byte(daemonEventStderrSecret))
		_, _ = os.Stdout.Write([]byte(`{"version":"aegisrt.agent-output/v1","artifacts":[{"name":"private-result","content":"` + daemonEventArtifactSecret + `","visibility":"agent"}]}`))
		os.Exit(0)
	case "sleep":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "retry":
		if request.AttemptNumber == 1 {
			time.Sleep(30 * time.Second)
		}
		writeDaemonAgentSuccess("retried")
		os.Exit(0)
	case "context":
		if request.Context != durableDaemonContext {
			os.Exit(11)
		}
		writeDaemonAgentSuccess("context restored")
		os.Exit(0)
	default:
		os.Exit(10)
	}
}

func writeDaemonAgentSuccess(diagnostic string) {
	_, _ = os.Stderr.Write([]byte(diagnostic))
	_, _ = os.Stdout.Write([]byte(`{"version":"aegisrt.agent-output/v1","artifacts":[]}`))
}
