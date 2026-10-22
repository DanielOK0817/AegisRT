package faultfixture

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aegisrt/internal/agentprotocol"
)

const validInput = `{"task_id":"task-1","workflow_id":"workflow-1","attempt_id":"task-1-attempt-1","attempt_number":1,"idempotency_key":"key-1","goal":"fault smoke"}`

func TestRunSuccessEmitsOneDeterministicArtifact(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"--mode=success"}, strings.NewReader(validInput), &stdout, &stderr); code != 0 {
		t.Fatalf("Run exit code = %d, stderr=%q", code, stderr.String())
	}
	envelope, err := agentprotocol.Parse(stdout.Bytes(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(envelope.Artifacts) != 1 || envelope.Artifacts[0].Name != "result" ||
		envelope.Artifacts[0].Content != "deterministic fault fixture result" {
		t.Fatalf("output = %#v", envelope)
	}
}

func TestRunWritesAttemptReadyMarkerBeforeWork(t *testing.T) {
	directory := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := Run(
		context.Background(),
		[]string{"--mode=success", "--ready-dir=" + directory},
		strings.NewReader(validInput),
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("Run exit code = %d, stderr=%q", code, stderr.String())
	}
	marker := filepath.Join(directory, "task-1-attempt-1.ready")
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "READY\n" {
		t.Fatalf("ready marker = %q", data)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "task-1-attempt-1.ready" {
		t.Fatalf("published readiness files = %#v", entries)
	}
}

func TestRunExitModeReturnsDeclaredNonZeroCodeWithoutOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"--mode=exit"}, strings.NewReader(validInput), &stdout, &stderr); code != 7 {
		t.Fatalf("Run exit code = %d", code)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "FAULT_EXIT") {
		t.Fatalf("unexpected fixture streams: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunRejectsInvalidInputAndHonorsCancellation(t *testing.T) {
	if code := Run(context.Background(), []string{"--mode=success"}, strings.NewReader(`{"task_id":"missing"}`), &bytes.Buffer{}, &bytes.Buffer{}); code != 2 {
		t.Fatalf("invalid input exit code = %d", code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	if code := Run(ctx, []string{"--mode=success", "--hold=1s"}, strings.NewReader(validInput), &bytes.Buffer{}, &bytes.Buffer{}); code != 1 {
		t.Fatalf("canceled fixture exit code = %d", code)
	}
	if time.Since(started) > 100*time.Millisecond {
		t.Fatal("canceled fixture ignored context")
	}
}

func TestInputContractRejectsUnknownFields(t *testing.T) {
	var decoded input
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSuffix(validInput, "}") + `,"secret":"x"}`))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err == nil {
		t.Fatal("unknown input field was accepted")
	}
}
