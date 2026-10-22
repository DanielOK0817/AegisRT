package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aegisrt/internal/domain"
	"aegisrt/internal/taskgraph"
)

func TestReadEndpointsRemainUnauthenticated(t *testing.T) {
	control := &fakeControl{}
	handler := testControlHandler(control, apiToken, nil)
	for _, target := range []string{"/healthz", "/readyz", "/v1/runtime"} {
		response := performAuthorizedRequest(handler, http.MethodGet, target, "", "", "")
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", target, response.Code)
		}
	}
}

func TestTaskListAuthorization(t *testing.T) {
	control := &fakeControl{tasks: []taskgraph.TaskSnapshot{snapshot("task", domain.TaskStatusReady)}}
	handler := testControlHandler(control, apiToken, nil)
	tests := []struct {
		name  string
		token string
		want  int
	}{
		{name: "missing", want: http.StatusUnauthorized},
		{name: "wrong", token: "wrong-secret", want: http.StatusUnauthorized},
		{name: "correct", token: apiToken, want: http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := performAuthorizedRequest(handler, http.MethodGet, "/v1/tasks", "", test.token, "")
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d", response.Code, test.want)
			}
			if strings.Contains(response.Body.String(), apiToken) || strings.Contains(response.Body.String(), "wrong-secret") {
				t.Fatal("authorization response leaked token material")
			}
		})
	}
}

func TestRemovedMutationRoutesReturnNotFound(t *testing.T) {
	handler := testControlHandler(&fakeControl{}, apiToken, nil)
	for _, target := range []string{"/v1/scheduler/ticks", "/v1/tasks/task/completion"} {
		response := performJSONRequest(handler, http.MethodPost, target, `{}`, apiToken)
		if response.Code != http.StatusNotFound {
			t.Fatalf("POST %s status = %d, want 404", target, response.Code)
		}
	}
}

func TestControlUnavailable(t *testing.T) {
	handler := testControlHandler(nil, apiToken, nil)
	readResponse := performAuthorizedRequest(handler, http.MethodGet, "/v1/tasks", "", apiToken, "")
	if readResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET status = %d, want 503", readResponse.Code)
	}
	writeResponse := performJSONRequest(handler, http.MethodPost, "/v1/task-graphs/patches", validPatch, apiToken)
	if writeResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST status = %d, want 503", writeResponse.Code)
	}
}

func TestAuthenticatedFlowOverLoopbackHTTP(t *testing.T) {
	control := &fakeControl{cancelSnapshot: snapshot("task", domain.TaskStatusCanceled)}
	server := httptest.NewServer(testControlHandler(control, apiToken, nil))
	defer server.Close()

	health, err := server.Client().Get(server.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", health.StatusCode)
	}

	patch := networkJSONRequest(t, server.Client(), http.MethodPost, server.URL+"/v1/task-graphs/patches", validPatch, apiToken)
	patchBody, err := io.ReadAll(patch.Body)
	patch.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if patch.StatusCode != http.StatusCreated {
		t.Fatalf("patch status = %d, body = %s", patch.StatusCode, patchBody)
	}

	cancel := networkJSONRequest(t, server.Client(), http.MethodPost, server.URL+"/v1/tasks/task/cancel", `{}`, apiToken)
	cancel.Body.Close()
	if cancel.StatusCode != http.StatusOK {
		t.Fatalf("cancel status = %d, want 200", cancel.StatusCode)
	}
}

func networkJSONRequest(t *testing.T, client *http.Client, method, target, body, token string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
