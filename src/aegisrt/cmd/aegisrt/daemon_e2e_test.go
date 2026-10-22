package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"aegisrt/internal/domain"
	"aegisrt/internal/statefile"
)

func TestDaemonAutomaticallyExecutesAuthenticatedTask(t *testing.T) {
	harness := startDaemonHarness(t, "success")
	response := harness.request(t, http.MethodPost, "/v1/task-graphs/patches", daemonPatch("automatic"), true)
	assertHTTPStatus(t, response, http.StatusCreated)
	waitForDaemonTaskStatus(t, harness, "automatic", domain.TaskStatusSucceeded)

	for _, target := range []string{"/v1/scheduler/ticks", "/v1/tasks/automatic/completion"} {
		response = harness.request(t, http.MethodPost, target, `{}`, true)
		assertHTTPStatus(t, response, http.StatusNotFound)
	}
	harness.stop(t)
	if strings.Contains(harness.stderr.String(), daemonTestToken) {
		t.Fatal("daemon log leaked API token")
	}
}

func TestDaemonCancelsRunningTaskAndReleasesResources(t *testing.T) {
	harness := startDaemonHarness(t, "sleep")
	response := harness.request(t, http.MethodPost, "/v1/task-graphs/patches", daemonPatch("cancel-me"), true)
	assertHTTPStatus(t, response, http.StatusCreated)
	waitForDaemonTaskStatus(t, harness, "cancel-me", domain.TaskStatusRunning)

	response = harness.request(t, http.MethodPost, "/v1/tasks/cancel-me/cancel", `{}`, true)
	if response.StatusCode != http.StatusAccepted && response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("cancel status = %d, body = %s", response.StatusCode, body)
	}
	response.Body.Close()
	waitForDaemonTaskStatus(t, harness, "cancel-me", domain.TaskStatusCanceled)

	response = harness.request(t, http.MethodGet, "/v1/runtime", "", false)
	defer response.Body.Close()
	var runtimeView struct {
		Stats struct {
			Allocated domain.ResourceVector `json:"allocated"`
		} `json:"stats"`
	}
	if err := json.NewDecoder(response.Body).Decode(&runtimeView); err != nil {
		t.Fatal(err)
	}
	if !runtimeView.Stats.Allocated.IsZero() {
		t.Fatalf("canceled task retained resources: %+v", runtimeView.Stats.Allocated)
	}
}

func TestDaemonRejectsUnauthenticatedTaskAccess(t *testing.T) {
	harness := startDaemonHarness(t, "success")
	for _, request := range []struct {
		method string
		target string
		body   string
	}{
		{method: http.MethodPost, target: "/v1/task-graphs/patches", body: daemonPatch("denied")},
		{method: http.MethodGet, target: "/v1/tasks"},
	} {
		response := harness.request(t, request.method, request.target, request.body, false)
		assertHTTPStatus(t, response, http.StatusUnauthorized)
	}
}

func TestDaemonRestartsAndRetriesInterruptedTask(t *testing.T) {
	directory := t.TempDir()
	first := startDaemonHarnessInDirectory(t, "retry", directory)
	response := first.request(t, http.MethodPost, "/v1/task-graphs/patches", daemonPatchWithAttempts("restart-me", 2), true)
	assertHTTPStatus(t, response, http.StatusCreated)
	waitForDaemonTaskStatus(t, first, "restart-me", domain.TaskStatusRunning)
	first.stop(t)

	second := startDaemonHarnessInDirectory(t, "retry", directory)
	waitForDaemonTaskStatus(t, second, "restart-me", domain.TaskStatusSucceeded)
	response = second.request(t, http.MethodGet, "/v1/runtime", "", false)
	defer response.Body.Close()
	var runtimeView struct {
		Stats struct {
			Durable           bool   `json:"durable"`
			CheckpointHealthy bool   `json:"checkpoint_healthy"`
			Generation        uint64 `json:"generation"`
		} `json:"stats"`
	}
	if err := json.NewDecoder(response.Body).Decode(&runtimeView); err != nil {
		t.Fatal(err)
	}
	if !runtimeView.Stats.Durable || !runtimeView.Stats.CheckpointHealthy || runtimeView.Stats.Generation < 5 {
		t.Fatalf("runtime durability stats = %+v", runtimeView.Stats)
	}
	store, err := statefile.New(filepath.Join(directory, "runtime.state"))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{daemonTestToken, "retried", `"stdout"`, `"stderr"`, `"context_text"`} {
		if bytes.Contains(payload, []byte(forbidden)) {
			t.Fatalf("checkpoint leaked %q", forbidden)
		}
	}
}

func TestDaemonFailsClosedOnCorruptState(t *testing.T) {
	directory := t.TempDir()
	args := daemonArgsInDirectory(t, "success", freeLoopbackAddress(t), directory)
	if err := os.WriteFile(filepath.Join(directory, "runtime.state"), []byte(`{"broken":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := run(context.Background(), args, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "restore Runtime state") {
		t.Fatalf("run error = %v, want state restore failure", err)
	}
	if strings.Contains(err.Error(), daemonTestToken) || strings.Contains(stderr.String(), daemonTestToken) {
		t.Fatal("startup failure leaked API token")
	}
}

type daemonHarness struct {
	baseURL string
	client  *http.Client
	cancel  context.CancelFunc
	done    chan error
	stderr  *bytes.Buffer
	once    sync.Once
}

func startDaemonHarness(t *testing.T, mode string) *daemonHarness {
	t.Helper()
	return startDaemonHarnessInDirectory(t, mode, t.TempDir())
}

func startDaemonHarnessInDirectory(t *testing.T, mode, directory string) *daemonHarness {
	t.Helper()
	address := freeLoopbackAddress(t)
	args := daemonArgsInDirectory(t, mode, address, directory)
	ctx, cancel := context.WithCancel(context.Background())
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, args, &stdout, &stderr)
	}()
	harness := &daemonHarness{
		baseURL: "http://" + address,
		client: &http.Client{
			Timeout:   time.Second,
			Transport: &http.Transport{Proxy: nil},
		},
		cancel: cancel, done: done, stderr: &stderr,
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := harness.client.Get(harness.baseURL + "/healthz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				t.Cleanup(func() { harness.stop(t) })
				return harness
			}
		}
		select {
		case runErr := <-done:
			t.Fatalf("daemon exited before health check: %v; stderr=%s", runErr, stderr.String())
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	t.Fatalf("daemon did not become healthy; stderr=%s", stderr.String())
	return nil
}

func (h *daemonHarness) stop(t *testing.T) {
	t.Helper()
	h.once.Do(func() {
		h.cancel()
		select {
		case err := <-h.done:
			if err != nil {
				t.Errorf("daemon shutdown error: %v; stderr=%s", err, h.stderr.String())
			}
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop before timeout")
		}
		h.client.CloseIdleConnections()
	})
}

func (h *daemonHarness) request(t *testing.T, method, target, body string, authorized bool) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, h.baseURL+target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if authorized {
		request.Header.Set("Authorization", "Bearer "+daemonTestToken)
	}
	response, err := h.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func waitForDaemonTaskStatus(t *testing.T, harness *daemonHarness, taskID string, want domain.TaskStatus) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	lastStatus := domain.TaskStatus("")
	for time.Now().Before(deadline) {
		response := harness.request(t, http.MethodGet, "/v1/tasks", "", true)
		var body struct {
			Tasks []struct {
				Task struct {
					ID string `json:"id"`
				} `json:"task"`
				Status domain.TaskStatus `json:"status"`
			} `json:"tasks"`
		}
		decodeErr := json.NewDecoder(response.Body).Decode(&body)
		response.Body.Close()
		if decodeErr == nil {
			for _, item := range body.Tasks {
				if item.Task.ID == taskID {
					lastStatus = item.Status
					if item.Status == want {
						return
					}
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task %q did not reach %s; last status=%s", taskID, want, lastStatus)
}

func assertHTTPStatus(t *testing.T, response *http.Response, want int) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != want {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, want %d; body=%s", response.StatusCode, want, body)
	}
}

func daemonPatch(taskID string) string {
	return daemonPatchWithAttempts(taskID, 1)
}

func daemonPatchWithAttempts(taskID string, maxAttempts int) string {
	return fmt.Sprintf(`{"tasks":[{"id":%q,"workflow_id":"daemon-e2e","agent_id":"test-agent","goal":"automatic daemon execution","priority":1,"resources":{},"max_attempts":%d,"timeout_ms":2000,"idempotency_key":%q}]}`,
		taskID, maxAttempts, "key-"+taskID)
}
