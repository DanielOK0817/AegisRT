package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"aegisrt/internal/contextstate"
	"aegisrt/internal/contextvm"
	"aegisrt/internal/runtimeapp"
)

func TestHealth(t *testing.T) {
	server := NewServer(Config{})
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("security header = %q, want nosniff", got)
	}
}

func TestReadinessFailure(t *testing.T) {
	server := NewServer(Config{Ready: func() bool { return false }})
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
}

func TestRuntimeInfo(t *testing.T) {
	startedAt := time.Unix(100, 0).UTC()
	server := NewServer(Config{Version: "test", StartedAt: startedAt})
	request := httptest.NewRequest(http.MethodGet, "/v1/runtime", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	var body struct {
		Name      string    `json:"name"`
		Version   string    `json:"version"`
		StartedAt time.Time `json:"started_at"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Name != "aegisrt" || body.Version != "test" || !body.StartedAt.Equal(startedAt) {
		t.Fatalf("unexpected response: %+v", body)
	}
}

func TestRuntimeInfoIncludesSafeContextStorageMetrics(t *testing.T) {
	control := &statsContextControl{stats: contextstate.Stats{
		Durable: true, CheckpointHealthy: true, Generation: 4,
		ManifestCount: 3, HeadCount: 2,
		Store: contextvm.StoreStats{UniquePages: 5, UniqueBlobs: 3, ReusedTokens: 12},
	}}
	server := NewServer(Config{ContextControl: control})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/runtime", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		ContextStats *contextstate.Stats `json:"context_stats"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.ContextStats == nil || body.ContextStats.Generation != 4 ||
		body.ContextStats.Store.UniquePages != 5 || body.ContextStats.Store.UniqueBlobs != 3 {
		t.Fatalf("context stats = %#v", body.ContextStats)
	}
}

type statsContextControl struct {
	stats contextstate.Stats
}

func (*statsContextControl) InspectContext(string) (runtimeapp.ContextSnapshot, error) {
	return runtimeapp.ContextSnapshot{}, nil
}

func (*statsContextControl) AppendContext(context.Context, runtimeapp.AppendContextCommand) (runtimeapp.AppendContextResult, error) {
	return runtimeapp.AppendContextResult{}, nil
}

func (s *statsContextControl) ContextStats() contextstate.Stats { return s.stats }

func TestMethodNotAllowed(t *testing.T) {
	server := NewServer(Config{})
	request := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", response.Code)
	}
	if got := response.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("Allow = %q, want GET", got)
	}
}
