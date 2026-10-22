package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"aegisrt/internal/domain"
	"aegisrt/internal/runtimecore"
	"aegisrt/internal/taskgraph"
)

const (
	apiToken   = "test-api-token"
	validPatch = `{"tasks":[{"id":"task","workflow_id":"demo","agent_id":"worker","goal":"run task","priority":1,"resources":{"cpu_milli":100},"max_attempts":1,"timeout_ms":1000,"idempotency_key":"demo-task"}]}`
)

type fakeControl struct {
	mu             sync.Mutex
	patchErr       error
	cancelErr      error
	cancelSnapshot taskgraph.TaskSnapshot
	tasks          []taskgraph.TaskSnapshot
	stats          runtimecore.Stats
	patchCalls     int
	cancelCalls    int
}

func (f *fakeControl) ApplyPatch(tasks []domain.TaskSpec, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.patchCalls++
	if f.patchErr != nil {
		return f.patchErr
	}
	for _, task := range tasks {
		f.tasks = append(f.tasks, taskgraph.TaskSnapshot{Spec: task, Status: domain.TaskStatusReady})
	}
	return nil
}

func (f *fakeControl) Cancel(string) (taskgraph.TaskSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelCalls++
	return f.cancelSnapshot, f.cancelErr
}

func (f *fakeControl) Tasks() []taskgraph.TaskSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]taskgraph.TaskSnapshot, len(f.tasks))
	copy(result, f.tasks)
	return result
}

func (f *fakeControl) Stats() runtimecore.Stats {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stats
}

func testControlHandler(control Control, token string, notify func()) http.Handler {
	return NewServer(Config{Control: control, AuthToken: []byte(token), Notify: notify}).Handler()
}

func performJSONRequest(handler http.Handler, method, target, body, token string) *httptest.ResponseRecorder {
	return performAuthorizedRequest(handler, method, target, body, token, "application/json")
}

func performAuthorizedRequest(
	handler http.Handler,
	method string,
	target string,
	body string,
	token string,
	contentType string,
) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func snapshot(taskID string, status domain.TaskStatus) taskgraph.TaskSnapshot {
	return taskgraph.TaskSnapshot{
		Spec: domain.TaskSpec{
			ID:             taskID,
			WorkflowID:     "demo",
			AgentID:        "worker",
			Goal:           "run task",
			Priority:       1,
			Resources:      domain.ResourceVector{CPUMilli: 100},
			MaxAttempts:    1,
			Timeout:        time.Second,
			IdempotencyKey: "demo-" + taskID,
		},
		Status: status,
	}
}
