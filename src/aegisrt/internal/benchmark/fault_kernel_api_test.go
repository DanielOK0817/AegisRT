package benchmark

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aegisrt/internal/domain"
)

const faultKernelTestToken = "memory-only-token"

func TestFaultKernelAPIClientSubmitsGraphAndObservesTasks(t *testing.T) {
	target, survivor := faultKernelAPITestTask("target-task"), faultKernelAPITestTask("survivor-task")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.URL.Path == "/readyz" {
			if request.Header.Get("Authorization") != "" {
				t.Error("readiness request disclosed bearer token")
			}
			writeFaultKernelAPITestJSON(writer, http.StatusOK, map[string]string{"status": "ready"})
			return
		}
		if request.Header.Get("Authorization") != "Bearer "+faultKernelTestToken {
			t.Error("authenticated request omitted bearer token")
		}
		switch request.Method + " " + request.URL.Path {
		case "POST /v1/task-graphs/patches":
			if request.Header.Get("Content-Type") != "application/json" {
				t.Errorf("content type = %q", request.Header.Get("Content-Type"))
			}
			var patch faultKernelPatchRequest
			decoder := json.NewDecoder(request.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&patch); err != nil || len(patch.Tasks) != 2 {
				t.Errorf("invalid patch: tasks=%d err=%v", len(patch.Tasks), err)
			}
			writeFaultKernelAPITestTasks(writer, http.StatusCreated, target, survivor)
		case "GET /v1/tasks":
			writeFaultKernelAPITestTasks(writer, http.StatusOK, target, survivor)
		default:
			t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()

	client, err := newFaultKernelAPIClient(strings.TrimPrefix(server.URL, "http://"), []byte(faultKernelTestToken))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.SubmitTargetAndSurvivor(context.Background(), target, survivor); err != nil {
		t.Fatal(err)
	}
	statuses, err := client.TaskStatuses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if requests != 3 || statuses[target.ID] != domain.TaskStatusReady || statuses[survivor.ID] != domain.TaskStatusReady {
		t.Fatalf("requests=%d statuses=%v", requests, statuses)
	}
}

func TestFaultKernelAPIClientRequiresNumericLoopbackAndSafeToken(t *testing.T) {
	invalid := []struct {
		address string
		token   []byte
	}{
		{address: "localhost:8080", token: []byte("token")},
		{address: "0.0.0.0:8080", token: []byte("token")},
		{address: "192.0.2.1:8080", token: []byte("token")},
		{address: "http://127.0.0.1:8080", token: []byte("token")},
		{address: "127.0.0.1", token: []byte("token")},
		{address: "127.0.0.1:0", token: []byte("token")},
		{address: "[::1%lo]:8080", token: []byte("token")},
		{address: "127.0.0.1:8080", token: nil},
		{address: "127.0.0.1:8080", token: []byte("line\nbreak")},
	}
	for _, test := range invalid {
		client, err := newFaultKernelAPIClient(test.address, test.token)
		if err == nil || client != nil {
			client.Close()
			t.Fatalf("accepted address=%q token=%q", test.address, test.token)
		}
		if strings.Contains(err.Error(), test.address) || len(test.token) > 0 && strings.Contains(err.Error(), string(test.token)) {
			t.Fatalf("configuration error leaked input: %v", err)
		}
	}
	for _, address := range []string{"127.0.0.1:8080", "[::1]:8080"} {
		client, err := newFaultKernelAPIClient(address, []byte("token"))
		if err != nil {
			t.Fatalf("rejected loopback address %q: %v", address, err)
		}
		transport, ok := client.httpClient.Transport.(*http.Transport)
		if !ok || transport.Proxy != nil || client.httpClient.Timeout <= 0 || transport.ResponseHeaderTimeout <= 0 {
			t.Fatal("client did not enforce proxy and timeout policy")
		}
		client.Close()
	}
}

func TestFaultKernelAPIClientRejectsUntrustedResponses(t *testing.T) {
	task := faultKernelAPITestTask("target-task")
	validTaskBody := faultKernelAPITestTasksJSON(task, domain.TaskStatusReady)
	tests := []struct {
		name        string
		contentType string
		status      int
		body        string
	}{
		{name: "wrong status", contentType: "application/json", status: http.StatusForbidden, body: `{"error":"secret"}`},
		{name: "wrong content type", contentType: "text/plain", status: http.StatusOK, body: `{"tasks":[]}`},
		{name: "unknown field", contentType: "application/json", status: http.StatusOK, body: `{"tasks":[],"secret":"value"}`},
		{name: "unknown task field", contentType: "application/json", status: http.StatusOK, body: strings.Replace(validTaskBody, `"id":"target-task"`, `"id":"target-task","secret":"value"`, 1)},
		{name: "missing task field", contentType: "application/json", status: http.StatusOK, body: strings.Replace(validTaskBody, `"priority":1,`, "", 1)},
		{name: "trailing value", contentType: "application/json", status: http.StatusOK, body: `{"tasks":[]} {}`},
		{name: "null tasks", contentType: "application/json", status: http.StatusOK, body: `{"tasks":null}`},
		{name: "invalid status", contentType: "application/json", status: http.StatusOK, body: faultKernelAPITestTasksJSON(task, domain.TaskStatus("SECRET_STATUS"))},
		{name: "duplicate task", contentType: "application/json", status: http.StatusOK, body: faultKernelAPITestTasksJSON(task, domain.TaskStatusReady, task, domain.TaskStatusReady)},
		{name: "oversize", contentType: "application/json", status: http.StatusOK, body: strings.Repeat("x", faultKernelAPIResponseLimit+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", test.contentType)
				writer.WriteHeader(test.status)
				_, _ = io.WriteString(writer, test.body)
			}))
			defer server.Close()
			client, err := newFaultKernelAPIClient(strings.TrimPrefix(server.URL, "http://"), []byte(faultKernelTestToken))
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if _, err = client.TaskStatuses(context.Background()); err == nil {
				t.Fatal("untrusted response was accepted")
			}
		})
	}
}

func TestFaultKernelAPIClientErrorsAreSanitizedAndBounded(t *testing.T) {
	const maliciousBody = `{"tasks":[{"task":{"id":"secret-task"}}],"path":"/secret/runtime.state"}`
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(writer, maliciousBody)
	}))
	address := strings.TrimPrefix(server.URL, "http://")
	client, err := newFaultKernelAPIClient(address, []byte(faultKernelTestToken))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.TaskStatuses(context.Background())
	if err == nil {
		t.Fatal("server failure was accepted")
	}
	for _, secret := range []string{faultKernelTestToken, address, maliciousBody, "secret-task", "/secret/runtime.state"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked %q: %v", secret, err)
		}
	}
	server.Close()
	_, err = client.TaskStatuses(context.Background())
	if err == nil || strings.Contains(err.Error(), address) {
		t.Fatalf("network error was not sanitized: %v", err)
	}

	timeoutServer := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer timeoutServer.Close()
	timeoutClient, newErr := newFaultKernelAPIClient(strings.TrimPrefix(timeoutServer.URL, "http://"), []byte(faultKernelTestToken))
	if newErr != nil {
		t.Fatal(newErr)
	}
	defer timeoutClient.Close()
	started := time.Now()
	_, err = timeoutClient.TaskStatuses(context.Background())
	if err == nil || time.Since(started) > 2*faultKernelAPIRequestTimeout {
		t.Fatalf("request timeout was not bounded: elapsed=%s err=%v", time.Since(started), err)
	}
}

func TestFaultKernelAPIClientRejectsInvalidLocalGraphWithoutRequest(t *testing.T) {
	client, newErr := newFaultKernelAPIClient("127.0.0.1:1", []byte(faultKernelTestToken))
	if newErr != nil {
		t.Fatal(newErr)
	}
	defer client.Close()
	task := faultKernelAPITestTask("same-task")
	err := client.SubmitTargetAndSurvivor(context.Background(), task, task)
	if !errors.Is(err, errFaultKernelAPITaskGraph) || strings.Contains(err.Error(), task.ID) {
		t.Fatalf("invalid graph error = %v", err)
	}
}

func faultKernelAPITestTask(id string) domain.TaskSpec {
	return domain.TaskSpec{ID: id, WorkflowID: "fault-kernel", AgentID: "fixture-agent", Goal: "deterministic fixture",
		Priority: 1, Resources: domain.ResourceVector{CPUMilli: 10, MemoryMiB: 8}, MaxAttempts: 2,
		Timeout: 5 * time.Second, IdempotencyKey: "key-" + id}
}

func writeFaultKernelAPITestTasks(writer http.ResponseWriter, status int, tasks ...domain.TaskSpec) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	views := make([]faultKernelTaskView, 0, len(tasks))
	for _, task := range tasks {
		views = append(views, newFaultKernelTaskView(task, domain.TaskStatusReady))
	}
	_ = json.NewEncoder(writer).Encode(faultKernelTasksResponse{Tasks: &views})
}

func writeFaultKernelAPITestJSON(writer http.ResponseWriter, status int, body any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(body)
}

func faultKernelAPITestTasksJSON(values ...any) string {
	views := make([]faultKernelTaskView, 0, len(values)/2)
	for index := 0; index < len(values); index += 2 {
		views = append(views, newFaultKernelTaskView(values[index].(domain.TaskSpec), values[index+1].(domain.TaskStatus)))
	}
	payload, _ := json.Marshal(faultKernelTasksResponse{Tasks: &views})
	return string(payload)
}
