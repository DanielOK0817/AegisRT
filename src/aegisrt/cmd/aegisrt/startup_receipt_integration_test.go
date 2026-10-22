//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

const startupReceiptHelperEnvironment = "AEGISRT_STARTUP_RECEIPT_HELPER"

func TestRunEmitsStartupReceiptThroughExplicitInheritedFD(t *testing.T) {
	process := startStartupReceiptDaemon(t, "127.0.0.1:0", true)
	payload := readStartupReceiptPipe(t, process.receiptReader, process)
	var receipt startupReceipt
	if err := json.Unmarshal(bytes.TrimSuffix(payload, []byte{'\n'}), &receipt); err != nil {
		process.stop(t)
		t.Fatal(err)
	}
	if err := receipt.validate(); err != nil {
		process.stop(t)
		t.Fatal(err)
	}
	if err := waitStartupReceiptReady(receipt.ListenAddress, process); err != nil {
		process.stop(t)
		t.Fatal(err)
	}
	process.stop(t)
}

func TestRunDoesNotWriteInheritedFDByDefault(t *testing.T) {
	address := freeLoopbackAddress(t)
	process := startStartupReceiptDaemon(t, address, false)
	if err := waitStartupReceiptReady(address, process); err != nil {
		process.stop(t)
		t.Fatal(err)
	}
	process.stop(t)
	payload, err := io.ReadAll(io.LimitReader(process.receiptReader, maximumStartupReceiptBytes+1))
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) != 0 {
		t.Fatalf("daemon wrote startup receipt without explicit flag: %q", payload)
	}
}

func TestStartupReceiptDaemonHelperProcess(t *testing.T) {
	if os.Getenv(startupReceiptHelperEnvironment) != "1" {
		return
	}
	arguments := argumentsAfterDoubleDash(os.Args)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		cancel()
	}()
	if err := run(ctx, arguments, io.Discard, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "STARTUP_RECEIPT_HELPER_FAILED")
		os.Exit(91)
	}
	os.Exit(0)
}

type startupReceiptProcess struct {
	command       *exec.Cmd
	stdin         io.WriteCloser
	receiptReader *os.File
	done          chan struct{}
	stderr        *bytes.Buffer
	waitMu        sync.Mutex
	waitErr       error
}

func startStartupReceiptDaemon(t *testing.T, address string, receiptEnabled bool) *startupReceiptProcess {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	arguments := daemonArgs(t, "success", address)
	if receiptEnabled {
		arguments = append(arguments, "-startup-receipt-fd", "3")
	}
	commandArguments := append([]string{"-test.run=^TestStartupReceiptDaemonHelperProcess$", "--"}, arguments...)
	command := exec.Command(os.Args[0], commandArguments...)
	command.Env = append(os.Environ(), startupReceiptHelperEnvironment+"=1")
	command.ExtraFiles = []*os.File{writer}
	var stderr bytes.Buffer
	command.Stdout = io.Discard
	command.Stderr = &stderr
	stdin, err := command.StdinPipe()
	if err != nil {
		_ = reader.Close()
		_ = writer.Close()
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = reader.Close()
		_ = writer.Close()
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = stdin.Close()
		_ = reader.Close()
		t.Fatal(err)
	}
	process := &startupReceiptProcess{
		command: command, stdin: stdin, receiptReader: reader,
		done: make(chan struct{}), stderr: &stderr,
	}
	go func() {
		err := command.Wait()
		process.waitMu.Lock()
		process.waitErr = err
		process.waitMu.Unlock()
		close(process.done)
	}()
	t.Cleanup(func() {
		_ = process.stdin.Close()
		_ = process.receiptReader.Close()
		select {
		case <-process.done:
		default:
			_ = command.Process.Kill()
			<-process.done
		}
	})
	return process
}

func (process *startupReceiptProcess) stop(t *testing.T) {
	t.Helper()
	if err := process.stdin.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-process.done:
		err := process.waitError()
		if err != nil {
			t.Fatalf("startup receipt daemon failed: %v; stderr=%s", err, process.stderr.String())
		}
	case <-time.After(5 * time.Second):
		_ = process.command.Process.Kill()
		<-process.done
		t.Fatalf("startup receipt daemon did not stop; stderr=%s", process.stderr.String())
	}
}

func (process *startupReceiptProcess) waitError() error {
	process.waitMu.Lock()
	defer process.waitMu.Unlock()
	return process.waitErr
}

func readStartupReceiptPipe(t *testing.T, reader *os.File, process *startupReceiptProcess) []byte {
	t.Helper()
	type result struct {
		payload []byte
		err     error
	}
	results := make(chan result, 1)
	go func() {
		payload, err := io.ReadAll(io.LimitReader(reader, maximumStartupReceiptBytes+1))
		results <- result{payload: payload, err: err}
	}()
	select {
	case result := <-results:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if len(result.payload) == 0 || len(result.payload) > maximumStartupReceiptBytes ||
			bytes.Count(result.payload, []byte{'\n'}) != 1 || result.payload[len(result.payload)-1] != '\n' {
			t.Fatalf("invalid startup receipt payload: %q", result.payload)
		}
		return result.payload
	case <-process.done:
		t.Fatalf("daemon exited before startup receipt: %v", process.waitError())
	case <-time.After(5 * time.Second):
		t.Fatal("startup receipt deadline exceeded")
	}
	return nil
}

func waitStartupReceiptReady(address string, process *startupReceiptProcess) error {
	client := &http.Client{
		Timeout:   250 * time.Millisecond,
		Transport: &http.Transport{Proxy: nil},
	}
	defer client.CloseIdleConnections()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, err := http.NewRequest(http.MethodGet, "http://"+address+"/readyz", nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-process.done:
			return fmt.Errorf("daemon stopped before readiness: %v", process.waitError())
		case <-deadline.C:
			return fmt.Errorf("daemon readiness deadline exceeded")
		case <-ticker.C:
		}
	}
}

func argumentsAfterDoubleDash(arguments []string) []string {
	for index, argument := range arguments {
		if strings.TrimSpace(argument) == "--" {
			return append([]string(nil), arguments[index+1:]...)
		}
	}
	return nil
}
