package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"aegisrt/internal/contextstate"
	"aegisrt/internal/contextvm"
	"aegisrt/internal/runtimeapp"
	"aegisrt/internal/taskgraph"
)

const (
	testManifestID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	nextManifestID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type fakeContextControl struct {
	mu            sync.Mutex
	inspectResult runtimeapp.ContextSnapshot
	inspectErr    error
	appendResult  runtimeapp.AppendContextResult
	appendErr     error
	inspectCalls  int
	appendCalls   int
	lastTaskID    string
	lastAppend    runtimeapp.AppendContextCommand
	outputResult  runtimeapp.OutputSummary
	outputErr     error
	outputCalls   int
}

func (f *fakeContextControl) InspectContext(taskID string) (runtimeapp.ContextSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inspectCalls++
	f.lastTaskID = taskID
	return f.inspectResult, f.inspectErr
}

func (f *fakeContextControl) AppendContext(_ context.Context, command runtimeapp.AppendContextCommand) (runtimeapp.AppendContextResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.appendCalls++
	f.lastAppend = command
	return f.appendResult, f.appendErr
}

func (f *fakeContextControl) InspectOutputSummary(taskID string) (runtimeapp.OutputSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.outputCalls++
	f.lastTaskID = taskID
	return f.outputResult, f.outputErr
}

func contextHandler(control ContextControl, token string) http.Handler {
	return NewServer(Config{ContextControl: control, AuthToken: []byte(token)}).Handler()
}

func contextRequest(handler http.Handler, method, target, body, token, idempotencyKey, ifMatch string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" || method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestInspectContextIsAuthenticatedAndReturnsOnlyMetadata(t *testing.T) {
	control := &fakeContextControl{inspectResult: runtimeapp.ContextSnapshot{
		TaskID: "task-1",
		Manifest: contextstate.Snapshot{
			ManifestID: testManifestID, Revision: 3, PageCount: 2,
			LogicalTokens: 12, PinnedTokens: 4,
		},
	}}
	handler := contextHandler(control, apiToken)

	unauthorized := contextRequest(handler, http.MethodGet, "/v1/tasks/task-1/context", "", "", "", "")
	if unauthorized.Code != http.StatusUnauthorized || control.inspectCalls != 0 {
		t.Fatalf("unauthorized status=%d calls=%d", unauthorized.Code, control.inspectCalls)
	}
	response := contextRequest(handler, http.MethodGet, "/v1/tasks/task-1/context", "", apiToken, "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("ETag"); got != `"`+testManifestID+`"` {
		t.Fatalf("ETag=%q", got)
	}
	if control.lastTaskID != "task-1" || control.inspectCalls != 1 {
		t.Fatalf("task=%q calls=%d", control.lastTaskID, control.inspectCalls)
	}
	body := response.Body.String()
	for _, forbidden := range []string{"content", "content_hash", "workflow_id", "agent_id"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, body)
		}
	}
}

func TestAppendContextBuildsPrincipalFreeCommandAndSetsETag(t *testing.T) {
	secret := `untrusted ; && $(whoami) "context"`
	control := &fakeContextControl{appendResult: runtimeapp.AppendContextResult{
		TaskID: "task-1", PageID: contextvm.PageID(testManifestID), TokenCount: 5,
		ManifestID: nextManifestID, Revision: 2, ManifestChanged: true,
	}}
	response := contextRequest(
		contextHandler(control, apiToken), http.MethodPost, "/v1/tasks/task-1/context/pages",
		fmt.Sprintf(`{"content":%q,"visibility":"agent","pin":true,"cold":true}`, secret),
		apiToken, "write-1", `"`+testManifestID+`"`,
	)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("ETag"); got != `"`+nextManifestID+`"` {
		t.Fatalf("ETag=%q", got)
	}
	command := control.lastAppend
	if control.appendCalls != 1 || command.TaskID != "task-1" || command.Content != secret ||
		command.Visibility != contextstate.VisibilityAgent || !command.Pin || !command.Cold ||
		command.IdempotencyKey != "write-1" || command.ExpectedManifestID != testManifestID {
		t.Fatalf("unexpected append command: %#v", command)
	}
	if strings.Contains(response.Body.String(), secret) || strings.Contains(response.Body.String(), "content_hash") {
		t.Fatalf("response leaked content: %s", response.Body.String())
	}
}

func TestAppendContextReplayReturnsOK(t *testing.T) {
	control := &fakeContextControl{appendResult: runtimeapp.AppendContextResult{
		TaskID: "task-1", PageID: contextvm.PageID(testManifestID), TokenCount: 1,
		ManifestID: nextManifestID, Revision: 2, ManifestChanged: true, IdempotentReplay: true,
	}}
	response := contextRequest(contextHandler(control, apiToken), http.MethodPost,
		"/v1/tasks/task-1/context/pages", `{"content":"safe","visibility":"workflow"}`,
		apiToken, "write-1", `"`+testManifestID+`"`)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAppendContextRequiresAuthenticationJSONAndConcurrencyHeaders(t *testing.T) {
	validBody := `{"content":"safe","visibility":"agent"}`
	tests := []struct {
		name        string
		token       string
		contentType string
		key         string
		ifMatch     string
		want        int
	}{
		{name: "missing auth", contentType: "application/json", key: "write-1", ifMatch: `"` + testManifestID + `"`, want: http.StatusUnauthorized},
		{name: "missing content type", token: apiToken, key: "write-1", ifMatch: `"` + testManifestID + `"`, want: http.StatusUnsupportedMediaType},
		{name: "missing idempotency key", token: apiToken, contentType: "application/json", ifMatch: `"` + testManifestID + `"`, want: http.StatusBadRequest},
		{name: "missing precondition", token: apiToken, contentType: "application/json", key: "write-1", want: http.StatusPreconditionRequired},
		{name: "weak precondition", token: apiToken, contentType: "application/json", key: "write-1", ifMatch: `W/"` + testManifestID + `"`, want: http.StatusBadRequest},
		{name: "unquoted precondition", token: apiToken, contentType: "application/json", key: "write-1", ifMatch: testManifestID, want: http.StatusBadRequest},
		{name: "multiple preconditions", token: apiToken, contentType: "application/json", key: "write-1", ifMatch: `"` + testManifestID + `", "` + nextManifestID + `"`, want: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			control := &fakeContextControl{}
			request := httptest.NewRequest(http.MethodPost, "/v1/tasks/task-1/context/pages", strings.NewReader(validBody))
			if test.token != "" {
				request.Header.Set("Authorization", "Bearer "+test.token)
			}
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			if test.key != "" {
				request.Header.Set("Idempotency-Key", test.key)
			}
			if test.ifMatch != "" {
				request.Header.Set("If-Match", test.ifMatch)
			}
			response := httptest.NewRecorder()
			contextHandler(control, apiToken).ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
			if control.appendCalls != 0 {
				t.Fatalf("append calls=%d", control.appendCalls)
			}
		})
	}
}

func TestAppendContextRejectsDuplicateControlHeaders(t *testing.T) {
	for _, header := range []string{"Idempotency-Key", "If-Match"} {
		t.Run(header, func(t *testing.T) {
			control := &fakeContextControl{}
			request := httptest.NewRequest(http.MethodPost, "/v1/tasks/task-1/context/pages",
				strings.NewReader(`{"content":"safe","visibility":"agent"}`))
			request.Header.Set("Authorization", "Bearer "+apiToken)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Add("Idempotency-Key", "write-1")
			request.Header.Add("If-Match", `"`+testManifestID+`"`)
			request.Header.Add(header, "duplicate")
			response := httptest.NewRecorder()
			contextHandler(control, apiToken).ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || control.appendCalls != 0 {
				t.Fatalf("status=%d calls=%d body=%s", response.Code, control.appendCalls, response.Body.String())
			}
		})
	}
}

func TestAppendContextUsesStrictBoundedJSONWithoutOwnerFields(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "unknown", body: `{"content":"secret","visibility":"agent","workflow_id":"other"}`, want: http.StatusBadRequest},
		{name: "duplicate key", body: `{"content":"first","content":"secret","visibility":"agent"}`, want: http.StatusBadRequest},
		{name: "trailing", body: `{"content":"secret","visibility":"agent"}{}`, want: http.StatusBadRequest},
		{name: "null", body: `null`, want: http.StatusBadRequest},
		{name: "array", body: `[]`, want: http.StatusBadRequest},
		{name: "empty", body: ``, want: http.StatusBadRequest},
		{name: "too large", body: `{"content":"` + strings.Repeat("x", int(contextRequestBytes)) + `","visibility":"agent"}`, want: http.StatusRequestEntityTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			control := &fakeContextControl{}
			response := contextRequest(contextHandler(control, apiToken), http.MethodPost,
				"/v1/tasks/task-1/context/pages", test.body, apiToken, "write-1", `"`+testManifestID+`"`)
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
			if control.appendCalls != 0 {
				t.Fatalf("append calls=%d", control.appendCalls)
			}
			if strings.Contains(response.Body.String(), "secret") {
				t.Fatalf("error leaked content: %s", response.Body.String())
			}
		})
	}
}

func TestContextErrorMappingIsStableAndSanitized(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
		code string
	}{
		{name: "task missing", err: taskgraph.ErrTaskNotFound, want: http.StatusNotFound, code: "task_not_found"},
		{name: "idempotency", err: contextstate.ErrIdempotencyConflict, want: http.StatusConflict, code: "idempotency_conflict"},
		{name: "reference", err: contextstate.ErrReferenceConflict, want: http.StatusConflict, code: "context_reference_conflict"},
		{name: "precondition", err: contextstate.ErrPreconditionFailed, want: http.StatusPreconditionFailed, code: "manifest_precondition_failed"},
		{name: "scope", err: contextstate.ErrScopeForbidden, want: http.StatusForbidden, code: "context_scope_forbidden"},
		{name: "page bytes", err: contextstate.ErrPageTooLarge, want: http.StatusRequestEntityTooLarge, code: "context_content_too_large"},
		{name: "invalid content", err: contextstate.ErrInvalidContent, want: http.StatusUnprocessableEntity, code: "invalid_context"},
		{name: "page tokens", err: contextstate.ErrPageTokenLimit, want: http.StatusUnprocessableEntity, code: "context_token_limit"},
		{name: "manifest tokens", err: contextstate.ErrManifestTokenLimit, want: http.StatusUnprocessableEntity, code: "context_token_limit"},
		{name: "pinned tokens", err: contextstate.ErrPinnedTokenLimit, want: http.StatusUnprocessableEntity, code: "pinned_token_limit"},
		{name: "disabled", err: contextstate.ErrTaskContextDisabled, want: http.StatusUnprocessableEntity, code: "context_token_limit"},
		{name: "page count", err: contextstate.ErrManifestPageLimit, want: http.StatusUnprocessableEntity, code: "context_page_limit"},
		{name: "bad key", err: contextstate.ErrInvalidIdempotencyKey, want: http.StatusBadRequest, code: "invalid_idempotency_key"},
		{name: "bad binding", err: contextstate.ErrInvalidTaskBinding, want: http.StatusBadRequest, code: "invalid_task_binding"},
		{name: "capacity", err: contextstate.ErrCheckpointCapacity, want: http.StatusInsufficientStorage, code: "context_capacity"},
		{name: "unavailable", err: contextstate.ErrUnavailable, want: http.StatusServiceUnavailable, code: "context_unavailable"},
		{name: "internal", err: errors.New("secret internal path"), want: http.StatusInternalServerError, code: "context_failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			control := &fakeContextControl{appendErr: fmt.Errorf("wrapped secret: %w", test.err)}
			response := contextRequest(contextHandler(control, apiToken), http.MethodPost,
				"/v1/tasks/task-1/context/pages", `{"content":"private body","visibility":"agent"}`,
				apiToken, "write-1", `"`+testManifestID+`"`)
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
			body := response.Body.String()
			if !strings.Contains(body, `"code":"`+test.code+`"`) {
				t.Fatalf("missing code %q: %s", test.code, body)
			}
			if strings.Contains(body, "wrapped secret") || strings.Contains(body, "secret internal path") ||
				strings.Contains(body, "private body") {
				t.Fatalf("error leaked sensitive detail: %s", body)
			}
		})
	}
}

func TestContextUnavailableWhenControlIsNotConfigured(t *testing.T) {
	handler := contextHandler(nil, apiToken)
	get := contextRequest(handler, http.MethodGet, "/v1/tasks/task-1/context", "", apiToken, "", "")
	if get.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET status=%d body=%s", get.Code, get.Body.String())
	}
	post := contextRequest(handler, http.MethodPost, "/v1/tasks/task-1/context/pages",
		`{"content":"safe","visibility":"agent"}`, apiToken, "write-1", `"`+testManifestID+`"`)
	if post.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST status=%d body=%s", post.Code, post.Body.String())
	}
}

func TestContextDurabilityFailureNotifiesRuntimeService(t *testing.T) {
	var notifications int
	control := &fakeContextControl{appendErr: contextstate.ErrUnavailable}
	handler := NewServer(Config{
		ContextControl: control, AuthToken: []byte(apiToken),
		Notify: func() { notifications++ },
	}).Handler()
	response := contextRequest(handler, http.MethodPost, "/v1/tasks/task-1/context/pages",
		`{"content":"safe","visibility":"agent"}`, apiToken, "write-1", `"`+testManifestID+`"`)
	if response.Code != http.StatusServiceUnavailable || notifications != 1 {
		t.Fatalf("status=%d notifications=%d body=%s", response.Code, notifications, response.Body.String())
	}
}
