package benchmark

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aegisrt/internal/contextstate"
	"aegisrt/internal/contextvm"
	"aegisrt/internal/domain"
	"aegisrt/internal/statefile"
)

const communicationBudget = 65536

type communicationStore struct {
	*statefile.Store
	saves   uint64
	written uint64
}

func (s *communicationStore) Save(payload []byte) error {
	if err := s.Store.Save(payload); err != nil {
		return err
	}
	s.saves++
	s.written += uint64(len(payload))
	return nil
}

type communicationWorkload struct {
	repository *contextstate.Repository
	config     contextstate.Config
	store      *communicationStore
	path       string
	commands   []contextstate.OutputCommand
	content    string
	caseSpec   CommunicationCase
}

func newCommunicationWorkload(ctx context.Context, dir string, c CommunicationCase, count int) (*communicationWorkload, error) {
	w := &communicationWorkload{path: filepath.Join(dir, "context.state"), caseSpec: c}
	w.content = strings.Repeat("payload ", (c.PayloadBytes+7)/8)[:c.PayloadBytes]
	w.config = contextstate.Config{Fingerprint: "communication-v1-whitespace-v1", RuntimeInstanceID: strings.Repeat("a", 64),
		Counter: contextvm.WhitespaceCounter{}, Limits: contextstate.DefaultLimits()}
	file, err := statefile.New(w.path)
	if err != nil {
		return nil, err
	}
	w.store = &communicationStore{Store: file}
	w.repository, err = contextstate.NewPersistent(w.config, w.store, true, time.Now())
	if err != nil {
		return nil, err
	}
	for i := 0; i < count; i++ {
		command := contextstate.OutputCommand{AttemptNumber: 1, TaskID: fmt.Sprintf("produce-%03d", i),
			WorkflowID: fmt.Sprintf("workflow-%03d", i), ProducerAgentID: "producer"}
		var targets []string
		for target := 0; target <= c.Recipients; target++ {
			agent := "producer"
			if target > 0 {
				agent = fmt.Sprintf("consumer-%02d", target)
			}
			manifest, _, err := w.repository.EnsureEmptyWithOwnership(command.WorkflowID, agent)
			if err != nil {
				return nil, err
			}
			if c.Operation == "context_hydrate" && target > 0 {
				continue
			}
			command.Targets = append(command.Targets, contextstate.OutputTarget{AgentID: agent,
				ExpectedManifestID: manifest.ID, TaskTokenBudget: communicationBudget, PrincipalPinnedBudget: communicationBudget})
			targets = append(targets, agent)
		}
		command.Artifacts = []contextstate.ArtifactCommand{{Name: "payload", Content: w.content,
			Visibility: contextstate.VisibilityWorkflow, Targets: targets}}
		w.commands = append(w.commands, command)
		if c.Operation != "output_publish" {
			if _, err := w.repository.PublishOutput(ctx, command, time.Now()); err != nil {
				return nil, err
			}
		}
	}
	return w, nil
}

func (w *communicationWorkload) run(ctx context.Context, index int) (int, error) {
	if w.caseSpec.Operation == "output_publish" {
		result, err := w.repository.PublishOutput(ctx, w.commands[index], time.Now())
		if err != nil {
			return 0, err
		}
		if result.IdempotentReplay || len(result.Appends) != w.caseSpec.Recipients+1 {
			return 0, fmt.Errorf("publication did not deliver to every target")
		}
		return len(w.content) * w.caseSpec.Recipients, nil
	}
	total := 0
	for pass := 0; pass < w.caseSpec.ReadPasses; pass++ {
		delivered, err := w.resolveAll(ctx, w.repository, index)
		if err != nil {
			return 0, err
		}
		total += delivered
	}
	return total, nil
}

func (w *communicationWorkload) resolveAll(ctx context.Context, repository *contextstate.Repository, index int) (int, error) {
	command := w.commands[index]
	for recipient := 1; recipient <= w.caseSpec.Recipients; recipient++ {
		task, agent := communicationConsumer(command, recipient)
		resolution, err := repository.Resolve(ctx, task, agent, communicationBudget)
		if err != nil {
			return 0, err
		}
		if resolution.Text != w.content || len(resolution.Refs) != 1 {
			return 0, fmt.Errorf("consumer payload or reference count mismatch")
		}
	}
	return len(w.content) * w.caseSpec.Recipients, nil
}

func communicationConsumer(command contextstate.OutputCommand, recipient int) (domain.TaskSpec, domain.AgentSpec) {
	agentID := fmt.Sprintf("consumer-%02d", recipient)
	resources := domain.ResourceVector{CPUMilli: 100, MemoryMiB: 64, TokenBudget: communicationBudget}
	task := domain.TaskSpec{ID: fmt.Sprintf("consume-%s-%02d", command.TaskID, recipient), WorkflowID: command.WorkflowID,
		AgentID: agentID, Goal: "verify fixed communication payload", Dependencies: []string{command.TaskID},
		Resources: resources, MaxAttempts: 1, Timeout: time.Minute, IdempotencyKey: "fixed-input"}
	agent := domain.AgentSpec{ID: agentID, Name: "local fixture", Runtime: "process", Entrypoint: "unused",
		Resources: resources, MaxConcurrency: 1, ExecutionTimeout: time.Minute}
	return task, agent
}

func (w *communicationWorkload) verify(ctx context.Context) error {
	before, err := os.ReadFile(w.path)
	if err != nil {
		return err
	}
	for index, command := range w.commands {
		result, err := w.repository.PublishOutput(ctx, command, time.Now())
		if err != nil || !result.IdempotentReplay {
			return fmt.Errorf("publication replay: %v", err)
		}
		if _, err := w.resolveAll(ctx, w.repository, index); err != nil {
			return err
		}
		task, agent := communicationConsumer(command, 1)
		task.ID += "-unrelated"
		task.Dependencies = nil
		resolution, err := w.repository.Resolve(ctx, task, agent, communicationBudget)
		if err != nil || resolution.Text != "" || len(resolution.Refs) != 0 {
			return fmt.Errorf("unrelated task received payload: %v", err)
		}
	}
	after, err := os.ReadFile(w.path)
	if err != nil {
		return err
	}
	if string(before) != string(after) {
		return fmt.Errorf("replay changed durable checkpoint")
	}
	reloaded, err := contextstate.NewPersistent(w.config, w.store, false, time.Now())
	if err != nil {
		return err
	}
	for index := range w.commands {
		if _, err := w.resolveAll(ctx, reloaded, index); err != nil {
			return err
		}
	}
	return nil
}
