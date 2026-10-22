package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aegisrt/internal/domain"
)

const durableDaemonContext = "durable daemon context"

func TestDaemonRestoresContextAndIdempotencyBeforeWorkerExecution(t *testing.T) {
	var stderr bytes.Buffer
	options, err := parseOptions(daemonArgs(t, "context", "127.0.0.1:0"), &stderr)
	if err != nil {
		t.Fatal(err)
	}
	first, err := buildDaemon(options)
	if err != nil {
		t.Fatal(err)
	}
	firstServer := httptest.NewServer(first.handler)
	initialETag := submitContextTask(t, firstServer)
	currentETag := appendDurableContext(t, firstServer, initialETag, http.StatusCreated)
	firstServer.Close()
	if err := first.close(); err != nil {
		t.Fatal(err)
	}

	second, err := buildDaemon(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := second.close(); err != nil {
			t.Errorf("close daemon: %v", err)
		}
	})
	secondServer := httptest.NewServer(second.handler)
	defer secondServer.Close()
	replayedETag := appendDurableContext(t, secondServer, initialETag, http.StatusOK)
	if replayedETag != currentETag {
		t.Fatalf("replay ETag=%q, want %q", replayedETag, currentETag)
	}

	runContext, cancel := context.WithCancel(context.Background())
	serviceDone := make(chan error, 1)
	go func() { serviceDone <- second.service.Run(runContext) }()
	second.service.Notify()
	waitForKernelTaskStatus(t, second, "context-restart", domain.TaskStatusSucceeded)
	cancel()
	select {
	case err := <-serviceDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Runtime service did not stop")
	}

	snapshot, err := second.contexts.Inspect("daemon-e2e", "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	if strongETagForTest(snapshot.ManifestID) != currentETag || snapshot.LogicalTokens != 3 || snapshot.PageCount != 1 {
		t.Fatalf("restored Context snapshot = %#v", snapshot)
	}
}

func submitContextTask(t *testing.T, server *httptest.Server) string {
	t.Helper()
	patch := `{"tasks":[{"id":"context-restart","workflow_id":"daemon-e2e","agent_id":"test-agent",` +
		`"goal":"verify durable context","priority":1,"resources":{"token_budget":32},` +
		`"max_attempts":1,"timeout_ms":2000,"idempotency_key":"context-restart-key"}]}`
	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/task-graphs/patches", strings.NewReader(patch))
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

	get, err := http.NewRequest(http.MethodGet, server.URL+"/v1/tasks/context-restart/context", nil)
	if err != nil {
		t.Fatal(err)
	}
	get.Header.Set("Authorization", "Bearer "+daemonTestToken)
	response, err = server.Client().Do(get)
	if err != nil {
		t.Fatal(err)
	}
	assertHTTPStatus(t, response, http.StatusOK)
	etag := response.Header.Get("ETag")
	if etag == "" {
		t.Fatal("Context inspection omitted ETag")
	}
	return etag
}

func appendDurableContext(t *testing.T, server *httptest.Server, ifMatch string, wantStatus int) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"content": durableDaemonContext, "visibility": "workflow", "pin": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/tasks/context-restart/context/pages", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+daemonTestToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "context-restart-append")
	request.Header.Set("If-Match", ifMatch)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		serialized, _ := io.ReadAll(response.Body)
		t.Fatalf("status=%d want=%d body=%s", response.StatusCode, wantStatus, serialized)
	}
	serialized, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), durableDaemonContext) || strings.Contains(string(serialized), "content_hash") {
		t.Fatalf("Context response leaked content: %s", serialized)
	}
	return response.Header.Get("ETag")
}

func waitForKernelTaskStatus(t *testing.T, app *daemonApp, taskID string, want domain.TaskStatus) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if snapshot, exists := app.kernel.Task(taskID); exists && snapshot.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	snapshot, exists := app.kernel.Task(taskID)
	t.Fatalf("task %q = (%s, %v), want %s; events=%v", taskID, snapshot.Status, exists, want, app.events.Snapshot())
}

func strongETagForTest(manifestID string) string { return fmt.Sprintf(`"%s"`, manifestID) }
