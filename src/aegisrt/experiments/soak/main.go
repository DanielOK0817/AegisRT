// soak exercises dynamic arrival and real subprocess execution through the
// production Coordinator. Synthetic sleepers measure reserved capacity time,
// not CPU utilization or business application quality.
package main

import (
	"context"
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
	if len(os.Args) == 3 && os.Args[1] == "--worker-duration" {
		d, err := time.ParseDuration(os.Args[2])
		if err != nil {
			os.Exit(2)
		}
		_, _ = io.Copy(io.Discard, os.Stdin)
		time.Sleep(d)
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type event struct {
	Task           string    `json:"task"`
	Workflow       string    `json:"workflow"`
	Started        time.Time `json:"started"`
	Finished       time.Time `json:"finished"`
	ProcessSeconds float64   `json:"process_seconds"`
	Error          string    `json:"error,omitempty"`
}

func run() error {
	duration := flag.Duration("duration", 10*time.Minute, "steady arrival interval")
	mode := flag.String("policy", "dag-drcfs-time", "dag-drcfs or dag-drcfs-time")
	out := flag.String("output", "", "new evidence directory")
	flag.Parse()
	if *duration <= 0 || *out == "" {
		return fmt.Errorf("positive duration and output required")
	}
	if err := os.Mkdir(*out, 0700); err != nil {
		return err
	}
	capacity := domain.ResourceVector{CPUMilli: 1000, MemoryMiB: 256}
	var policy *scheduler.DAGDRCFS
	var err error
	switch *mode {
	case "dag-drcfs":
		policy, err = scheduler.NewDAGDRCFS(capacity, time.Second)
	case "dag-drcfs-time":
		policy, err = scheduler.NewDurationDAGDRCFS(capacity, time.Second)
	default:
		return fmt.Errorf("unsupported policy")
	}
	if err != nil {
		return err
	}
	store, err := statefile.OpenExclusive(filepath.Join(*out, "runtime.state"))
	if err != nil {
		return err
	}
	defer store.Close()
	kernel, _, err := runtimecore.NewPersistent(policy, capacity, "soak-"+*mode, store, time.Now())
	if err != nil {
		return err
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	agents := []domain.AgentSpec{}
	for i, id := range []string{"short", "long"} {
		delay := []string{"1s", "3s"}[i]
		agents = append(agents, domain.AgentSpec{ID: id, Name: id, Runtime: "process", Entrypoint: binary, Args: []string{"--worker-duration", delay}, Resources: domain.ResourceVector{CPUMilli: 250, MemoryMiB: 64}, MaxConcurrency: 4, ExecutionTimeout: 20 * time.Second})
	}
	coordinator, err := controlplane.New(kernel, worker.NewManager(executor.MinimalHostEnvironment(), nil), "soak-worker", agents)
	if err != nil {
		return err
	}
	trace, err := os.OpenFile(filepath.Join(*out, "events.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer trace.Close()
	samples, err := os.OpenFile(filepath.Join(*out, "samples.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer samples.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan struct {
		result controlplane.TickResult
		err    error
	}, 4)
	var workers sync.WaitGroup
	started := time.Now()
	deadline := started.Add(*duration)
	counts := map[string]int{"short": 0, "long": 0}
	completed := map[string]int{"short": 0, "long": 0}
	seconds := map[string]float64{"short": 0, "long": 0}
	add := func() error {
		batch := []domain.TaskSpec{}
		for _, id := range []string{"short", "long"} {
			if counts[id]-completed[id] < 16 {
				for j := 0; j < 16; j++ {
					counts[id]++
					name := fmt.Sprintf("%s-%05d", id, counts[id])
					batch = append(batch, domain.TaskSpec{ID: name, WorkflowID: id, AgentID: id, Goal: "synthetic sustained process execution", Resources: domain.ResourceVector{CPUMilli: 250, MemoryMiB: 64}, MaxAttempts: 1, Timeout: 20 * time.Second, IdempotencyKey: name})
				}
			}
		}
		if len(batch) == 0 {
			return nil
		}
		return coordinator.ApplyPatch(batch, time.Now())
	}
	if err := add(); err != nil {
		return err
	}
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for ctx.Err() == nil {
				result, err := coordinator.Tick(ctx, time.Now())
				if errors.Is(err, scheduler.ErrNoCandidate) {
					time.Sleep(5 * time.Millisecond)
					continue
				}
				select {
				case results <- struct {
					result controlplane.TickResult
					err    error
				}{result, err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	steadyCounts := map[string]int{}
	steadySeconds := map[string]float64{}
	steadyCaptured := false
	for {
		if time.Now().After(deadline) && !steadyCaptured {
			for k, v := range completed {
				steadyCounts[k] = v
			}
			for k, v := range seconds {
				steadySeconds[k] = v
			}
			steadyCaptured = true
		}
		if steadyCaptured && completed["short"]+completed["long"] == counts["short"]+counts["long"] {
			break
		}
		select {
		case item := <-results:
			r := item.result
			e := event{Task: r.Decision.Candidate.TaskID, Workflow: r.Decision.Candidate.WorkflowID, Started: r.Execution.Process.StartedAt, Finished: r.Execution.Process.FinishedAt}
			e.ProcessSeconds = e.Finished.Sub(e.Started).Seconds()
			if item.err != nil {
				e.Error = item.err.Error()
			}
			if err := json.NewEncoder(trace).Encode(e); err != nil {
				return err
			}
			if item.err != nil {
				return item.err
			}
			if r.Execution.Attempt.Status != domain.AttemptStatusSucceeded {
				return fmt.Errorf("attempt not successful")
			}
			completed[e.Workflow]++
			seconds[e.Workflow] += e.ProcessSeconds
			if !steadyCaptured {
				if err := add(); err != nil {
					return err
				}
			}
		case now := <-ticker.C:
			stats := kernel.Stats()
			if !stats.Allocated.FitsWithin(capacity) {
				return fmt.Errorf("capacity exceeded")
			}
			var mem runtime.MemStats
			runtime.ReadMemStats(&mem)
			if err := json.NewEncoder(samples).Encode(map[string]any{"at": now, "elapsed_seconds": time.Since(started).Seconds(), "completed": completed, "process_seconds": seconds, "runtime": stats, "goroutines": runtime.NumGoroutine(), "heap_alloc": mem.HeapAlloc}); err != nil {
				return err
			}
		}
	}
	cancel()
	workers.Wait()
	if !kernel.Stats().Allocated.IsZero() || kernel.Stats().PendingCompletionCount != 0 {
		return fmt.Errorf("final resource or completion leak")
	}
	for _, task := range kernel.Tasks() {
		if task.Status != domain.TaskStatusSucceeded {
			return fmt.Errorf("unfinished task")
		}
	}
	if err := trace.Sync(); err != nil {
		return err
	}
	if err := samples.Sync(); err != nil {
		return err
	}
	summary := map[string]any{"status": "complete", "policy": *mode, "started_at": started, "finished_at": time.Now(), "requested_seconds": duration.Seconds(), "completed": completed, "process_seconds": seconds, "steady_completed": steadyCounts, "steady_process_seconds": steadySeconds, "fairness": policy.FairnessState(), "final_runtime": kernel.Stats(), "synthetic_workload": true, "kernel_resource_limits": false}
	payload, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, "summary.json"), payload, 0600); err != nil {
		return err
	}
	fmt.Println(string(payload))
	return nil
}
