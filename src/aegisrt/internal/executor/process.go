package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrInvalidRequest = errors.New("invalid worker request")
	ErrStart          = errors.New("worker process failed to start")
	ErrPrepareHook    = errors.New("worker process preparation failed")
	ErrStartHook      = errors.New("worker process start hook failed")
	ErrCleanup        = errors.New("worker process cleanup failed")
	ErrExit           = errors.New("worker process exited unsuccessfully")
	ErrTimeout        = errors.New("worker process timed out")
	ErrCanceled       = errors.New("worker process was canceled")
)

const defaultOutputLimit int64 = 1 << 20

type Request struct {
	AttemptID      string            `json:"attempt_id"`
	Command        string            `json:"command"`
	Args           []string          `json:"args,omitempty"`
	WorkingDir     string            `json:"working_dir,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Stdin          []byte            `json:"-"`
	Timeout        time.Duration     `json:"timeout"`
	MaxOutputBytes int64             `json:"max_output_bytes"`
	OnStarted      StartHook         `json:"-"`
}

func (r Request) Validate() error {
	if strings.TrimSpace(r.AttemptID) == "" {
		return fmt.Errorf("%w: attempt_id is required", ErrInvalidRequest)
	}
	if strings.TrimSpace(r.Command) == "" {
		return fmt.Errorf("%w: command is required", ErrInvalidRequest)
	}
	if strings.ContainsRune(r.Command, 0) || strings.ContainsRune(r.WorkingDir, 0) {
		return fmt.Errorf("%w: command and working_dir must not contain NUL", ErrInvalidRequest)
	}
	for index, argument := range r.Args {
		if strings.ContainsRune(argument, 0) {
			return fmt.Errorf("%w: args[%d] contains NUL", ErrInvalidRequest, index)
		}
	}
	for name, value := range r.Env {
		if name == "" || strings.ContainsAny(name, "=\x00") || strings.ContainsRune(value, 0) {
			return fmt.Errorf("%w: invalid environment entry", ErrInvalidRequest)
		}
	}
	if r.Timeout < 0 || r.MaxOutputBytes < 0 {
		return fmt.Errorf("%w: timeout and max_output_bytes must be non-negative", ErrInvalidRequest)
	}
	return nil
}

type Result struct {
	AttemptID            string    `json:"attempt_id"`
	ExitCode             int       `json:"exit_code"`
	Stdout               []byte    `json:"stdout"`
	Stderr               []byte    `json:"stderr"`
	StdoutTruncated      bool      `json:"stdout_truncated"`
	StderrTruncated      bool      `json:"stderr_truncated"`
	PreparationStartedAt time.Time `json:"preparation_started_at"`
	StartedAt            time.Time `json:"started_at"`
	FinishedAt           time.Time `json:"finished_at"`
	TimedOut             bool      `json:"timed_out"`
	Canceled             bool      `json:"canceled"`
	ProcessStarted       bool      `json:"process_started"`
	WaitCompleted        bool      `json:"wait_completed"`
	ProcessStartedAt     time.Time `json:"process_started_at"`
	WaitCompletedAt      time.Time `json:"wait_completed_at"`
	DeadlineAt           time.Time `json:"-"`
}

type ProcessRunner struct {
	baseEnv []string
	now     func() time.Time
	prepare []PrepareHook
	hooks   []StartHook
}

type StartHook func(context.Context, Request, int) error

type Cleanup func() error

type PrepareHook func(context.Context, Request, *exec.Cmd) (Cleanup, error)

func NewProcessRunner(baseEnv []string, hooks ...StartHook) *ProcessRunner {
	return &ProcessRunner{
		baseEnv: append([]string(nil), baseEnv...),
		now:     time.Now,
		hooks:   append([]StartHook(nil), hooks...),
	}
}

// NewProcessRunnerWithPrepare installs hooks that run before process start.
// Each successful hook returns cleanup work that runs in reverse order after
// every start, exit, timeout, or cancellation path.
func NewProcessRunnerWithPrepare(baseEnv []string, hooks ...PrepareHook) *ProcessRunner {
	return &ProcessRunner{
		baseEnv: append([]string(nil), baseEnv...),
		now:     time.Now,
		prepare: append([]PrepareHook(nil), hooks...),
	}
}

// MinimalHostEnvironment returns only process-launch essentials. It does not
// inherit arbitrary provider credentials from the Runtime process.
func MinimalHostEnvironment() []string {
	names := []string{"PATH", "PATHEXT", "SYSTEMROOT", "WINDIR", "TEMP", "TMP"}
	result := make([]string, 0, len(names))
	for _, name := range names {
		if value, exists := os.LookupEnv(name); exists {
			result = append(result, name+"="+value)
		}
	}
	return result
}

func (r *ProcessRunner) Run(ctx context.Context, request Request) (result Result, runErr error) {
	result = Result{AttemptID: request.AttemptID, ExitCode: -1}
	if err := request.Validate(); err != nil {
		return result, err
	}
	limit := request.MaxOutputBytes
	if limit == 0 {
		limit = defaultOutputLimit
	}
	runCtx := ctx
	cancel := func() {}
	if request.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, request.Timeout)
	}
	defer cancel()
	if deadline, exists := runCtx.Deadline(); exists {
		result.DeadlineAt = deadline
	}

	stdout := newBoundedBuffer(limit)
	stderr := newBoundedBuffer(limit)
	command := exec.CommandContext(runCtx, request.Command, request.Args...)
	command.Dir = request.WorkingDir
	command.Env = mergeEnvironment(r.baseEnv, request.Env)
	command.Stdin = bytes.NewReader(request.Stdin)
	command.Stdout = stdout
	command.Stderr = stderr
	configureProcessTree(command)

	result.PreparationStartedAt = r.now().UTC()
	cleanups, err := prepareExecution(runCtx, request, command, r.prepare)
	if err != nil {
		result.FinishedAt = r.now().UTC()
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			result.TimedOut = true
			return result, errors.Join(ErrTimeout, err)
		}
		if errors.Is(err, context.Canceled) || errors.Is(runCtx.Err(), context.Canceled) {
			result.Canceled = true
			return result, errors.Join(ErrCanceled, err)
		}
		return result, err
	}
	defer func() {
		if err := runCleanups(cleanups); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("%w: %w", ErrCleanup, err))
		}
	}()
	err = command.Start()
	if err != nil {
		result.FinishedAt = r.now().UTC()
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			result.TimedOut = true
			return result, fmt.Errorf("%w: %v", ErrTimeout, err)
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			result.Canceled = true
			return result, fmt.Errorf("%w: %v", ErrCanceled, err)
		}
		return result, fmt.Errorf("%w: %v", ErrStart, err)
	}
	result.ProcessStarted = true
	result.ProcessStartedAt = r.now()
	result.StartedAt = result.ProcessStartedAt.UTC()
	hooks := append([]StartHook(nil), r.hooks...)
	hooks = append(hooks, request.OnStarted)
	for _, hook := range hooks {
		if hook == nil {
			continue
		}
		if hookErr := hook(runCtx, request, command.Process.Pid); hookErr != nil {
			terminateProcess(command)
			_ = command.Wait()
			result.WaitCompleted = true
			result.WaitCompletedAt = r.now()
			result.FinishedAt = r.now().UTC()
			result.Stdout = stdout.Bytes()
			result.Stderr = stderr.Bytes()
			if errors.Is(hookErr, context.DeadlineExceeded) || errors.Is(runCtx.Err(), context.DeadlineExceeded) {
				result.TimedOut = true
				return result, errors.Join(ErrStartHook, ErrTimeout, hookErr)
			}
			if errors.Is(hookErr, context.Canceled) || errors.Is(runCtx.Err(), context.Canceled) {
				result.Canceled = true
				return result, errors.Join(ErrStartHook, ErrCanceled, hookErr)
			}
			return result, fmt.Errorf("%w: %w", ErrStartHook, hookErr)
		}
	}
	err = waitProcess(command)
	result.WaitCompleted = true
	result.WaitCompletedAt = r.now()
	terminateProcess(command)
	result.FinishedAt = r.now().UTC()
	result.Stdout = stdout.Bytes()
	result.Stderr = stderr.Bytes()
	result.StdoutTruncated = stdout.Truncated()
	result.StderrTruncated = stderr.Truncated()
	if command.ProcessState != nil {
		result.ExitCode = command.ProcessState.ExitCode()
	}
	if err == nil {
		return result, nil
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		result.TimedOut = true
		return result, fmt.Errorf("%w: %v", ErrTimeout, err)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		result.Canceled = true
		return result, fmt.Errorf("%w: %v", ErrCanceled, err)
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return result, fmt.Errorf("%w: exit code %d", ErrExit, exitError.ExitCode())
	}
	return result, fmt.Errorf("%w: %v", ErrExit, err)
}

func mergeEnvironment(base []string, overrides map[string]string) []string {
	values := make(map[string]string, len(base)+len(overrides))
	for _, entry := range base {
		name, value, found := strings.Cut(entry, "=")
		if found && name != "" {
			values[name] = value
		}
	}
	for name, value := range overrides {
		values[name] = value
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]string, 0, len(names))
	for _, name := range names {
		result = append(result, name+"="+values[name])
	}
	return result
}

type boundedBuffer struct {
	mu        sync.Mutex
	data      []byte
	limit     int64
	truncated bool
}

func newBoundedBuffer(limit int64) *boundedBuffer {
	return &boundedBuffer{limit: limit}
}

func (b *boundedBuffer) Write(input []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - int64(len(b.data))
	if remaining <= 0 {
		b.truncated = true
		return len(input), nil
	}
	writeLength := int64(len(input))
	if writeLength > remaining {
		writeLength = remaining
		b.truncated = true
	}
	b.data = append(b.data, input[:writeLength]...)
	return len(input), nil
}

func (b *boundedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.data...)
}

func (b *boundedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

var _ io.Writer = (*boundedBuffer)(nil)
