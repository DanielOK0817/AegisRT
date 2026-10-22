// contention executes a finite, identical workload through the production
// Coordinator and durable Runtime, using real child processes and no models.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"aegisrt/internal/controlplane"
	"aegisrt/internal/domain"
	"aegisrt/internal/executor"
	"aegisrt/internal/runtimecore"
	"aegisrt/internal/scheduler"
	"aegisrt/internal/statefile"
	"aegisrt/internal/worker"
)

func main() {
	if len(os.Args) == 5 && os.Args[1] == "--child" {
		if err := child(os.Args[2], os.Args[3], os.Args[4]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type processInput struct {
	TaskID     string `json:"task_id"`
	WorkflowID string `json:"workflow_id"`
}

func artifactContent(taskID string) string { return "aegisrt-contention/v1:" + taskID + "\n" }

func child(durationText, output, class string) error {
	duration, err := time.ParseDuration(durationText)
	if err != nil {
		return err
	}
	var input processInput
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&input); err != nil {
		return err
	}
	if input.TaskID == "" || filepath.Base(input.TaskID) != input.TaskID {
		return errors.New("invalid task id")
	}
	started := time.Now().UTC()
	time.Sleep(duration)
	data := []byte(artifactContent(input.TaskID))
	if err := os.WriteFile(filepath.Join(output, input.TaskID+".txt"), data, 0600); err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	return writeJSON(filepath.Join(output, input.TaskID+".process.json"), map[string]any{
		"task_id": input.TaskID, "workflow_id": input.WorkflowID, "class": class,
		"pid": os.Getpid(), "started_at": started, "finished_at": time.Now().UTC(),
		"requested_duration_ns": duration.Nanoseconds(), "artifact_sha256": hex.EncodeToString(digest[:]),
	})
}

type transition struct {
	TaskID     string                `json:"task_id"`
	WorkflowID string                `json:"workflow_id"`
	Status     domain.TaskStatus     `json:"status"`
	At         time.Time             `json:"at"`
	ObservedAt time.Time             `json:"observed_at"`
	ObservedNS int64                 `json:"observed_ns"`
	Generation uint64                `json:"generation"`
	Allocated  domain.ResourceVector `json:"allocated"`
}

// This observer delegates every commit to the unmodified production store.
// Checkpoint mutation timestamps can precede lock acquisition; observed_ns
// measures the successful-commit observation and is used for admission waits.
type observedStore struct {
	inner *statefile.Store
	trace *os.File
	start time.Time
	last  map[string]domain.TaskStatus
}

func (s *observedStore) Load() ([]byte, error) { return s.inner.Load() }

func (s *observedStore) Save(data []byte) error {
	if err := s.inner.Save(data); err != nil {
		return err
	}
	var checkpoint runtimecore.Checkpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return err
	}
	observed := time.Now()
	now := observed.UTC()
	ready := map[string]time.Time{}
	for _, item := range checkpoint.ReadySince {
		ready[item.TaskID] = item.ReadyAt
	}
	for _, item := range checkpoint.Graph.Tasks {
		if s.last[item.Spec.ID] == item.Status {
			continue
		}
		at := checkpoint.SavedAt
		if item.Status == domain.TaskStatusReady {
			at = ready[item.Spec.ID]
		}
		e := transition{item.Spec.ID, item.Spec.WorkflowID, item.Status, at, now, observed.Sub(s.start).Nanoseconds(), checkpoint.Generation, checkpoint.Scheduler.Allocated}
		if err := json.NewEncoder(s.trace).Encode(e); err != nil {
			return err
		}
		s.last[item.Spec.ID] = item.Status
	}
	return nil
}

type completed struct {
	Result controlplane.TickResult `json:"result"`
	Error  string                  `json:"error,omitempty"`
}

func run() error {
	mode := flag.String("policy", "dag-drcfs", "fifo, dag-drcfs, or dag-drcfs-time")
	output := flag.String("output", "", "new evidence directory")
	count := flag.Int("tasks-per-workflow", 24, "fixed task count in each of two workflows")
	longDuration := flag.Duration("long-duration", 1200*time.Millisecond, "long child hold duration")
	shortDuration := flag.Duration("short-duration", 250*time.Millisecond, "short child hold duration")
	shortArrival := flag.Duration("short-arrival", 100*time.Millisecond, "short workflow arrival after long")
	workerCount := flag.Int("workers", 4, "parallel coordinator loops and reservation slots")
	flag.Parse()
	if *output == "" || *count <= 0 || *workerCount <= 0 || *longDuration <= 0 || *shortDuration <= 0 || *shortArrival < 0 {
		return errors.New("output and positive workload settings required")
	}
	if err := os.Mkdir(*output, 0700); err != nil {
		return err
	}
	absOutput, err := filepath.Abs(*output)
	if err != nil {
		return err
	}
	artifacts := filepath.Join(absOutput, "artifacts")
	if err := os.Mkdir(artifacts, 0700); err != nil {
		return err
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	binaryData, err := os.ReadFile(binary)
	if err != nil {
		return err
	}
	binaryHash := sha256.Sum256(binaryData)
	capacity := domain.ResourceVector{CPUMilli: 250 * int64(*workerCount), MemoryMiB: 64 * int64(*workerCount)}
	policy, err := newPolicy(*mode, capacity)
	if err != nil {
		return err
	}
	trace, err := os.OpenFile(filepath.Join(absOutput, "events.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer trace.Close()
	store, err := statefile.OpenExclusive(filepath.Join(absOutput, "runtime.state"))
	if err != nil {
		return err
	}
	defer store.Close()
	started := time.Now()
	observer := &observedStore{store, trace, started, map[string]domain.TaskStatus{}}
	kernel, _, err := runtimecore.NewPersistent(policy, capacity, "contention-"+*mode, observer, started.UTC())
	if err != nil {
		return err
	}
	agents := []domain.AgentSpec{}
	for index, class := range []string{"long", "short"} {
		duration := []*time.Duration{longDuration, shortDuration}[index]
		agents = append(agents, domain.AgentSpec{ID: class, Name: class, Runtime: "process", Entrypoint: binary,
			Args:      []string{"--child", duration.String(), artifacts, class},
			Resources: domain.ResourceVector{CPUMilli: 250, MemoryMiB: 64}, MaxConcurrency: *workerCount, ExecutionTimeout: 30 * time.Second})
	}
	coordinator, err := controlplane.New(kernel, worker.NewManager(executor.MinimalHostEnvironment(), nil), "contention-worker", agents)
	if err != nil {
		return err
	}
	tasks := map[string][]domain.TaskSpec{}
	for _, class := range []string{"long", "short"} {
		for index := 0; index < *count; index++ {
			id := fmt.Sprintf("task-%03d-%s", index, class)
			tasks[class] = append(tasks[class], domain.TaskSpec{ID: id, WorkflowID: class, AgentID: class,
				Goal: "fixed reserved-capacity workload", Resources: domain.ResourceVector{CPUMilli: 250, MemoryMiB: 64},
				MaxAttempts: 1, Timeout: 30 * time.Second, IdempotencyKey: id})
		}
	}
	config := map[string]any{"schema": "aegisrt.workflow-contention/v1", "policy": *mode, "started_at": started.UTC(),
		"workers": *workerCount, "capacity": capacity, "long_duration_ns": longDuration.Nanoseconds(),
		"short_duration_ns": shortDuration.Nanoseconds(), "short_arrival_ns": shortArrival.Nanoseconds(),
		"tasks_per_workflow": *count, "tasks": tasks, "binary_sha256": hex.EncodeToString(binaryHash[:]),
		"go_version": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH,
		"synthetic_workload": true, "kernel_resource_limits": false, "model_requests": 0,
		"observer": "committed checkpoint state transitions; executor process timestamps retained"}
	if err := writeJSON(filepath.Join(absOutput, "config.json"), config); err != nil {
		return err
	}
	initial := tasks["long"]
	if *shortArrival == 0 {
		initial = append(append([]domain.TaskSpec{}, initial...), tasks["short"]...)
	}
	if err := coordinator.ApplyPatch(initial, time.Now().UTC()); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	results := make(chan completed, *workerCount)
	var workers sync.WaitGroup
	for index := 0; index < *workerCount; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for ctx.Err() == nil {
				result, err := coordinator.Tick(ctx, time.Now().UTC())
				if errors.Is(err, scheduler.ErrNoCandidate) {
					time.Sleep(time.Millisecond)
					continue
				}
				item := completed{Result: result}
				if err != nil {
					item.Error = err.Error()
				}
				select {
				case results <- item:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	defer func() { cancel(); workers.Wait() }()
	arrival := time.NewTimer(time.Until(started.Add(*shortArrival)))
	defer arrival.Stop()
	var arrivalChannel <-chan time.Time = arrival.C
	if *shortArrival == 0 {
		arrival.Stop()
		arrivalChannel = nil
	}
	resultFile, err := os.OpenFile(filepath.Join(absOutput, "results.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer resultFile.Close()
	finished := 0
	for finished < 2**count {
		select {
		case <-arrivalChannel:
			if err := coordinator.ApplyPatch(tasks["short"], time.Now().UTC()); err != nil {
				return err
			}
		case result := <-results:
			if err := json.NewEncoder(resultFile).Encode(result); err != nil {
				return err
			}
			if result.Error != "" {
				return errors.New(result.Error)
			}
			if result.Result.Execution.Attempt.Status != domain.AttemptStatusSucceeded {
				return errors.New("non-successful execution")
			}
			finished++
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	cancel()
	workers.Wait()
	stats := kernel.Stats()
	if !stats.Allocated.IsZero() || stats.PendingCompletionCount != 0 {
		return errors.New("resource or completion leak")
	}
	for _, task := range kernel.Tasks() {
		if task.Status != domain.TaskStatusSucceeded || kernel.AttemptNumber(task.Spec.ID) != 1 {
			return fmt.Errorf("task %s incomplete or repeated", task.Spec.ID)
		}
	}
	if err := trace.Sync(); err != nil {
		return err
	}
	if err := resultFile.Sync(); err != nil {
		return err
	}
	ended := time.Now()
	monotonicElapsed := ended.Sub(started).Nanoseconds()
	wallElapsed := ended.UnixNano() - started.UnixNano()
	return writeJSON(filepath.Join(absOutput, "summary.json"), map[string]any{
		"status": "complete", "policy": *mode, "finished_at": ended.UTC(),
		"elapsed_ns": monotonicElapsed, "elapsed_wall_ns": wallElapsed, "clock_skew_ns": wallElapsed - monotonicElapsed,
		"completed": finished, "final_runtime": stats,
		"synthetic_workload": true, "kernel_resource_limits": false, "model_requests": 0})
}

func newPolicy(mode string, capacity domain.ResourceVector) (scheduler.Policy, error) {
	switch mode {
	case "fifo":
		return scheduler.FIFO{}, nil
	case "dag-drcfs":
		return scheduler.NewDAGDRCFS(capacity, time.Second)
	case "dag-drcfs-time":
		return scheduler.NewDurationDAGDRCFS(capacity, time.Second)
	default:
		return nil, fmt.Errorf("unknown policy %s", mode)
	}
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}
