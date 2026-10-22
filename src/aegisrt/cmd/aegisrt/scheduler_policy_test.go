package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"aegisrt/internal/domain"
	"aegisrt/internal/runtimecore"
)

func TestParseOptionsAcceptsSupportedSchedulerPolicies(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "default", want: "dag-drcfs"},
		{name: "fifo", args: []string{"-scheduler-policy", "fifo"}, want: "fifo"},
		{name: "priority", args: []string{"-scheduler-policy", "priority"}, want: "priority"},
		{name: "dag-drcfs", args: []string{"-scheduler-policy", "dag-drcfs"}, want: "dag-drcfs"},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := []string{"-agents-file", "agents.json", "-auth-token-file", "api.token", "-ephemeral"}
			args = append(args, test.args...)
			options, err := parseOptions(args, &bytes.Buffer{})
			if err != nil {
				t.Fatal(err)
			}
			if options.schedulerPolicy != test.want {
				t.Fatalf("scheduler policy = %q, want %q", options.schedulerPolicy, test.want)
			}
		})
	}
}

func TestParseOptionsRejectsUnknownSchedulerPolicy(t *testing.T) {
	for _, value := range []string{"", "FIFO", "dag_drcfs", "unknown", " priority"} {
		t.Run(value, func(t *testing.T) {
			_, err := parseOptions([]string{
				"-agents-file", "agents.json", "-auth-token-file", "api.token", "-ephemeral",
				"-scheduler-policy", value,
			}, &bytes.Buffer{})
			if err == nil {
				t.Fatalf("scheduler policy %q was accepted", value)
			}
		})
	}
}

func TestBuildSchedulingPolicyFailsClosed(t *testing.T) {
	capacity := domain.ResourceVector{CPUMilli: 1000}
	for _, name := range []string{"fifo", "priority", "dag-drcfs"} {
		policy, err := buildSchedulingPolicy(name, capacity)
		if err != nil {
			t.Fatalf("build policy %q: %v", name, err)
		}
		if policy.Name() != name {
			t.Fatalf("policy name = %q, want %q", policy.Name(), name)
		}
	}
	if policy, err := buildSchedulingPolicy("unknown", capacity); err == nil || policy != nil {
		t.Fatalf("unknown policy result = (%v, %v), want nil policy and non-nil error", policy, err)
	}
}

func TestBuiltDaemonExposesConfiguredSchedulerPolicy(t *testing.T) {
	for _, name := range []string{"fifo", "priority", "dag-drcfs"} {
		t.Run(name, func(t *testing.T) {
			args := append(daemonArgs(t, "success", "127.0.0.1:0"), "-scheduler-policy", name)
			options, err := parseOptions(args, &bytes.Buffer{})
			if err != nil {
				t.Fatal(err)
			}
			app, err := buildDaemon(options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := app.close(); err != nil {
					t.Errorf("close daemon: %v", err)
				}
			})

			request := httptest.NewRequest(http.MethodGet, "/v1/runtime", nil)
			response := httptest.NewRecorder()
			app.handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var body struct {
				Stats struct {
					Policy string `json:"policy"`
				} `json:"stats"`
			}
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Stats.Policy != name {
				t.Fatalf("Runtime stats policy = %q, want %q", body.Stats.Policy, name)
			}
		})
	}
}

func TestBuiltDaemonRejectsStateFromDifferentSchedulerPolicy(t *testing.T) {
	args := daemonArgs(t, "success", "127.0.0.1:0")
	options, err := parseOptions(args, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := buildDaemon(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.close(); err != nil {
		t.Fatal(err)
	}

	options.schedulerPolicy = "fifo"
	second, err := buildDaemon(options)
	if second != nil {
		_ = second.close()
	}
	if !errors.Is(err, runtimecore.ErrInvalidCheckpoint) {
		t.Fatalf("buildDaemon error = %v, want ErrInvalidCheckpoint", err)
	}
}

func TestRuntimeConfigFingerprintIncludesSchedulerPolicy(t *testing.T) {
	capacity := domain.ResourceVector{CPUMilli: 1000}
	options := daemonOptions{maxWorkers: 1, pidsMax: 10, cpuPeriod: 100_000}
	fifo, err := runtimeConfigFingerprint("fifo", capacity, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	priority, err := runtimeConfigFingerprint("priority", capacity, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	if fifo == priority {
		t.Fatal("different scheduler policies produced the same configuration fingerprint")
	}
}
