package benchmark

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"
)

func CollectCommunication(ctx context.Context, options CommunicationOptions) (summary RunSummary, resultErr error) {
	if ctx == nil {
		return summary, fmt.Errorf("context is required")
	}
	if err := options.Plan.validate(); err != nil {
		return summary, err
	}
	if len(options.SourceSHA256) != 64 || !isLowerHex(options.SourceSHA256) {
		return summary, fmt.Errorf("build source SHA256 is required; use experiments/communication/run.ps1")
	}
	parent, err := validateOutputParent(options.OutputDir)
	if err != nil {
		return summary, err
	}
	if err := ctx.Err(); err != nil {
		return summary, err
	}
	planJSON, err := json.Marshal(options.Plan)
	if err != nil {
		return summary, err
	}
	planHash := sha256.Sum256(planJSON)
	software, err := collectSoftware(hex.EncodeToString(planHash[:]))
	if err != nil {
		return summary, err
	}
	environment, err := currentCommunicationEnvironment()
	if err != nil {
		return summary, err
	}
	runDir, err := os.MkdirTemp(parent, "communication-")
	if err != nil {
		return summary, err
	}
	summary = RunSummary{RunID: filepath.Base(runDir), OutputDir: runDir}
	report := CommunicationReport{SchemaVersion: 1, Status: "running", RunID: summary.RunID,
		StartedAt: time.Now().UTC(), SourceSHA256: options.SourceSHA256, Software: software,
		Environment: environment, Plan: options.Plan, Counter: "whitespace-v1", GOMAXPROCS: runtime.GOMAXPROCS(0),
		Scope: []string{"local persistent Context repository calls; no HTTP, process execution, or model",
			"shared repository, distinct workflow per operation, identical payload across workflows",
			"one untimed warmup per case; fresh repository per repetition; fixed case order",
			"recipient count excludes producer; latency includes all recipient deliveries per operation",
			"context_load repeats the same fanout read 32 times per sample to reduce clock quantization; divide sample latency by read_passes for mean fanout read time",
			"latency starts when a worker invokes the operation and includes repository lock waiting",
			"zero latency samples are retained when a call is below the local clock resolution",
			"Go process allocation deltas include workers and checks, not RSS or peak memory",
			"checkpoint payload bytes count successful Save inputs, not filesystem physical IO",
			"setup, garbage collection, replay and reload checks excluded from timed region",
			"no historical baseline or causal performance improvement claim"}}
	defer func() {
		report.FinishedAt = time.Now().UTC()
		report.Status = "complete"
		if resultErr != nil {
			report.Status, report.Failure = "failed", resultErr.Error()
		}
		if err := writeCommunicationJSON(filepath.Join(runDir, "report.json"), report); err != nil && resultErr == nil {
			resultErr = err
		}
	}()
	caseIndex := 0
	for _, operation := range []string{"output_publish", "context_load", "context_hydrate"} {
		for _, size := range options.Plan.PayloadBytes {
			for _, recipients := range options.Plan.Recipients {
				for _, concurrency := range options.Plan.Concurrency {
					c := CommunicationCase{Operation: operation, PayloadBytes: size, Recipients: recipients, Concurrency: concurrency}
					for repetition := 0; repetition <= options.Plan.Repetitions; repetition++ {
						if err := ctx.Err(); err != nil {
							return summary, err
						}
						dir := filepath.Join(runDir, fmt.Sprintf("case-%03d-repetition-%02d", caseIndex, repetition))
						if err := os.Mkdir(dir, 0o700); err != nil {
							return summary, err
						}
						trial, err := runCommunicationTrial(ctx, dir, c, options.Plan.Operations, repetition)
						if err != nil {
							return summary, fmt.Errorf("case %d repetition %d: %w", caseIndex, repetition, err)
						}
						if err := writeCommunicationJSON(filepath.Join(dir, "trial.json"), trial); err != nil {
							return summary, err
						}
						if repetition > 0 {
							report.Trials = append(report.Trials, trial)
							summary.RowCount++
						}
					}
					caseIndex++
				}
			}
		}
	}
	return summary, nil
}

func runCommunicationTrial(ctx context.Context, dir string, c CommunicationCase, count, repetition int) (CommunicationTrial, error) {
	c.ReadPasses = 1
	if c.Operation == "context_load" {
		c.ReadPasses = 32
	}
	trial := CommunicationTrial{Case: c, Repetition: repetition, Samples: make([]CommunicationSample, count)}
	w, err := newCommunicationWorkload(ctx, dir, c, count)
	if err != nil {
		return trial, err
	}
	beforeFile, err := os.Stat(w.path)
	if err != nil {
		return trial, err
	}
	trial.CheckpointBeforeBytes = beforeFile.Size()
	trial.PhysicalBeforeBytes = w.repository.Stats().Store.PhysicalBytes
	saves, written := w.store.saves, w.store.written
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	errors := make([]error, count)
	jobs := make(chan int, count)
	for i := 0; i < count; i++ {
		jobs <- i
	}
	close(jobs)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for worker := 0; worker < c.Concurrency; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for i := range jobs {
				began := time.Now()
				delivered, err := w.run(ctx, i)
				trial.Samples[i] = CommunicationSample{Index: i, LatencyNS: time.Since(began).Nanoseconds(), DeliveredBytes: delivered}
				errors[i] = err
			}
		}()
	}
	trial.StartedAt = time.Now().UTC()
	started := time.Now()
	close(start)
	workers.Wait()
	trial.WallNS = time.Since(started).Nanoseconds()
	runtime.ReadMemStats(&after)
	for _, err := range errors {
		if err != nil {
			return trial, err
		}
	}
	trial.TotalAllocBytes = after.TotalAlloc - before.TotalAlloc
	trial.HeapAllocBeforeBytes, trial.HeapAllocAfterBytes = before.HeapAlloc, after.HeapAlloc
	trial.Mallocs, trial.GCCycles = after.Mallocs-before.Mallocs, after.NumGC-before.NumGC
	afterFile, err := os.Stat(w.path)
	if err != nil {
		return trial, err
	}
	trial.CheckpointAfterBytes = afterFile.Size()
	trial.CheckpointSaveCalls = w.store.saves - saves
	trial.CheckpointPayloadWrittenBytes = w.store.written - written
	trial.PhysicalAfterBytes = w.repository.Stats().Store.PhysicalBytes
	latencies := make([]int64, count)
	for i, sample := range trial.Samples {
		latencies[i] = sample.LatencyNS
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	trial.P50NS, trial.P95NS = latencies[(count*50+99)/100-1], latencies[(count*95+99)/100-1]
	if err := w.verify(ctx); err != nil {
		return trial, err
	}
	trial.ReplayStable, trial.ReloadVerified, trial.IsolationVerified = true, true, true
	return trial, nil
}

func writeCommunicationJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
