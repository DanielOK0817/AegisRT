package main

import (
	"errors"
	"fmt"
	"time"

	"aegisrt/internal/contextdispatch"
	"aegisrt/internal/contextstate"
	"aegisrt/internal/domain"
	"aegisrt/internal/statefile"
	"aegisrt/internal/taskgraph"
)

const contextCounterIdentity = "whitespace-v1"
const contextSummarizerIdentity = "none-v1"

func buildContextRepository(
	options daemonOptions,
	tasks []taskgraph.TaskSnapshot,
	agents []domain.AgentSpec,
	runtimeInstanceID string,
	allowStateCreation bool,
	allowLegacyMigration bool,
) (*contextstate.Repository, *statefile.Store, error) {
	limits := contextstate.DefaultLimits()
	counter, summarizer, err := localContextComponents(options)
	if err != nil {
		return nil, nil, err
	}
	summarizerIdentity := contextSummarizerIdentity
	if summarizer != nil {
		summarizerIdentity = summarizer.Name()
	}
	fingerprint, err := contextConfigFingerprint(agents, runtimeInstanceID, limits, counter.Name(), summarizerIdentity)
	if err != nil {
		return nil, nil, err
	}
	config := contextstate.Config{
		Fingerprint: fingerprint, RuntimeInstanceID: runtimeInstanceID,
		Counter: counter, Limits: limits,
	}
	if summarizer != nil {
		config.Summarizer = summarizer
	}
	if options.ephemeral {
		repository, createErr := contextstate.New(config)
		if createErr != nil {
			return nil, nil, createErr
		}
		if err := validateRuntimeContextHeads(repository, tasks); err != nil {
			return nil, nil, err
		}
		return repository, nil, nil
	}
	store, err := statefile.OpenExclusive(options.stateFile + ".context")
	if err != nil {
		return nil, nil, fmt.Errorf("open Context state store: %w", err)
	}
	repository, err := contextstate.NewPersistent(config, store, allowStateCreation, time.Now().UTC())
	if err != nil {
		return nil, nil, errors.Join(err, store.Close())
	}
	if allowLegacyMigration {
		if err := migrateEmptyContextHeads(repository, tasks); err != nil {
			return nil, nil, errors.Join(err, store.Close())
		}
	}
	if err := validateRuntimeContextHeads(repository, tasks); err != nil {
		return nil, nil, errors.Join(err, store.Close())
	}
	return repository, store, nil
}

func migrateEmptyContextHeads(repository *contextstate.Repository, tasks []taskgraph.TaskSnapshot) error {
	seen := make(map[string]struct{}, len(tasks))
	for _, item := range tasks {
		key := item.Spec.WorkflowID + "\x00" + item.Spec.AgentID
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		if _, _, err := repository.EnsureEmptyWithOwnership(item.Spec.WorkflowID, item.Spec.AgentID); err != nil {
			return fmt.Errorf("migrate empty Context for task %q: %w", item.Spec.ID, err)
		}
	}
	return nil
}

func validateRuntimeContextHeads(repository *contextstate.Repository, tasks []taskgraph.TaskSnapshot) error {
	for _, item := range tasks {
		if item.Status.IsTerminal() {
			continue
		}
		snapshot, err := repository.Inspect(item.Spec.WorkflowID, item.Spec.AgentID)
		if err != nil {
			if errors.Is(err, contextdispatch.ErrManifestNotFound) {
				return fmt.Errorf("%w: task %q has no durable Context head", contextstate.ErrMigrationRequired, item.Spec.ID)
			}
			return err
		}
		if int64(snapshot.PinnedTokens) > item.Spec.Resources.TokenBudget {
			return fmt.Errorf("%w: task %q pinned Context exceeds its token budget", contextstate.ErrInvalidCheckpoint, item.Spec.ID)
		}
	}
	return nil
}

func contextConfigFingerprint(agents []domain.AgentSpec, runtimeInstanceID string, limits contextstate.Limits, counterIdentity, summarizerIdentity string) (string, error) {
	return contextstate.ConfigFingerprint(agents, runtimeInstanceID, limits, counterIdentity, summarizerIdentity)
}
