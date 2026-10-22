//go:build linux

package benchmark

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	faultKernelStopGrace = 2 * time.Second
	faultKernelStopLimit = 5 * time.Second
)

type faultKernelReceiptResult struct {
	receipt faultKernelDaemonReceipt
	err     error
}

func startFaultKernelDaemon(ctx context.Context, config faultKernelDaemonConfig) (*faultKernelDaemon, faultKernelDaemonReceipt, error) {
	if ctx == nil || config.Driver == nil || config.AttemptID == "" {
		return nil, faultKernelDaemonReceipt{}, faultKernelDaemonFailure("start-configuration")
	}
	if err := ctx.Err(); err != nil {
		return nil, faultKernelDaemonReceipt{}, errors.Join(
			faultKernelDaemonFailure("start-configuration"), err,
		)
	}
	timeout := config.StartupTimeout
	if timeout == 0 {
		timeout = faultKernelDefaultStartupTimeout
	}
	if timeout < time.Millisecond || timeout > faultKernelMaximumStartupTimeout || hasStartupReceiptArgument(config.Arguments) {
		return nil, faultKernelDaemonReceipt{}, faultKernelDaemonFailure("start-configuration")
	}
	executable, err := locateFaultKernelDaemon(config.BenchmarkExecutable)
	if err != nil {
		return nil, faultKernelDaemonReceipt{}, err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, faultKernelDaemonReceipt{}, faultKernelDaemonFailure("startup-pipe")
	}
	arguments := append(append([]string(nil), config.Arguments...), "-startup-receipt-fd", "3")
	command := exec.Command(executable, arguments...)
	command.Stdin, command.Stdout, command.Stderr = nil, io.Discard, io.Discard
	command.ExtraFiles = []*os.File{writer}
	group, err := placeFaultKernelDaemon(config, command)
	if err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, faultKernelDaemonReceipt{}, faultKernelDaemonFailure("cgroup-placement")
	}
	if err := command.Start(); err != nil {
		_ = group.Close()
		_ = reader.Close()
		_ = writer.Close()
		return nil, faultKernelDaemonReceipt{}, faultKernelDaemonFailure("process-start")
	}
	daemon := recordStartedFaultKernelDaemon(command)
	closeErr := group.Close()
	closeErr = firstFaultKernelError(closeErr, writer.Close())
	if closeErr != nil {
		_ = reader.Close()
		abortFaultKernelDaemon(daemon)
		return nil, faultKernelDaemonReceipt{}, faultKernelDaemonFailure("descriptor-close")
	}
	receipt, err := awaitFaultKernelDaemonReceipt(ctx, daemon, reader, timeout)
	_ = reader.Close()
	if err != nil {
		abortFaultKernelDaemon(daemon)
		return nil, faultKernelDaemonReceipt{}, err
	}
	return daemon, receipt, nil
}

func placeFaultKernelDaemon(config faultKernelDaemonConfig, command *exec.Cmd) (io.Closer, error) {
	if config.UseAttemptManager {
		return config.Driver.PlaceCommandInAttemptManager(config.AttemptID, command)
	}
	return config.Driver.PlaceCommandInExistingGroup(config.AttemptID, command)
}

func locateFaultKernelDaemon(benchmarkExecutable string) (string, error) {
	if benchmarkExecutable == "" {
		var err error
		benchmarkExecutable, err = os.Executable()
		if err != nil {
			return "", faultKernelDaemonFailure("executable-location")
		}
	}
	candidate := filepath.Join(filepath.Dir(benchmarkExecutable), "aegisrt")
	info, err := os.Lstat(candidate)
	if err != nil || validateFaultKernelDaemonExecutable(info) != nil {
		return "", faultKernelDaemonFailure("executable-validation")
	}
	return candidate, nil
}

func validateFaultKernelDaemonExecutable(info os.FileInfo) error {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 {
		return faultKernelDaemonFailure("executable-validation")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return faultKernelDaemonFailure("executable-validation")
	}
	return nil
}

func hasStartupReceiptArgument(arguments []string) bool {
	for _, argument := range arguments {
		if argument == "-startup-receipt-fd" || strings.HasPrefix(argument, "-startup-receipt-fd=") {
			return true
		}
	}
	return false
}

func recordStartedFaultKernelDaemon(command *exec.Cmd) *faultKernelDaemon {
	daemon := &faultKernelDaemon{process: command.Process, done: make(chan struct{})}
	go func() {
		err := command.Wait()
		daemon.result = faultKernelDaemonWaitResult{state: command.ProcessState, err: err}
		daemon.once.Do(func() { close(daemon.done) })
	}()
	return daemon
}

func awaitFaultKernelDaemonReceipt(ctx context.Context, daemon *faultKernelDaemon, reader io.Reader, timeout time.Duration) (faultKernelDaemonReceipt, error) {
	results := make(chan faultKernelReceiptResult, 1)
	go func() {
		payload, err := io.ReadAll(io.LimitReader(reader, faultKernelMaximumStartupReceiptBytes+1))
		if err != nil {
			results <- faultKernelReceiptResult{err: faultKernelDaemonFailure("startup-receipt-read")}
			return
		}
		receipt, err := parseFaultKernelDaemonReceipt(payload)
		results <- faultKernelReceiptResult{receipt: receipt, err: err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-results:
		return result.receipt, result.err
	case <-daemon.done:
		return faultKernelDaemonReceipt{}, faultKernelDaemonFailure("process-exited-before-receipt")
	case <-ctx.Done():
		return faultKernelDaemonReceipt{}, errors.Join(
			faultKernelDaemonFailure("startup-canceled"), ctx.Err(),
		)
	case <-timer.C:
		return faultKernelDaemonReceipt{}, faultKernelDaemonFailure("startup-timeout")
	}
}

func (daemon *faultKernelDaemon) KillAndWait(ctx context.Context) error {
	if daemon == nil || daemon.process == nil || daemon.done == nil || ctx == nil {
		return faultKernelDaemonFailure("process-control")
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(faultKernelDaemonFailure("process-control"), err)
	}
	if !daemon.isDone() {
		if err := daemon.process.Signal(syscall.SIGKILL); err != nil && !daemon.isDone() {
			failure := faultKernelDaemonFailure("sigkill-send")
			if ctx.Err() != nil {
				return errors.Join(failure, ctx.Err())
			}
			return failure
		}
	}
	if !daemon.waitBounded(ctx, faultKernelStopLimit) {
		err := faultKernelDaemonFailure("sigkill-confirmation")
		if ctx.Err() != nil {
			return errors.Join(err, ctx.Err())
		}
		return err
	}
	if !daemon.wasKilledBySIGKILL() {
		failure := faultKernelDaemonFailure("sigkill-confirmation")
		if ctx.Err() != nil {
			return errors.Join(failure, ctx.Err())
		}
		return failure
	}
	return nil
}

func (daemon *faultKernelDaemon) Stop(ctx context.Context) error {
	if daemon == nil || daemon.process == nil || daemon.done == nil || ctx == nil {
		return faultKernelDaemonFailure("process-control")
	}
	overall, cancel := boundedFaultKernelContext(ctx, faultKernelStopLimit)
	defer cancel()
	if daemon.isDone() {
		return nil
	}
	if err := daemon.process.Signal(syscall.SIGTERM); err != nil && !daemon.isDone() {
		return faultKernelDaemonFailure("sigterm-send")
	}
	grace, cancelGrace := boundedFaultKernelContext(overall, faultKernelStopGrace)
	stopped := daemon.waitBounded(grace, faultKernelStopGrace)
	cancelGrace()
	if stopped {
		return nil
	}
	return daemon.KillAndWait(overall)
}

func (daemon *faultKernelDaemon) waitBounded(ctx context.Context, maximum time.Duration) bool {
	timer := time.NewTimer(maximum)
	defer timer.Stop()
	select {
	case <-daemon.done:
		return true
	case <-ctx.Done():
		return false
	case <-timer.C:
		return false
	}
}

func (daemon *faultKernelDaemon) isDone() bool {
	select {
	case <-daemon.done:
		return true
	default:
		return false
	}
}

func (daemon *faultKernelDaemon) wasKilledBySIGKILL() bool {
	if daemon == nil || !daemon.isDone() || daemon.result.state == nil {
		return false
	}
	status, ok := daemon.result.state.Sys().(syscall.WaitStatus)
	return ok && status.Signaled() && status.Signal() == syscall.SIGKILL
}

func boundedFaultKernelContext(parent context.Context, maximum time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, maximum)
}

func abortFaultKernelDaemon(daemon *faultKernelDaemon) {
	ctx, cancel := context.WithTimeout(context.Background(), faultKernelStopLimit)
	defer cancel()
	_ = daemon.KillAndWait(ctx)
}

func firstFaultKernelError(first, second error) error {
	if first != nil {
		return first
	}
	return second
}
