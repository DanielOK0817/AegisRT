// Package faultfixture provides a bounded, deterministic local Agent used by
// AegisRT's functional fault experiments. It performs no network access.
package faultfixture

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aegisrt/internal/agentprotocol"
)

const maxInputBytes = 1 << 20

type input struct {
	TaskID         string `json:"task_id"`
	WorkflowID     string `json:"workflow_id"`
	AttemptID      string `json:"attempt_id"`
	AttemptNumber  int    `json:"attempt_number"`
	IdempotencyKey string `json:"idempotency_key"`
	Goal           string `json:"goal"`
	ContextRef     string `json:"context_ref,omitempty"`
	Context        string `json:"context,omitempty"`
}

func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return run(ctx, args, stdin, stdout, stderr, newFixtureChild)
}

func run(
	ctx context.Context,
	args []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	newChild childFactory,
) int {
	if ctx == nil || stdin == nil || stdout == nil || stderr == nil {
		return 2
	}
	flags := flag.NewFlagSet("fault-agent-fixture", flag.ContinueOnError)
	flags.SetOutput(stderr)
	mode := flags.String("mode", "", "fixture mode")
	hold := flags.Duration("hold", 0, "bounded deterministic hold")
	readyDir := flags.String("ready-dir", "", "optional attempt readiness directory")
	spawnChild := flags.Bool("spawn-child", false, "spawn a bounded deterministic child")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}
	if (*mode != "success" && *mode != "exit") || *hold < 0 || *hold > maxHold {
		fmt.Fprintln(stderr, "FAULT_INVALID_OPTIONS")
		return 2
	}
	value, err := decodeInput(stdin)
	if err != nil {
		fmt.Fprintln(stderr, "FAULT_INVALID_INPUT")
		return 2
	}
	if *spawnChild {
		child, childErr := newChild(maxHold)
		if childErr != nil || child.Start() != nil {
			fmt.Fprintln(stderr, "FAULT_CHILD_START_FAILED")
			return 1
		}
		defer reapChild(child)
	}
	if *readyDir != "" {
		if err := signalReady(*readyDir, value.AttemptID); err != nil {
			fmt.Fprintln(stderr, "FAULT_READY_FAILED")
			return 1
		}
	}
	if *hold > 0 {
		timer := time.NewTimer(*hold)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			fmt.Fprintln(stderr, "FAULT_CANCELED")
			return 1
		case <-timer.C:
		}
	}
	if *mode == "exit" {
		fmt.Fprintln(stderr, "FAULT_EXIT")
		return 7
	}
	envelope := agentprotocol.Envelope{
		Version: agentprotocol.VersionV1,
		Artifacts: []agentprotocol.Artifact{{
			Name: "result", Content: "deterministic fault fixture result", Visibility: "workflow",
		}},
	}
	if err := json.NewEncoder(stdout).Encode(envelope); err != nil {
		fmt.Fprintln(stderr, "FAULT_OUTPUT_FAILED")
		return 1
	}
	return 0
}

func decodeInput(reader io.Reader) (input, error) {
	limited := &io.LimitedReader{R: reader, N: maxInputBytes + 1}
	decoder := json.NewDecoder(bufio.NewReader(limited))
	decoder.DisallowUnknownFields()
	var value input
	if err := decoder.Decode(&value); err != nil {
		return input{}, err
	}
	if limited.N <= 0 {
		return input{}, fmt.Errorf("input exceeds limit")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return input{}, fmt.Errorf("input contains trailing data")
	}
	if !validField(value.TaskID) || !validField(value.WorkflowID) || !validField(value.AttemptID) ||
		!validField(value.IdempotencyKey) || strings.TrimSpace(value.Goal) == "" || value.AttemptNumber <= 0 {
		return input{}, fmt.Errorf("input is incomplete")
	}
	return value, nil
}

func signalReady(directory, attemptID string) error {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || len(directory) > 4096 ||
		strings.ContainsRune(directory, 0) || !validMarkerID(attemptID) {
		return fmt.Errorf("invalid readiness target")
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("readiness directory unavailable")
	}
	finalPath := filepath.Join(directory, attemptID+".ready")
	temporaryPath := finalPath + ".tmp"
	file, err := os.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := io.WriteString(file, "READY\n")
	if writeErr == nil {
		writeErr = file.Sync()
	}
	writeErr = errors.Join(writeErr, file.Close())
	if writeErr != nil {
		_ = os.Remove(temporaryPath)
		return writeErr
	}
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	return nil
}

func validMarkerID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index, char := range value {
		letter := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z'
		digit := char >= '0' && char <= '9'
		if !(letter || digit || index > 0 && (char == '.' || char == '_' || char == '-')) {
			return false
		}
	}
	return true
}

func validField(value string) bool {
	return value != "" && len(value) <= 128 && strings.TrimSpace(value) == value && !strings.ContainsRune(value, 0)
}
