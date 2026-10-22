package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aegisrt/internal/controlplane"
	"aegisrt/internal/domain"
	"aegisrt/internal/resourcecontrol"
	"aegisrt/internal/runtimecore"
	"aegisrt/internal/statefile"
)

func TestBuiltDaemonHoldsExclusiveStateLease(t *testing.T) {
	var stderr bytes.Buffer
	options, err := parseOptions(daemonArgs(t, "success", "127.0.0.1:0"), &stderr)
	if err != nil {
		t.Fatal(err)
	}
	first, err := buildDaemon(options)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildDaemon(options)
	if !errors.Is(err, statefile.ErrLocked) || second != nil {
		_ = first.close()
		t.Fatalf("second buildDaemon = (%v, %v), want statefile.ErrLocked", second, err)
	}
	if err := first.close(); err != nil {
		t.Fatal(err)
	}
	third, err := buildDaemon(options)
	if err != nil {
		t.Fatalf("buildDaemon after lease release: %v", err)
	}
	if err := third.close(); err != nil {
		t.Fatal(err)
	}
}

func TestBuiltDaemonFailsClosedOnActiveRecoveryWithoutCgroup(t *testing.T) {
	var stderr bytes.Buffer
	options, err := parseOptions(daemonArgs(t, "success", "127.0.0.1:0"), &stderr)
	if err != nil {
		t.Fatal(err)
	}
	first, err := buildDaemon(options)
	if err != nil {
		t.Fatal(err)
	}
	provisionRecoveryContext(t, first)
	now := time.Now().UTC()
	if err := first.kernel.ApplyPatch([]domain.TaskSpec{recoveryTestTask()}, now); err != nil {
		_ = first.close()
		t.Fatal(err)
	}
	if _, err := first.kernel.Schedule(now); err != nil {
		_ = first.close()
		t.Fatal(err)
	}
	if err := first.close(); err != nil {
		t.Fatal(err)
	}

	second, err := buildDaemon(options)
	if second != nil {
		_ = second.close()
	}
	if !errors.Is(err, runtimecore.ErrRecoveryBlocked) {
		t.Fatalf("buildDaemon error = %v, want ErrRecoveryBlocked", err)
	}
}

func TestBuiltDaemonRecoversActiveStateAfterNamespacedCleanup(t *testing.T) {
	var stderr bytes.Buffer
	options, err := parseOptions(daemonArgs(t, "success", "127.0.0.1:0"), &stderr)
	if err != nil {
		t.Fatal(err)
	}
	options.cgroupRoot = t.TempDir()
	options, err = withRuntimeNamespace(options)
	if err != nil {
		t.Fatal(err)
	}
	first, err := buildDaemon(options)
	if err != nil {
		t.Fatal(err)
	}
	provisionRecoveryContext(t, first)
	now := time.Now().UTC()
	if err := first.kernel.ApplyPatch([]domain.TaskSpec{recoveryTestTask()}, now); err != nil {
		_ = first.close()
		t.Fatal(err)
	}
	if _, err := first.kernel.Schedule(now); err != nil {
		_ = first.close()
		t.Fatal(err)
	}
	groupPath := filepath.Join(
		options.cgroupRoot, "aegisrt", options.runtimeNamespace, controlplane.AttemptID("orphan", 1),
	)
	if err := os.MkdirAll(groupPath, 0o755); err != nil {
		_ = first.close()
		t.Fatal(err)
	}
	if err := first.close(); err != nil {
		t.Fatal(err)
	}

	second, err := buildDaemon(options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := second.close(); err != nil {
			t.Errorf("close recovered daemon: %v", err)
		}
	}()
	if len(second.recovery.RequeuedTaskIDs) != 1 || second.recovery.RequeuedTaskIDs[0] != "orphan" {
		t.Fatalf("recovery report = %+v, want orphan requeued", second.recovery)
	}
	if snapshot, exists := second.kernel.Task("orphan"); !exists || snapshot.Status != domain.TaskStatusReady {
		t.Fatalf("recovered task = (%+v, %v), want READY", snapshot, exists)
	}
	if _, err := os.Stat(groupPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan cgroup still exists: %v", err)
	}
}

func TestBuiltDaemonFailsClosedOnUnknownResidualCgroup(t *testing.T) {
	var stderr bytes.Buffer
	options, err := parseOptions(daemonArgs(t, "success", "127.0.0.1:0"), &stderr)
	if err != nil {
		t.Fatal(err)
	}
	options.cgroupRoot = t.TempDir()
	options, err = withRuntimeNamespace(options)
	if err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(options.cgroupRoot, "aegisrt", options.runtimeNamespace, "unknown-attempt")
	if err := os.MkdirAll(unknown, 0o755); err != nil {
		t.Fatal(err)
	}

	app, err := buildDaemon(options)
	if app != nil {
		_ = app.close()
	}
	if !errors.Is(err, resourcecontrol.ErrResidualCgroups) {
		t.Fatalf("buildDaemon error = %v, want ErrResidualCgroups", err)
	}
	if _, statErr := os.Stat(unknown); statErr != nil {
		t.Fatalf("unknown cgroup was mutated: %v", statErr)
	}
}

func TestBuiltDaemonRecordsSafeContextExecutionEvent(t *testing.T) {
	var stderr bytes.Buffer
	options, err := parseOptions(daemonArgs(t, "success", "127.0.0.1:0"), &stderr)
	if err != nil {
		t.Fatal(err)
	}
	app, err := buildDaemon(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.close(); err != nil {
			t.Errorf("close daemon app: %v", err)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serviceDone := make(chan error, 1)
	go func() { serviceDone <- app.service.Run(ctx) }()
	app.ready.Store(true)
	server := httptest.NewServer(app.handler)
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/task-graphs/patches",
		strings.NewReader(daemonPatch("event-task")))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+daemonTestToken)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	assertHTTPStatus(t, response, http.StatusCreated)

	deadline := time.Now().Add(5 * time.Second)
	for app.events.Stats().TotalAppended == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	events := app.events.Snapshot()
	if len(events) != 1 || events[0].Type != "run.succeeded" {
		t.Fatalf("events = %#v", events)
	}
	var payload struct {
		Context struct {
			ManifestID     string `json:"manifest_id"`
			PhysicalTokens int    `json:"physical_tokens"`
		} `json:"context"`
		AttemptStatus domain.AttemptStatus `json:"attempt_status"`
	}
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Context.ManifestID == "" || payload.AttemptStatus != domain.AttemptStatusSucceeded {
		t.Fatalf("safe event payload = %#v", payload)
	}
	serialized, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"automatic daemon execution", "stdout", "stderr", "\"ok\""} {
		if strings.Contains(string(serialized), forbidden) {
			t.Fatalf("event leaked %q: %s", forbidden, serialized)
		}
	}

	cancel()
	select {
	case err := <-serviceDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Runtime service did not stop")
	}
}

func TestBuiltDaemonExposesSafeAgentCatalog(t *testing.T) {
	var stderr bytes.Buffer
	options, err := parseOptions(daemonArgs(t, "success", "127.0.0.1:0"), &stderr)
	if err != nil {
		t.Fatal(err)
	}
	app, err := buildDaemon(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.close(); err != nil {
			t.Errorf("close daemon app: %v", err)
		}
	})

	request := httptest.NewRequest(http.MethodGet, "/v1/agents", nil)
	request.Header.Set("Authorization", "Bearer "+daemonTestToken)
	response := httptest.NewRecorder()
	app.handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, `"id":"test-agent"`) || !strings.Contains(body, `"max_concurrency":2`) {
		t.Fatalf("agent catalog = %s", body)
	}
	for _, forbidden := range []string{"entrypoint", "args", os.Args[0], daemonTestToken} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("agent catalog leaked %q: %s", forbidden, body)
		}
	}
}

func TestBuiltDaemonServesConsoleWithoutWeakeningAPIAuthentication(t *testing.T) {
	var stderr bytes.Buffer
	options, err := parseOptions(daemonArgs(t, "success", "127.0.0.1:0"), &stderr)
	if err != nil {
		t.Fatal(err)
	}
	app, err := buildDaemon(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.close(); err != nil {
			t.Errorf("close daemon app: %v", err)
		}
	})

	console := httptest.NewRecorder()
	app.handler.ServeHTTP(console, httptest.NewRequest(http.MethodGet, "/console/", nil))
	if console.Code != http.StatusOK || !strings.Contains(console.Body.String(), "AegisRT控制台") {
		t.Fatalf("console status=%d body=%q", console.Code, console.Body.String())
	}
	if strings.Contains(console.Body.String(), daemonTestToken) {
		t.Fatal("console HTML leaked daemon token")
	}
	if !strings.Contains(console.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatalf("console CSP=%q", console.Header().Get("Content-Security-Policy"))
	}

	tasks := httptest.NewRecorder()
	app.handler.ServeHTTP(tasks, httptest.NewRequest(http.MethodGet, "/v1/tasks", nil))
	if tasks.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized task list status=%d, want 401", tasks.Code)
	}
}
