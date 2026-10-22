package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"aegisrt/internal/agentprotocol"
	"aegisrt/internal/benchmark"
)

func TestTerminationSignalsIncludeSystemdSIGTERM(t *testing.T) {
	signals := terminationSignals()
	if !reflect.DeepEqual(signals, []os.Signal{os.Interrupt, syscall.SIGTERM}) {
		t.Fatalf("termination signals = %v", signals)
	}
}

func TestRunHiddenCommandPassesArgumentsAndStreamsToFaultFixture(t *testing.T) {
	stdin := strings.NewReader(`{"task_id":"task-1","workflow_id":"workflow-1","attempt_id":"attempt-1","attempt_number":1,"idempotency_key":"key-1","goal":"complete fixture task"}`)
	var stdout, stderr bytes.Buffer

	exitCode, handled := runHiddenCommand(
		context.Background(),
		[]string{hiddenFaultFixtureCommand, "--mode", "success"},
		stdin,
		&stdout,
		&stderr,
	)
	if !handled || exitCode != 0 {
		t.Fatalf("hidden command handled/code = %v/%d", handled, exitCode)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), `"version":"`+agentprotocol.VersionV1+`"`) ||
		!strings.Contains(stdout.String(), "deterministic fault fixture result") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunHiddenCommandPropagatesFaultFixtureExitCode(t *testing.T) {
	stdin := strings.NewReader(`{"task_id":"task-1","workflow_id":"workflow-1","attempt_id":"attempt-1","attempt_number":1,"idempotency_key":"key-1","goal":"complete fixture task"}`)
	var stdout, stderr bytes.Buffer

	exitCode, handled := runHiddenCommand(
		context.Background(),
		[]string{hiddenFaultFixtureCommand, "--mode", "exit"},
		stdin,
		&stdout,
		&stderr,
	)
	if !handled || exitCode != 7 {
		t.Fatalf("hidden command handled/code = %v/%d", handled, exitCode)
	}
	if stdout.Len() != 0 || stderr.String() != "FAULT_EXIT\n" {
		t.Fatalf("stdout/stderr = %q/%q", stdout.String(), stderr.String())
	}
}

func TestRunHiddenCommandIgnoresPublicCommands(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode, handled := runHiddenCommand(
		context.Background(),
		[]string{string(commandScheduler)},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if handled || exitCode != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("public command dispatch = handled=%v code=%d stdout=%q stderr=%q", handled, exitCode, stdout.String(), stderr.String())
	}
}

func TestParseArgsSupportsEvidenceCommands(t *testing.T) {
	for _, kind := range []commandKind{commandScheduler, commandContext, commandFault} {
		t.Run(string(kind), func(t *testing.T) {
			options, err := parseArgs([]string{string(kind), "--output", "result", "--seeds", "7,11", "--repetitions", "3"})
			if err != nil {
				t.Fatal(err)
			}
			if options.kind != kind || options.output != "result" ||
				!reflect.DeepEqual(options.seeds, []uint32{7, 11}) || options.repetitions != 3 {
				t.Fatalf("options = %#v", options)
			}
		})
	}
}

func TestParseArgsSupportsFixedFormalSchedulerProfile(t *testing.T) {
	options, err := parseArgs([]string{"scheduler", "--profile", "formal", "--output", "result"})
	if err != nil {
		t.Fatal(err)
	}
	want := benchmark.NewSchedulerFormalPlan()
	if options.profile != benchmark.SchedulerProfileFormal || !reflect.DeepEqual(options.schedulerPlan, want) {
		t.Fatalf("formal scheduler options = %#v", options)
	}
	if _, err := parseArgs([]string{"scheduler", "--profile", "formal", "--output", "result", "--seeds", "7"}); err == nil {
		t.Fatal("formal scheduler accepted a seed override")
	}
}

func TestUsageListsFaultWithoutLeakingHiddenFixture(t *testing.T) {
	if !strings.Contains(usageText, "aegisbench fault ") {
		t.Fatalf("public fault command missing from usage: %q", usageText)
	}
	if strings.Contains(usageText, hiddenFaultFixtureCommand) {
		t.Fatalf("hidden fixture command leaked into public usage")
	}
}

func TestFaultAgentCommandUsesCurrentExecutableAndHiddenPrefix(t *testing.T) {
	const executable = `C:\safe\aegisbench.exe`
	want := benchmark.FaultAgentCommand{
		Entrypoint: executable,
		ArgsPrefix: []string{hiddenFaultFixtureCommand},
	}
	if got := faultAgentCommand(executable); !reflect.DeepEqual(got, want) {
		t.Fatalf("fault command = %#v, want %#v", got, want)
	}
}

func TestParseArgsRejectsUnknownUnsafeOrIncompleteInput(t *testing.T) {
	tests := [][]string{
		{},
		{"unknown", "--output", "x", "--seeds", "1", "--repetitions", "1"},
		{"scheduler", "--output", "", "--seeds", "1", "--repetitions", "1"},
		{"context", "--output", "x", "--seeds", "", "--repetitions", "1"},
		{"scheduler", "--output", "x", "--seeds", "1,1", "--repetitions", "1"},
		{"context", "--output", "x", "--seeds", "bad", "--repetitions", "1"},
		{"context", "--output", "x", "--seeds", "1", "--repetitions", "0"},
		{"context", "--output", "x", "--seeds", "1", "--repetitions", "1", "extra"},
	}
	for _, args := range tests {
		if _, err := parseArgs(args); err == nil {
			t.Fatalf("parseArgs(%q) succeeded", args)
		}
	}
}

func TestParseArgsRejectsFaultPlanBeyondCollectorLimit(t *testing.T) {
	_, err := parseArgs([]string{
		"fault", "--output", "result",
		"--seeds", "0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16",
		"--repetitions", "1",
	})
	if err == nil || !strings.Contains(err.Error(), "16") {
		t.Fatalf("oversized fault plan error = %v", err)
	}
}

func TestEvidencePublishedMessageContainsOnlyKindAndRunID(t *testing.T) {
	const runID = "run-0123456789abcdef01234567"
	for _, kind := range []commandKind{commandScheduler, commandContext, commandFault} {
		want := string(kind) + " evidence published: " + runID
		if got := evidencePublishedMessage(kind, runID); got != want {
			t.Fatalf("message = %q, want %q", got, want)
		}
	}
}

func TestCollectionErrorMessageDoesNotExposeUnderlyingDetails(t *testing.T) {
	if got := collectionErrorMessage(errors.New(`open C:\Users\person\private: access denied`)); got != "evidence collection failed" {
		t.Fatalf("collection error message = %q", got)
	}
	if got := collectionErrorMessage(context.Canceled); got != "evidence collection canceled" {
		t.Fatalf("cancellation message = %q", got)
	}
}
