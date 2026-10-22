package benchmark

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"aegisrt/internal/agentprotocol"
	"aegisrt/internal/controlplane"
	"aegisrt/internal/domain"
	"aegisrt/internal/executor"
	"aegisrt/internal/runtimecore"
	"aegisrt/internal/scheduler"
	"aegisrt/internal/worker"
)

var faultCapacity = domain.ResourceVector{
	CPUMilli: 1000, MemoryMiB: 512, LLMSlots: 1, TokenBudget: 4096,
}

type faultArtifactRecorder struct {
	mu      sync.Mutex
	records map[string]faultPublicationRecord
}

type faultPublicationRecord struct {
	trace       FaultPublicationTrace
	fingerprint string
}

func newFaultArtifactRecorder() *faultArtifactRecorder {
	return &faultArtifactRecorder{records: make(map[string]faultPublicationRecord)}
}

func (r *faultArtifactRecorder) PublishAgentOutput(ctx context.Context, taskID string, attempt int, artifacts []agentprotocol.Artifact) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	type candidate struct {
		key         string
		trace       FaultPublicationTrace
		fingerprint string
	}
	candidates := make([]candidate, 0, len(artifacts))
	batch := make(map[string]string, len(artifacts))
	for _, artifact := range artifacts {
		sum := sha256.Sum256([]byte(artifact.Content))
		key := taskID + "\x00" + artifact.Name
		fingerprint, err := faultArtifactFingerprint(artifact)
		if err != nil {
			return err
		}
		if previous, exists := batch[key]; exists && previous != fingerprint {
			return fmt.Errorf("artifact idempotency conflict")
		}
		if _, exists := batch[key]; exists {
			return fmt.Errorf("duplicate artifact key in one publication")
		}
		batch[key] = fingerprint
		candidates = append(candidates, candidate{
			key: key, fingerprint: fingerprint,
			trace: FaultPublicationTrace{
				TaskID: taskID, ArtifactKey: artifact.Name, ContentSHA256: hex.EncodeToString(sum[:]),
			},
		})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, item := range candidates {
		if record, exists := r.records[item.key]; exists && record.fingerprint != item.fingerprint {
			return fmt.Errorf("artifact idempotency conflict")
		}
	}
	for _, item := range candidates {
		record, exists := r.records[item.key]
		if !exists {
			record = faultPublicationRecord{trace: item.trace, fingerprint: item.fingerprint}
		}
		record.trace.ObservedCount++
		r.records[item.key] = record
	}
	return nil
}

func faultArtifactFingerprint(artifact agentprotocol.Artifact) (string, error) {
	type semanticArtifact struct {
		Name       string
		Content    string
		Visibility string
		Recipients []string
		Pin        bool
		Cold       bool
	}
	var recipients []string
	if artifact.Recipients != nil {
		recipients = append([]string{}, artifact.Recipients...)
	}
	payload, err := json.Marshal(semanticArtifact{
		Name: artifact.Name, Content: artifact.Content, Visibility: artifact.Visibility,
		Recipients: recipients, Pin: artifact.Pin, Cold: artifact.Cold,
	})
	if err != nil {
		return "", fmt.Errorf("fingerprint artifact: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func (r *faultArtifactRecorder) snapshot() ([]FaultPublicationTrace, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]FaultPublicationTrace, 0, len(r.records))
	duplicates := 0
	for _, record := range r.records {
		result = append(result, record.trace)
		if record.trace.ObservedCount > 1 {
			duplicates += record.trace.ObservedCount - 1
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].TaskID != result[j].TaskID {
			return result[i].TaskID < result[j].TaskID
		}
		return result[i].ArtifactKey < result[j].ArtifactKey
	})
	return result, duplicates
}

func newFaultCoordinator(
	runtime *runtimecore.Runtime,
	workload faultCaseWorkload,
	command FaultAgentCommand,
	targetMode string,
	targetHold time.Duration,
	targetTimeout time.Duration,
	readyDir string,
	recorder controlplane.OutputPublisher,
) (*controlplane.Coordinator, error) {
	agents := []domain.AgentSpec{
		faultAgent(workload.Target.AgentID, command, targetMode, targetHold, targetTimeout, readyDir),
		faultAgent(workload.Unaffected.AgentID, command, "success", 0, time.Duration(faultNormalTimeoutMS)*time.Millisecond, readyDir),
	}
	manager := worker.NewManager(executor.MinimalHostEnvironment(), nil)
	return controlplane.NewWithOptions(runtime, manager, "fault-worker", agents, controlplane.Options{OutputPublisher: recorder})
}

func faultAgent(
	agentID string,
	command FaultAgentCommand,
	mode string,
	hold, timeout time.Duration,
	readyDir string,
) domain.AgentSpec {
	args := append([]string(nil), command.ArgsPrefix...)
	args = append(args, "--mode="+mode, "--hold="+hold.String())
	if readyDir != "" {
		args = append(args, "--ready-dir="+readyDir)
	}
	return domain.AgentSpec{
		ID: agentID, Name: "Fault Fixture Agent", Runtime: "process", Entrypoint: command.Entrypoint,
		Args: args, Capabilities: []string{"fault.fixture"},
		Resources:      domain.ResourceVector{CPUMilli: 100, MemoryMiB: 64, TokenBudget: 512},
		MaxConcurrency: 1, ExecutionTimeout: timeout,
	}
}

func faultTasks(workload faultCaseWorkload) []domain.TaskSpec {
	return []domain.TaskSpec{
		faultTask(workload, workload.Target),
		faultTask(workload, workload.Unaffected),
	}
}

func faultTask(workload faultCaseWorkload, descriptor faultTaskDescriptor) domain.TaskSpec {
	return domain.TaskSpec{
		ID: descriptor.TaskID, WorkflowID: workload.WorkflowID, AgentID: descriptor.AgentID,
		Goal: "execute deterministic fault smoke fixture", Priority: descriptor.Priority,
		Resources:   domain.ResourceVector{CPUMilli: 100, MemoryMiB: 64, TokenBudget: 256},
		MaxAttempts: descriptor.MaxAttempts, Timeout: time.Duration(faultNormalTimeoutMS) * time.Millisecond,
		IdempotencyKey: descriptor.IdempotencyKey,
	}
}

func newFaultRuntime() (*runtimecore.Runtime, error) {
	policy, err := scheduler.NewDAGDRCFS(faultCapacity, time.Millisecond)
	if err != nil {
		return nil, err
	}
	return runtimecore.New(policy, faultCapacity)
}

func faultTaskTrace(
	runtime *runtimecore.Runtime,
	descriptor faultTaskDescriptor,
	results []controlplane.TickResult,
) (FaultTaskTrace, error) {
	snapshot, exists := runtime.Task(descriptor.TaskID)
	if !exists {
		return FaultTaskTrace{}, fmt.Errorf("fault task %q disappeared", descriptor.TaskID)
	}
	trace := FaultTaskTrace{
		TaskID: descriptor.TaskID, MaxAttempts: descriptor.MaxAttempts,
		FinalStatus: snapshot.Status, AttemptCount: runtime.AttemptNumber(descriptor.TaskID),
	}
	for _, result := range results {
		if result.Execution.Attempt.TaskID != descriptor.TaskID {
			continue
		}
		trace.Attempts = append(trace.Attempts, faultAttemptTrace(result))
	}
	sort.Slice(trace.Attempts, func(i, j int) bool { return trace.Attempts[i].Number < trace.Attempts[j].Number })
	if trace.AttemptCount != len(trace.Attempts) {
		return FaultTaskTrace{}, fmt.Errorf("fault task %q attempt trace is incomplete", descriptor.TaskID)
	}
	return trace, nil
}

func faultAttemptTrace(result controlplane.TickResult) FaultAttemptTrace {
	attempt := result.Execution.Attempt
	process := result.Execution.Process
	var exitCode *int
	if process.ProcessStarted {
		value := process.ExitCode
		exitCode = &value
	}
	elapsed := process.WaitCompletedAt.Sub(process.ProcessStartedAt)
	if !process.ProcessStarted || !process.WaitCompleted || process.ProcessStartedAt.IsZero() ||
		process.WaitCompletedAt.IsZero() || elapsed < 0 {
		elapsed = 0
	}
	return FaultAttemptTrace{
		Number: attempt.Number, Status: attempt.Status, ExitCode: exitCode,
		TimedOut: process.TimedOut, Canceled: process.Canceled,
		ProcessWaitCompleted: process.ProcessStarted && process.WaitCompleted,
		RunElapsedNS:         elapsed.Nanoseconds(), ReasonCode: faultAttemptReason(result),
	}
}

func faultAttemptReason(result controlplane.TickResult) string {
	switch {
	case result.Execution.Attempt.Status == domain.AttemptStatusLost:
		return "RUNTIME_INTERRUPTED"
	case result.Execution.Process.TimedOut:
		return "AGENT_TIMEOUT"
	case result.Execution.Attempt.Status == domain.AttemptStatusFailed:
		return "AGENT_EXIT"
	case result.Execution.Attempt.Status == domain.AttemptStatusCanceled:
		return "AGENT_CANCELED"
	default:
		return "NONE"
	}
}

func intPointer(value int) *int       { return &value }
func int64Pointer(value int64) *int64 { return &value }
