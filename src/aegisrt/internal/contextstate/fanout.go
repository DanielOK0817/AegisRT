package contextstate

import (
	"context"
	"errors"
	"time"
)

// Fanout atomically attaches one artifact to several principal manifests. All
// target-specific idempotency records and manifest updates share one Context
// checkpoint commit.
func (r *Repository) Fanout(ctx context.Context, commands []AppendCommand, now time.Time) ([]AppendResult, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fingerprints, err := validateFanout(commands, r.config)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureHealthyLocked(); err != nil {
		return nil, err
	}
	candidate, err := r.cloneComponentsLocked()
	if err != nil {
		return nil, r.poisonLocked(err)
	}
	results := make([]AppendResult, 0, len(commands))
	mutated := false
	for index, command := range commands {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result, changed, applyErr := applyInputCandidate(&candidate, command, fingerprints[index], r.config)
		if errors.Is(applyErr, errSequenceExhausted) {
			return nil, r.poisonLocked(applyErr)
		}
		if applyErr != nil {
			return nil, applyErr
		}
		mutated = mutated || changed
		results = append(results, result)
	}
	if !mutated {
		return results, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if err := r.commitLocked(candidate, now); err != nil {
		return results, err
	}
	return results, nil
}

func validateFanout(commands []AppendCommand, config Config) ([]string, error) {
	if len(commands) == 0 {
		return nil, ErrInvalidFanout
	}
	first := commands[0]
	seen := make(map[principal]struct{}, len(commands))
	fingerprints := make([]string, 0, len(commands))
	for _, command := range commands {
		if command.TaskID != first.TaskID || command.WorkflowID != first.WorkflowID ||
			command.Content != first.Content || command.Visibility != first.Visibility ||
			command.Pin != first.Pin || command.Cold != first.Cold {
			return nil, ErrInvalidFanout
		}
		key := principal{workflowID: command.WorkflowID, agentID: command.AgentID}
		if _, exists := seen[key]; exists {
			return nil, ErrInvalidFanout
		}
		seen[key] = struct{}{}
		fingerprint, err := validateAppendCommand(command, config)
		if err != nil {
			return nil, err
		}
		fingerprints = append(fingerprints, fingerprint)
	}
	if len(commands) > 1 && first.Visibility != VisibilityWorkflow {
		return nil, ErrInvalidFanout
	}
	return fingerprints, nil
}
