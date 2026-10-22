package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestVersionFlag(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := run(context.Background(), []string{"-version"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(stdout.String()); got != version {
		t.Fatalf("version output = %q, want %q", got, version)
	}
}

func TestInvalidFlag(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := run(context.Background(), []string{"-unknown"}, &stdout, &stderr); err == nil {
		t.Fatal("expected invalid flag error")
	}
}

func TestRunStartsAndShutsDownOnCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := run(ctx, daemonArgs(t, "success", "127.0.0.1:0"), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "AegisRT started") {
		t.Fatalf("startup log missing: %q", stderr.String())
	}
}

func TestRunRejectsInvalidCapacity(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	args := append(daemonArgs(t, "success", "127.0.0.1:0"), "-cpu-milli", "-1")
	err := run(context.Background(), args, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "create scheduling policy") {
		t.Fatalf("error = %v, want capacity validation error", err)
	}
}

func TestRunRequiresLocalSecurityFiles(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := run(context.Background(), nil, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "agents-file") {
		t.Fatalf("error = %v, want agents-file validation", err)
	}
}
