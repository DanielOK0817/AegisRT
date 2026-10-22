package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"aegisrt/internal/contextstate"
	"aegisrt/internal/domain"
	"aegisrt/internal/runtimecore"
	"aegisrt/internal/taskgraph"
)

func TestPatchAuthorization(t *testing.T) {
	tests := []struct {
		name        string
		configured  string
		provided    string
		wantStatus  int
		wantApplied int
	}{
		{name: "unconfigured fails closed", provided: apiToken, wantStatus: http.StatusUnauthorized},
		{name: "missing", configured: apiToken, wantStatus: http.StatusUnauthorized},
		{name: "wrong", configured: apiToken, provided: "wrong-secret", wantStatus: http.StatusUnauthorized},
		{name: "correct", configured: apiToken, provided: apiToken, wantStatus: http.StatusCreated, wantApplied: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			control := &fakeControl{}
			handler := testControlHandler(control, test.configured, nil)
			response := performJSONRequest(handler, http.MethodPost, "/v1/task-graphs/patches", validPatch, test.provided)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if control.patchCalls != test.wantApplied {
				t.Fatalf("patch calls = %d, want %d", control.patchCalls, test.wantApplied)
			}
			if strings.Contains(response.Body.String(), apiToken) || strings.Contains(response.Body.String(), "wrong-secret") {
				t.Fatal("authorization response leaked token material")
			}
		})
	}
}

func TestWritesRequireApplicationJSON(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		want        int
	}{
		{name: "missing", want: http.StatusUnsupportedMediaType},
		{name: "plain text", contentType: "text/plain", want: http.StatusUnsupportedMediaType},
		{name: "malformed", contentType: "application/json; charset", want: http.StatusUnsupportedMediaType},
		{name: "json", contentType: "application/json", want: http.StatusCreated},
		{name: "json charset", contentType: "application/json; charset=utf-8", want: http.StatusCreated},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := performAuthorizedRequest(
				testControlHandler(&fakeControl{}, apiToken, nil),
				http.MethodPost, "/v1/task-graphs/patches", validPatch, apiToken, test.contentType,
			)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestCancelAuthorizationAndContentType(t *testing.T) {
	tests := []struct {
		name        string
		token       string
		contentType string
		want        int
		wantCalls   int
	}{
		{name: "missing token", contentType: "application/json", want: http.StatusUnauthorized},
		{name: "wrong token", token: "wrong-secret", contentType: "application/json", want: http.StatusUnauthorized},
		{name: "missing content type", token: apiToken, want: http.StatusUnsupportedMediaType},
		{name: "correct", token: apiToken, contentType: "application/json", want: http.StatusOK, wantCalls: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			control := &fakeControl{cancelSnapshot: snapshot("task", domain.TaskStatusCanceled)}
			response := performAuthorizedRequest(
				testControlHandler(control, apiToken, nil),
				http.MethodPost, "/v1/tasks/task/cancel", `{}`, test.token, test.contentType,
			)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.want, response.Body.String())
			}
			if control.cancelCalls != test.wantCalls {
				t.Fatalf("cancel calls = %d, want %d", control.cancelCalls, test.wantCalls)
			}
		})
	}
}

func TestPatchUsesStrictJSON(t *testing.T) {
	tests := []string{
		`{"tasks":[],"unexpected":true}`,
		validPatch + `{}`,
		`{"tasks":[{"id":"task","unexpected":true}]}`,
	}
	for _, body := range tests {
		response := performJSONRequest(testControlHandler(&fakeControl{}, apiToken, nil), http.MethodPost, "/v1/task-graphs/patches", body, apiToken)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body %q: status = %d, want 400", body, response.Code)
		}
	}
}

func TestPatchRejectsExcessiveTaskCountBeforeContextProvisioning(t *testing.T) {
	control := &fakeControl{}
	body := `{"tasks":[` + strings.TrimSuffix(strings.Repeat(`{},`, maxPatchTasks+1), ",") + `]}`
	response := performJSONRequest(
		testControlHandler(control, apiToken, nil), http.MethodPost, "/v1/task-graphs/patches", body, apiToken,
	)
	if response.Code != http.StatusUnprocessableEntity || control.patchCalls != 0 {
		t.Fatalf("status=%d patchCalls=%d body=%s", response.Code, control.patchCalls, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"code":"task_limit_exceeded"`) {
		t.Fatalf("unexpected body: %s", response.Body.String())
	}
}

func TestPatchMapsDurabilityFailureToServiceUnavailable(t *testing.T) {
	control := &fakeControl{patchErr: fmt.Errorf("secret path: %w", runtimecore.ErrCheckpointUnavailable)}
	response := performJSONRequest(
		testControlHandler(control, apiToken, nil),
		http.MethodPost, "/v1/task-graphs/patches", validPatch, apiToken,
	)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "secret path") {
		t.Fatalf("durability error leaked internal detail: %s", response.Body.String())
	}
}

func TestPatchMapsContextCapacityToInsufficientStorage(t *testing.T) {
	control := &fakeControl{patchErr: fmt.Errorf("sensitive size: %w", contextstate.ErrCheckpointCapacity)}
	response := performJSONRequest(
		testControlHandler(control, apiToken, nil),
		http.MethodPost, "/v1/task-graphs/patches", validPatch, apiToken,
	)
	if response.Code != http.StatusInsufficientStorage {
		t.Fatalf("status = %d, want 507; body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "sensitive size") ||
		!strings.Contains(response.Body.String(), `"code":"context_capacity"`) {
		t.Fatalf("unexpected or unsanitized body: %s", response.Body.String())
	}
}

func TestPatchContextDurabilityFailureNotifiesRuntimeService(t *testing.T) {
	var notifications atomic.Int32
	control := &fakeControl{patchErr: contextstate.ErrUnavailable}
	response := performJSONRequest(
		testControlHandler(control, apiToken, func() { notifications.Add(1) }),
		http.MethodPost, "/v1/task-graphs/patches", validPatch, apiToken,
	)
	if response.Code != http.StatusServiceUnavailable || notifications.Load() != 1 {
		t.Fatalf("status=%d notifications=%d body=%s", response.Code, notifications.Load(), response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"code":"context_unavailable"`) {
		t.Fatalf("unexpected body: %s", response.Body.String())
	}
}

func TestCancelRequiresStrictEmptyObject(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "empty object", body: `{}`, want: http.StatusOK},
		{name: "whitespace object", body: ` { } `, want: http.StatusOK},
		{name: "empty body", want: http.StatusBadRequest},
		{name: "null", body: `null`, want: http.StatusBadRequest},
		{name: "field", body: `{"force":true}`, want: http.StatusBadRequest},
		{name: "array", body: `[]`, want: http.StatusBadRequest},
		{name: "trailing", body: `{} {}`, want: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			control := &fakeControl{cancelSnapshot: snapshot("task", domain.TaskStatusCanceled)}
			response := performJSONRequest(testControlHandler(control, apiToken, nil), http.MethodPost, "/v1/tasks/task/cancel", test.body, apiToken)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestCancelStatusMapping(t *testing.T) {
	tests := []struct {
		name     string
		snapshot taskgraph.TaskSnapshot
		err      error
		want     int
	}{
		{name: "missing", err: fmt.Errorf("wrapped: %w", taskgraph.ErrTaskNotFound), want: http.StatusNotFound},
		{name: "terminal", err: fmt.Errorf("wrapped: %w", runtimecore.ErrTaskNotCancelable), want: http.StatusConflict},
		{name: "checkpoint unavailable", err: fmt.Errorf("wrapped: %w", runtimecore.ErrCheckpointUnavailable), want: http.StatusServiceUnavailable},
		{name: "runtime unavailable", err: fmt.Errorf("wrapped: %w", runtimecore.ErrRuntimeUnavailable), want: http.StatusServiceUnavailable},
		{name: "internal", err: errors.New("storage unavailable"), want: http.StatusInternalServerError},
		{name: "already canceled", snapshot: snapshot("task", domain.TaskStatusCanceled), want: http.StatusOK},
		{name: "active accepted", snapshot: snapshot("task", domain.TaskStatusRunning), want: http.StatusAccepted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			control := &fakeControl{cancelSnapshot: test.snapshot, cancelErr: test.err}
			response := performJSONRequest(testControlHandler(control, apiToken, nil), http.MethodPost, "/v1/tasks/task/cancel", `{}`, apiToken)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestNotifyOnlyAfterAcceptedWrites(t *testing.T) {
	var notifications atomic.Int32
	control := &fakeControl{cancelSnapshot: snapshot("task", domain.TaskStatusCanceled)}
	handler := testControlHandler(control, apiToken, func() { notifications.Add(1) })

	requests := []struct {
		target string
		body   string
		token  string
		want   int
	}{
		{target: "/v1/task-graphs/patches", body: validPatch, token: apiToken, want: http.StatusCreated},
		{target: "/v1/tasks/task/cancel", body: `{}`, token: apiToken, want: http.StatusOK},
		{target: "/v1/task-graphs/patches", body: validPatch, token: "wrong", want: http.StatusUnauthorized},
	}
	for _, request := range requests {
		response := performJSONRequest(handler, http.MethodPost, request.target, request.body, request.token)
		if response.Code != request.want {
			t.Fatalf("%s: status = %d, want %d", request.target, response.Code, request.want)
		}
	}
	if got := notifications.Load(); got != 2 {
		t.Fatalf("notifications = %d, want 2", got)
	}

	control.patchErr = errors.New("rejected")
	response := performJSONRequest(handler, http.MethodPost, "/v1/task-graphs/patches", validPatch, apiToken)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("rejected patch status = %d, want 400", response.Code)
	}
	control.cancelErr = runtimecore.ErrTaskNotCancelable
	response = performJSONRequest(handler, http.MethodPost, "/v1/tasks/task/cancel", `{}`, apiToken)
	if response.Code != http.StatusConflict {
		t.Fatalf("rejected cancel status = %d, want 409", response.Code)
	}
	if got := notifications.Load(); got != 2 {
		t.Fatalf("notifications after rejected writes = %d, want 2", got)
	}
}
