// completion-boundaries runs the production Coordinator and durable repositories
// with experiment-only wrappers that block at an externally killed boundary.
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
	"strconv"
	"strings"
	"time"

	"aegisrt/internal/agentprotocol"
	"aegisrt/internal/contextstate"
	"aegisrt/internal/contextvm"
	"aegisrt/internal/controlplane"
	"aegisrt/internal/domain"
	"aegisrt/internal/executor"
	"aegisrt/internal/runtimeapp"
	"aegisrt/internal/runtimecore"
	"aegisrt/internal/scheduler"
	"aegisrt/internal/statefile"
	"aegisrt/internal/worker"
)

const fingerprint = "completion-boundaries-v1"
const frozenBody = "Frozen research result. Evidence alpha 17, evidence beta 29. 中文正文保持一致。"

type event map[string]any

func appendEvent(dir string, value event) error {
	value["at"] = time.Now().UTC()
	value["pid"] = os.Getpid()
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err = file.Write(append(data, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

func saveJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err = file.Write(append(data, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

// The Python controller sends an OS-level hard kill after seeing this marker.
// No defers, completion callbacks, or graceful daemon shutdown are invoked.
func blockAt(dir, point string) {
	if err := saveJSON(filepath.Join(dir, "boundary.json"), event{"point": point, "pid": os.Getpid(), "at": time.Now().UTC()}); err != nil {
		panic(err)
	}
	for {
		time.Sleep(time.Hour)
	}
}

type runtimeStore struct {
	*statefile.Store
	dir, phase, point string
	armed             bool
}

func (s *runtimeStore) Save(data []byte) error {
	if err := s.Store.Save(data); err != nil {
		return err
	}
	var checkpoint runtimecore.Checkpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return err
	}
	if err := saveJSON(filepath.Join(s.dir, "journal", fmt.Sprintf("runtime-%s-%06d.json", s.phase, checkpoint.Generation)), checkpoint); err != nil {
		return err
	}
	if err := appendEvent(s.dir, event{"kind": "runtime_saved", "phase": s.phase, "generation": checkpoint.Generation}); err != nil {
		return err
	}
	if !s.armed {
		return nil
	}
	producerStatus := domain.TaskStatus("")
	for _, item := range checkpoint.Graph.Tasks {
		if item.Spec.ID == "produce" {
			producerStatus = item.Status
		}
	}
	switch s.point {
	case "running":
		if producerStatus == domain.TaskStatusRunning && len(checkpoint.PendingCompletions) == 0 {
			// Wait for the actual Worker child to record its PID and input before
			// the controller kills this Coordinator process.
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, err := os.Stat(filepath.Join(s.dir, "producer-attempt-1.json")); err == nil {
					break
				}
				if time.Now().After(deadline) {
					return errors.New("producer did not register at running boundary")
				}
				time.Sleep(10 * time.Millisecond)
			}
			blockAt(s.dir, s.point)
		}
	case "prepared":
		if len(checkpoint.PendingCompletions) == 1 {
			blockAt(s.dir, s.point)
		}
	case "succeeded":
		if producerStatus == domain.TaskStatusSucceeded {
			blockAt(s.dir, s.point)
		}
	}
	return nil
}

type contextStore struct {
	*statefile.Store
	dir, phase string
}

func (s *contextStore) Save(data []byte) error {
	if err := s.Store.Save(data); err != nil {
		return err
	}
	var stored map[string]any
	if err := json.Unmarshal(data, &stored); err != nil {
		return err
	}
	generation := int(stored["generation"].(float64))
	if err := saveJSON(filepath.Join(s.dir, "journal", fmt.Sprintf("context-%s-%06d.json", s.phase, generation)), stored); err != nil {
		return err
	}
	return appendEvent(s.dir, event{"kind": "context_saved", "phase": s.phase, "generation": generation})
}

type publisher struct {
	target            *runtimeapp.ContextControl
	repository        *contextstate.Repository
	dir, phase, point string
}

func (p *publisher) ValidateAgentOutput(ctx context.Context, id string, attempt int, artifacts []agentprotocol.Artifact) error {
	return p.target.ValidateAgentOutput(ctx, id, attempt, artifacts)
}
func (p *publisher) PublishAgentOutput(ctx context.Context, id string, attempt int, artifacts []agentprotocol.Artifact) error {
	_, previousErr := p.repository.Output(id)
	if err := appendEvent(p.dir, event{"kind": "publication_requested", "phase": p.phase, "task_id": id, "attempt": attempt}); err != nil {
		return err
	}
	if err := p.target.PublishAgentOutput(ctx, id, attempt, artifacts); err != nil {
		return err
	}
	output, err := p.repository.Output(id)
	if err != nil {
		return err
	}
	if err := appendEvent(p.dir, event{"kind": "publication_returned", "phase": p.phase, "task_id": id, "attempt": attempt, "existing_before": previousErr == nil, "output": output}); err != nil {
		return err
	}
	if p.phase == "initial" && p.point == "published" && id == "produce" {
		blockAt(p.dir, p.point)
	}
	return nil
}

func agent(dir, role, point string) error {
	var input struct {
		TaskID        string `json:"task_id"`
		AttemptNumber int    `json:"attempt_number"`
		Context       string `json:"context"`
	}
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&input); err != nil {
		return err
	}
	if err := appendEvent(dir, event{"kind": "agent_started", "task_id": input.TaskID, "attempt": input.AttemptNumber, "role": role}); err != nil {
		return err
	}
	if role == "producer" {
		if err := saveJSON(filepath.Join(dir, fmt.Sprintf("producer-attempt-%d.json", input.AttemptNumber)), event{"pid": os.Getpid(), "executable": os.Args[0], "dir": dir, "attempt": input.AttemptNumber}); err != nil {
			return err
		}
		if point == "running" && input.AttemptNumber == 1 {
			time.Sleep(60 * time.Second)
		}
	}
	body := frozenBody
	visibility := "workflow"
	if role == "consumer" {
		body = input.Context
		visibility = "agent"
		if err := os.WriteFile(filepath.Join(dir, "consumer-input.txt"), []byte(body), 0600); err != nil {
			return err
		}
	}
	if err := appendEvent(dir, event{"kind": "agent_output_written", "task_id": input.TaskID, "attempt": input.AttemptNumber, "role": role}); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(agentprotocol.Envelope{Version: agentprotocol.VersionV1, Artifacts: []agentprotocol.Artifact{{Name: "result", Content: body, Visibility: visibility}}})
}

func openRuntime(dir, phase, point string) error {
	runtimeFile, err := statefile.OpenExclusive(filepath.Join(dir, "runtime.state"))
	if err != nil {
		return err
	}
	defer runtimeFile.Close()
	contextFile, err := statefile.OpenExclusive(filepath.Join(dir, "context.state"))
	if err != nil {
		return err
	}
	defer contextFile.Close()
	wrapped := &runtimeStore{Store: runtimeFile, dir: dir, phase: phase, point: point}
	capacity := domain.ResourceVector{CPUMilli: 1000, MemoryMiB: 512, TokenBudget: 8192}
	cleanup := func(taskID string, attempt int) error {
		if taskID != "produce" {
			return fmt.Errorf("unexpected active task %q", taskID)
		}
		data, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("producer-attempt-%d.json", attempt)))
		if err != nil {
			return err
		}
		var record struct {
			PID        int    `json:"pid"`
			Executable string `json:"executable"`
			Dir        string `json:"dir"`
		}
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		if record.Dir != dir {
			return errors.New("process registry has wrong experiment directory")
		}
		killed, err := killRegisteredAgent(record.PID, record.Executable, dir)
		if err != nil {
			return err
		}
		return appendEvent(dir, event{"kind": "recovery_cleanup", "task_id": taskID, "attempt": attempt, "agent_pid": record.PID, "kill_sent": killed})
	}
	kernel, report, err := runtimecore.NewPersistent(scheduler.FIFO{}, capacity, fingerprint, wrapped, time.Now().UTC(), cleanup)
	if err != nil {
		return err
	}
	if err := appendEvent(dir, event{"kind": "runtime_opened", "phase": phase, "report": report}); err != nil {
		return err
	}
	repository, err := contextstate.NewPersistent(contextstate.Config{Fingerprint: fingerprint, RuntimeInstanceID: kernel.InstanceID(), Counter: contextvm.WhitespaceCounter{}, Limits: contextstate.DefaultLimits()}, &contextStore{Store: contextFile, dir: dir, phase: phase}, phase == "initial", time.Now().UTC())
	if err != nil {
		return err
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	resources := domain.ResourceVector{CPUMilli: 100, MemoryMiB: 32, TokenBudget: 1024}
	agents := []domain.AgentSpec{}
	for _, role := range []string{"producer", "consumer"} {
		agents = append(agents, domain.AgentSpec{ID: role, Name: role, Runtime: "process", Entrypoint: binary, Args: []string{"-mode", "agent", "-dir", dir, "-role", role, "-point", point}, Resources: resources, MaxConcurrency: 1, ExecutionTimeout: 15 * time.Second})
	}
	pub := &publisher{repository: repository, dir: dir, phase: phase, point: point}
	coordinator, err := controlplane.NewWithOptions(kernel, worker.NewManager(executor.MinimalHostEnvironment(), nil), "boundary-worker", agents, controlplane.Options{ContextResolver: repository, OutputPublisher: pub})
	if err != nil {
		return err
	}
	control, err := runtimeapp.NewContextControl(coordinator, repository, agents)
	if err != nil {
		return err
	}
	pub.target = control
	if phase == "initial" {
		if err := control.ApplyPatch([]domain.TaskSpec{
			{ID: "produce", WorkflowID: "boundary-workflow", AgentID: "producer", Goal: "produce frozen research", Resources: resources, Priority: 1, MaxAttempts: 2, Timeout: 15 * time.Second, IdempotencyKey: "produce-key"},
			{ID: "consume", WorkflowID: "boundary-workflow", AgentID: "consumer", Goal: "read exact producer output", Dependencies: []string{"produce"}, Resources: resources, Priority: 1, MaxAttempts: 1, Timeout: 15 * time.Second, IdempotencyKey: "consume-key"},
		}, time.Now().UTC()); err != nil {
			return err
		}
		wrapped.armed = point != "none"
	}
	before := time.Now()
	if err := coordinator.RecoverOutputs(context.Background()); err != nil {
		return err
	}
	if err := appendEvent(dir, event{"kind": "outputs_recovered", "phase": phase, "elapsed_ns": time.Since(before).Nanoseconds()}); err != nil {
		return err
	}
	for {
		consumer, _ := kernel.Task("consume")
		if consumer.Status.IsTerminal() {
			break
		}
		result, err := coordinator.Tick(context.Background(), time.Now().UTC())
		if err != nil {
			return err
		}
		if err := appendEvent(dir, event{"kind": "tick_returned", "phase": phase, "result": result}); err != nil {
			return err
		}
	}
	outputs := map[string]contextstate.OutputRecord{}
	for _, id := range []string{"produce", "consume"} {
		record, err := repository.Output(id)
		if err != nil {
			return err
		}
		outputs[id] = record
	}
	return saveJSON(filepath.Join(dir, "observations-"+phase+".json"), event{"runtime_stats": kernel.Stats(), "context_stats": repository.Stats(), "tasks": kernel.Tasks(), "outputs": outputs, "pending": kernel.PendingCompletions(), "recovery_report": report, "frozen_body": frozenBody})
}

func main() {
	mode := flag.String("mode", "initial", "initial, recover, recover-again, or agent")
	dir := flag.String("dir", "", "isolated trial directory")
	point := flag.String("point", "none", "none, running, prepared, published, or succeeded")
	role := flag.String("role", "", "agent role")
	flag.Parse()
	if *dir == "" || !filepath.IsAbs(*dir) {
		fmt.Fprintln(os.Stderr, "absolute isolated trial directory is required")
		os.Exit(2)
	}
	if !strings.Contains("|none|running|prepared|published|succeeded|", "|"+*point+"|") {
		fmt.Fprintln(os.Stderr, "unknown point")
		os.Exit(2)
	}
	var err error
	switch *mode {
	case "agent":
		err = agent(*dir, *role, *point)
	case "initial", "recover", "recover-again":
		err = openRuntime(*dir, *mode, *point)
	default:
		err = errors.New("unknown mode: " + strconv.Quote(*mode))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
