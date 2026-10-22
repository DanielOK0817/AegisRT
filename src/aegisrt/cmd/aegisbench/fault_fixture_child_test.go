package main

import (
	"bytes"
	"context"
	"testing"
)

func TestRunHiddenChildCommandIsSilentAndBounded(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode, handled := runHiddenCommand(
		context.Background(),
		[]string{hiddenFaultChildCommand, "--hold=1ms"},
		&bytes.Buffer{}, &stdout, &stderr,
	)
	if !handled || exitCode != 0 {
		t.Fatalf("handled=%t exitCode=%d", handled, exitCode)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("child emitted output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunHiddenChildCommandRejectsUnsafeHoldSilently(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode, handled := runHiddenCommand(
		context.Background(),
		[]string{hiddenFaultChildCommand, "--hold=31s"},
		&bytes.Buffer{}, &stdout, &stderr,
	)
	if !handled || exitCode != 2 {
		t.Fatalf("handled=%t exitCode=%d", handled, exitCode)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("invalid child emitted output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
