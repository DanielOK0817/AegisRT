package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"
)

type daemonEventPage struct {
	StreamID        string    `json:"stream_id"`
	StreamStartedAt time.Time `json:"stream_started_at"`
	Events          []struct {
		ID      string    `json:"id"`
		Type    string    `json:"type"`
		Time    time.Time `json:"time"`
		TaskID  string    `json:"task_id"`
		Payload struct {
			AttemptStatus string `json:"attempt_status"`
			ExitCode      int    `json:"exit_code"`
			TimedOut      bool   `json:"timed_out"`
			Canceled      bool   `json:"canceled"`
			Timing        struct {
				ReadyAt    time.Time `json:"ready_at"`
				StartedAt  time.Time `json:"started_at"`
				FinishedAt time.Time `json:"finished_at"`
				QueueMS    int64     `json:"queue_ms"`
				RunMS      int64     `json:"run_ms"`
			} `json:"timing"`
		} `json:"payload"`
	} `json:"events"`
	NextAfterID string `json:"next_after_id"`
}

func TestDaemonEventsExposeSafeProjectionForRealTask(t *testing.T) {
	harness := startDaemonHarness(t, "event-secrets")
	response := harness.request(t, http.MethodPost, "/v1/task-graphs/patches", daemonEventPatch("event-safe"), true)
	assertHTTPStatus(t, response, http.StatusCreated)
	page, raw := waitForDaemonEventPage(t, harness, "event-safe")
	if page.StreamID == "" || page.StreamStartedAt.IsZero() || len(page.Events) != 1 ||
		page.Events[0].ID != "1" || page.Events[0].Type != "run.succeeded" ||
		page.Events[0].Payload.AttemptStatus != "SUCCEEDED" {
		t.Fatalf("event page = %+v", page)
	}
	timing := page.Events[0].Payload.Timing
	if timing.ReadyAt.IsZero() || timing.StartedAt.Before(timing.ReadyAt) ||
		timing.FinishedAt.Before(timing.StartedAt) || page.Events[0].Time.Before(timing.FinishedAt) ||
		timing.QueueMS != timing.StartedAt.Sub(timing.ReadyAt).Milliseconds() ||
		timing.RunMS != timing.FinishedAt.Sub(timing.StartedAt).Milliseconds() {
		t.Fatalf("invalid real task timing: event=%+v timing=%+v", page.Events[0], timing)
	}
	waitForDaemonTaskStatus(t, harness, "event-safe", "SUCCEEDED")
	for _, forbidden := range [][]byte{
		[]byte("automatic daemon execution"), []byte(daemonEventArtifactSecret),
		[]byte(daemonEventStderrSecret), []byte(`"goal"`), []byte(`"artifacts"`),
		[]byte(`"stdout"`), []byte(`"stderr"`),
	} {
		if bytes.Contains(raw, forbidden) {
			t.Fatalf("event response leaked %q: %s", forbidden, raw)
		}
	}
}

func daemonEventPatch(taskID string) string {
	return fmt.Sprintf(`{"tasks":[{"id":%q,"workflow_id":"daemon-e2e","agent_id":"test-agent","goal":"automatic daemon execution","priority":1,"resources":{"token_budget":128},"max_attempts":1,"timeout_ms":2000,"idempotency_key":%q}]}`,
		taskID, "key-"+taskID)
}

func TestDaemonEventCursorIsRejectedAcrossDurableRestart(t *testing.T) {
	directory := t.TempDir()
	first := startDaemonHarnessInDirectory(t, "success", directory)
	response := first.request(t, http.MethodPost, "/v1/task-graphs/patches", daemonPatch("epoch-a"), true)
	assertHTTPStatus(t, response, http.StatusCreated)
	waitForDaemonTaskStatus(t, first, "epoch-a", "SUCCEEDED")
	firstPage, _ := waitForDaemonEventPage(t, first, "epoch-a")
	assertRuntimeStartedAt(t, first, firstPage.StreamStartedAt)
	first.stop(t)

	second := startDaemonHarnessInDirectory(t, "success", directory)
	response = second.request(t, http.MethodPost, "/v1/task-graphs/patches", daemonPatch("epoch-b"), true)
	assertHTTPStatus(t, response, http.StatusCreated)
	waitForDaemonTaskStatus(t, second, "epoch-b", "SUCCEEDED")
	secondPage, _ := waitForDaemonEventPage(t, second, "epoch-b")
	assertRuntimeStartedAt(t, second, secondPage.StreamStartedAt)
	assertDaemonTasksPresent(t, second, "epoch-a", "epoch-b")
	if firstPage.StreamID == secondPage.StreamID || len(secondPage.Events) != 1 ||
		secondPage.Events[0].ID != "1" || secondPage.Events[0].TaskID != "epoch-b" {
		t.Fatalf("event epochs first=%+v second=%+v", firstPage, secondPage)
	}

	staleTarget := "/v1/events?after_id=1&stream_id=" + url.QueryEscape(firstPage.StreamID)
	response = second.request(t, http.MethodGet, staleTarget, "", true)
	if response.StatusCode != http.StatusConflict {
		payload, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("stale cursor status=%d body=%s", response.StatusCode, payload)
	}
	var conflict struct {
		StreamID string `json:"stream_id"`
		Error    struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	decodeErr := json.NewDecoder(response.Body).Decode(&conflict)
	response.Body.Close()
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if conflict.Error.Code != "stale_event_cursor" || conflict.StreamID != secondPage.StreamID {
		t.Fatalf("stale cursor response = %+v", conflict)
	}

	currentTarget := "/v1/events?after_id=0&stream_id=" + url.QueryEscape(secondPage.StreamID)
	current, _ := readDaemonEventPage(t, second, currentTarget)
	if len(current.Events) != 1 || current.Events[0].ID != "1" || current.Events[0].TaskID != "epoch-b" {
		t.Fatalf("current stream replay = %+v", current)
	}
}

func assertDaemonTasksPresent(t *testing.T, harness *daemonHarness, taskIDs ...string) {
	t.Helper()
	response := harness.request(t, http.MethodGet, "/v1/tasks", "", true)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(response.Body)
		t.Fatalf("tasks status=%d body=%s", response.StatusCode, payload)
	}
	var body struct {
		Tasks []struct {
			Task struct {
				ID string `json:"id"`
			} `json:"task"`
		} `json:"tasks"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	found := make(map[string]bool, len(body.Tasks))
	for _, item := range body.Tasks {
		found[item.Task.ID] = true
	}
	for _, taskID := range taskIDs {
		if !found[taskID] {
			t.Fatalf("durable task %q missing after restart; found=%v", taskID, found)
		}
	}
}

func waitForDaemonEventPage(t *testing.T, harness *daemonHarness, taskID string) (daemonEventPage, []byte) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		page, raw := readDaemonEventPage(t, harness, "/v1/events")
		for _, event := range page.Events {
			if event.TaskID == taskID {
				return page, raw
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("event for task %q was not observed", taskID)
	return daemonEventPage{}, nil
}

func readDaemonEventPage(t *testing.T, harness *daemonHarness, target string) (daemonEventPage, []byte) {
	t.Helper()
	response := harness.request(t, http.MethodGet, target, "", true)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("event query status=%d body=%s", response.StatusCode, raw)
	}
	var page daemonEventPage
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	return page, raw
}

func assertRuntimeStartedAt(t *testing.T, harness *daemonHarness, want time.Time) {
	t.Helper()
	response := harness.request(t, http.MethodGet, "/v1/runtime", "", false)
	defer response.Body.Close()
	var body struct {
		StartedAt time.Time `json:"started_at"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.StartedAt.Equal(want) {
		t.Fatalf("runtime started_at=%s, event stream started_at=%s", body.StartedAt, want)
	}
}
