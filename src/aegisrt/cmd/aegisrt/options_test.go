package main

import (
	"bytes"
	"runtime"
	"testing"
	"time"
)

func TestParseOptionsAcceptsSecureDaemonConfiguration(t *testing.T) {
	var stderr bytes.Buffer
	options, err := parseOptions([]string{
		"-listen", "127.0.0.1:0",
		"-agents-file", "agents.json",
		"-auth-token-file", "api.token",
		"-state-file", "runtime.state",
		"-max-workers", "3",
		"-poll-interval", "25ms",
		"-event-capacity", "128",
		"-migrate-legacy-state",
	}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if options.maxWorkers != 3 || options.pollInterval != 25*time.Millisecond ||
		options.eventCapacity != 128 || options.agentsFile != "agents.json" ||
		options.stateFile != "runtime.state" || options.ephemeral || !options.migrateLegacyState {
		t.Fatalf("unexpected options: %#v", options)
	}
}

func TestParseOptionsRejectsUnsafeOrIncompleteConfiguration(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "non-loopback", args: []string{"-listen", "0.0.0.0:8080", "-agents-file", "agents.json", "-auth-token-file", "api.token"}},
		{name: "missing agents", args: []string{"-auth-token-file", "api.token"}},
		{name: "missing token", args: []string{"-agents-file", "agents.json"}},
		{name: "missing state mode", args: []string{"-agents-file", "agents.json", "-auth-token-file", "api.token"}},
		{name: "conflicting state mode", args: []string{"-agents-file", "agents.json", "-auth-token-file", "api.token", "-state-file", "runtime.state", "-ephemeral"}},
		{name: "ephemeral state migration", args: []string{"-agents-file", "agents.json", "-auth-token-file", "api.token", "-ephemeral", "-migrate-legacy-state"}},
		{name: "zero workers", args: []string{"-agents-file", "agents.json", "-auth-token-file", "api.token", "-max-workers", "0"}},
		{name: "excessive workers", args: []string{"-agents-file", "agents.json", "-auth-token-file", "api.token", "-max-workers", "257"}},
		{name: "zero interval", args: []string{"-agents-file", "agents.json", "-auth-token-file", "api.token", "-poll-interval", "0s"}},
		{name: "busy interval", args: []string{"-agents-file", "agents.json", "-auth-token-file", "api.token", "-poll-interval", "1ms"}},
		{name: "zero events", args: []string{"-agents-file", "agents.json", "-auth-token-file", "api.token", "-event-capacity", "0"}},
		{name: "excessive events", args: []string{"-agents-file", "agents.json", "-auth-token-file", "api.token", "-event-capacity", "1000001"}},
		{name: "positional", args: []string{"-agents-file", "agents.json", "-auth-token-file", "api.token", "unexpected"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			if _, err := parseOptions(test.args, &stderr); err == nil {
				t.Fatal("parseOptions accepted unsafe configuration")
			}
		})
	}
}

func TestParseOptionsAcceptsExplicitEphemeralMode(t *testing.T) {
	var stderr bytes.Buffer
	options, err := parseOptions([]string{
		"-agents-file", "agents.json", "-auth-token-file", "api.token", "-ephemeral",
	}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !options.ephemeral || options.stateFile != "" {
		t.Fatalf("unexpected state mode: %#v", options)
	}
}

func TestParseOptionsHandlesAutomaticDelegatedCgroupModeByPlatform(t *testing.T) {
	var stderr bytes.Buffer
	options, err := parseOptions([]string{
		"-agents-file", "agents.json",
		"-auth-token-file", "api.token",
		"-state-file", "runtime.state",
		"-cgroup-root", "auto",
	}, &stderr)
	if runtime.GOOS != "linux" {
		if err == nil {
			t.Fatal("automatic cgroup mode was accepted outside Linux")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if options.cgroupRoot != "auto" {
		t.Fatalf("unexpected cgroup root: %q", options.cgroupRoot)
	}
}

func TestParseOptionsVersionNeedsNoRuntimeFiles(t *testing.T) {
	var stderr bytes.Buffer
	options, err := parseOptions([]string{"-version"}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !options.showVersion {
		t.Fatal("version flag was not retained")
	}
}

func TestValidateLoopbackAddress(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8080", "localhost:0", "[::1]:8080"} {
		if err := validateLoopbackAddress(address); err != nil {
			t.Fatalf("%q rejected: %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:8080", ":8080", "example.com:80", "bad"} {
		if err := validateLoopbackAddress(address); err == nil {
			t.Fatalf("%q accepted", address)
		}
	}
}
