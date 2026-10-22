package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aegisrt/internal/agentprotocol"
	"aegisrt/internal/domain"
)

const contextChannelArtifact = "shared deterministic analysis"

func TestDaemonPersistsAndReusesAgentOutputAcrossDirectDependants(t *testing.T) {
	directory := t.TempDir()
	options := contextChannelDaemonOptions(t, directory)
	first, err := buildDaemon(options)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(first.handler)
	cancel, serviceDone := startContextChannelService(first)
	serviceStopped := false
	firstClosed := false
	defer func() {
		if !serviceStopped {
			cancel()
			select {
			case serviceErr := <-serviceDone:
				if serviceErr != nil {
					t.Errorf("stop Runtime service: %v", serviceErr)
				}
			case <-time.After(3 * time.Second):
				t.Error("Runtime service did not stop during cleanup")
			}
		}
		server.Close()
		if !firstClosed {
			if closeErr := first.close(); closeErr != nil {
				t.Errorf("close first daemon: %v", closeErr)
			}
		}
	}()
	submitContextChannelDAG(t, server)
	for _, taskID := range []string{"produce", "analyze", "review"} {
		waitForKernelTaskStatus(t, first, taskID, domain.TaskStatusSucceeded)
	}
	firstRecord, err := first.contexts.Output("produce")
	if err != nil {
		t.Fatal(err)
	}
	if len(firstRecord.Artifacts) != 1 || len(firstRecord.Artifacts[0].Targets) != 3 {
		t.Fatalf("producer delivery provenance = %+v, want three targets", firstRecord)
	}

	stats := first.contexts.Stats().Store
	if stats.UniquePages != 1 || stats.UniqueBlobs != 1 {
		t.Fatalf("Context CAS stats = %+v, want one physical page/blob", stats)
	}
	assertResolvedChannelArtifact(t, first, "analyze", "analyzer")
	assertResolvedChannelArtifact(t, first, "review", "reviewer")

	stopContextChannelService(t, cancel, serviceDone)
	serviceStopped = true
	server.Close()
	if err := first.close(); err != nil {
		t.Fatal(err)
	}
	firstClosed = true

	second, err := buildDaemon(options)
	if err != nil {
		t.Fatal(err)
	}
	secondServer := httptest.NewServer(second.handler)
	secondCancel, secondDone := startContextChannelService(second)
	secondStopped := false
	secondClosed := false
	defer func() {
		if !secondStopped {
			secondCancel()
			select {
			case serviceErr := <-secondDone:
				if serviceErr != nil {
					t.Errorf("stop restored Runtime service: %v", serviceErr)
				}
			case <-time.After(3 * time.Second):
				t.Error("restored Runtime service did not stop during cleanup")
			}
		}
		secondServer.Close()
		if !secondClosed {
			if closeErr := second.close(); closeErr != nil {
				t.Errorf("close restored daemon: %v", closeErr)
			}
		}
	}()
	record, err := second.contexts.Output("produce")
	if err != nil || len(record.Artifacts) != 1 || record.Artifacts[0].Name != "analysis" {
		t.Fatalf("restored producer output = %+v error=%v", record, err)
	}
	assertResolvedChannelArtifact(t, second, "analyze", "analyzer")
	assertResolvedChannelArtifact(t, second, "review", "reviewer")
	submitContextChannelTask(t, secondServer, contextChannelTask("late-report", "reporter", []string{"produce"}))
	waitForKernelTaskStatus(t, second, "late-report", domain.TaskStatusSucceeded)
	assertResolvedChannelArtifact(t, second, "late-report", "reporter")
	record, err = second.contexts.Output("produce")
	if err != nil || len(record.Artifacts[0].Targets) != 3 {
		t.Fatalf("late read changed durable publication targets: %+v error=%v", record, err)
	}
	head, err := second.contexts.Inspect("channel-e2e", "reporter")
	if err != nil || head.PageCount != 0 {
		t.Fatalf("late read materialized references in principal head: %+v error=%v", head, err)
	}
	stopContextChannelService(t, secondCancel, secondDone)
	secondStopped = true
	secondServer.Close()
	if err := second.close(); err != nil {
		t.Fatal(err)
	}
	secondClosed = true
	third, err := buildDaemon(options)
	if err != nil {
		t.Fatal(err)
	}
	defer third.close()
	assertResolvedChannelArtifact(t, third, "late-report", "reporter")
}

func contextChannelDaemonOptions(t *testing.T, directory string) daemonOptions {
	t.Helper()
	tokenPath := filepath.Join(directory, "api.token")
	if err := os.WriteFile(tokenPath, []byte(daemonTestToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	agentsPath := filepath.Join(directory, "agents.json")
	agents := []map[string]any{
		contextChannelAgentConfig("producer", "producer"),
		contextChannelAgentConfig("analyzer", "consumer"),
		contextChannelAgentConfig("reviewer", "consumer"),
		contextChannelAgentConfig("reporter", "consumer"),
	}
	payload, err := json.Marshal(map[string]any{"agents": agents})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agentsPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{
		"-listen", "127.0.0.1:0", "-agents-file", agentsPath,
		"-auth-token-file", tokenPath, "-state-file", filepath.Join(directory, "runtime.state"),
		"-max-workers", "2", "-poll-interval", "10ms", "-event-capacity", "128",
	}
	var stderr bytes.Buffer
	options, err := parseOptions(args, &stderr)
	if err != nil {
		t.Fatalf("parse options: %v; stderr=%s", err, stderr.String())
	}
	return options
}

func contextChannelAgentConfig(agentID, mode string) map[string]any {
	return map[string]any{
		"id": agentID, "name": agentID, "runtime": "process", "entrypoint": os.Args[0],
		"args": []string{"-test.run=TestContextChannelAgentHelperProcess", "--", mode},
		"resources": map[string]any{
			"cpu_milli": 250, "memory_mib": 64, "token_budget": 128,
		},
		"max_concurrency": 1, "execution_timeout": "2s",
	}
}

func startContextChannelService(app *daemonApp) (context.CancelFunc, <-chan error) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.service.Run(ctx) }()
	return cancel, done
}

func stopContextChannelService(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Runtime service did not stop")
	}
}

func submitContextChannelDAG(t *testing.T, server *httptest.Server) {
	t.Helper()
	tasks := []map[string]any{
		contextChannelTask("produce", "producer", nil),
		contextChannelTask("analyze", "analyzer", []string{"produce"}),
		contextChannelTask("review", "reviewer", []string{"produce"}),
	}
	payload, err := json.Marshal(map[string]any{"tasks": tasks})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/task-graphs/patches", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+daemonTestToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	assertHTTPStatus(t, response, http.StatusCreated)
}

func submitContextChannelTask(t *testing.T, server *httptest.Server, task map[string]any) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"tasks": []map[string]any{task}})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/task-graphs/patches", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+daemonTestToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	assertHTTPStatus(t, response, http.StatusCreated)
}

func contextChannelTask(taskID, agentID string, dependencies []string) map[string]any {
	return map[string]any{
		"id": taskID, "workflow_id": "channel-e2e", "agent_id": agentID,
		"goal": "verify durable ContextChannel", "dependencies": dependencies, "priority": 1,
		"resources": map[string]any{"token_budget": 128}, "max_attempts": 1,
		"timeout_ms": 2000, "idempotency_key": "channel-" + taskID,
	}
}

func assertResolvedChannelArtifact(t *testing.T, app *daemonApp, taskID, agentID string) {
	t.Helper()
	snapshot, exists := app.kernel.Task(taskID)
	if !exists {
		t.Fatalf("task %q not found", taskID)
	}
	resolution, err := app.contexts.Resolve(context.Background(), snapshot.Spec, contextChannelAgentSpec(agentID), 128)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resolution.Text, contextChannelArtifact) {
		t.Fatalf("Agent %q Context omitted durable artifact: %q", agentID, resolution.Text)
	}
}

func contextChannelAgentSpec(agentID string) domain.AgentSpec {
	return domain.AgentSpec{
		ID: agentID, Name: agentID, Runtime: "process", Entrypoint: os.Args[0],
		Resources:      domain.ResourceVector{CPUMilli: 250, MemoryMiB: 64, TokenBudget: 128},
		MaxConcurrency: 1, ExecutionTimeout: 2 * time.Second,
	}
}

func TestContextChannelAgentHelperProcess(t *testing.T) {
	mode := helperMode(os.Args)
	if mode == "" {
		return
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(8)
	}
	var request struct {
		Context string `json:"context"`
	}
	if json.Unmarshal(input, &request) != nil {
		os.Exit(9)
	}
	envelope := agentprotocol.Envelope{Version: agentprotocol.VersionV1, Artifacts: []agentprotocol.Artifact{}}
	switch mode {
	case "producer":
		envelope.Artifacts = []agentprotocol.Artifact{{
			Name: "analysis", Content: contextChannelArtifact, Visibility: "workflow",
		}}
	case "consumer":
		if !strings.Contains(request.Context, contextChannelArtifact) {
			os.Exit(10)
		}
	default:
		os.Exit(11)
	}
	if json.NewEncoder(os.Stdout).Encode(envelope) != nil {
		os.Exit(12)
	}
	os.Exit(0)
}

func helperMode(args []string) string {
	for index, argument := range args {
		if argument == "--" && index+1 < len(args) {
			return args[index+1]
		}
	}
	return ""
}
