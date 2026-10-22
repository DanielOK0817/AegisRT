package runtimecore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"aegisrt/internal/agentprotocol"
	"aegisrt/internal/domain"
	"aegisrt/internal/taskgraph"
)

var ErrCompletionPending = errors.New("task has a durable completion pending publication")

// PendingCompletion is a durable decision to finish an exited, cleaned-up
// attempt. Publication can be retried, but the Agent must never run again.
type PendingCompletion struct {
	TaskID        string                   `json:"task_id"`
	AttemptNumber int                      `json:"attempt_number"`
	Artifacts     []agentprotocol.Artifact `json:"artifacts"`
	PreparedAt    time.Time                `json:"prepared_at"`
}

func (r *Runtime) PrepareCompletion(taskID string, attempt int, artifacts []agentprotocol.Artifact, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureHealthyLocked(); err != nil {
		return err
	}
	item, exists := r.graph.Task(taskID)
	if !exists {
		return fmt.Errorf("%w: %q", taskgraph.ErrTaskNotFound, taskID)
	}
	if item.Status != domain.TaskStatusRunning || attempt <= 0 || r.attempts[taskID] != attempt {
		return fmt.Errorf("invalid completion attempt for task %q", taskID)
	}
	if _, canceled := r.cancelRequested[taskID]; canceled {
		return context.Canceled
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if err := validateCompletionArtifacts(artifacts); err != nil {
		return err
	}
	record := cloneCompletion(PendingCompletion{TaskID: taskID, AttemptNumber: attempt, Artifacts: artifacts, PreparedAt: now.UTC()})
	if previous, exists := r.pendingCompletions[taskID]; exists {
		if previous.AttemptNumber != attempt || !reflect.DeepEqual(previous.Artifacts, record.Artifacts) {
			return ErrCompletionPending
		}
		return nil
	}
	before := r.mutationSnapshotLocked(now)
	r.pendingCompletions[taskID] = record
	return r.commitMutationLocked(before, now)
}

func (r *Runtime) PendingCompletions() []PendingCompletion {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pendingCompletionsLocked()
}

func (r *Runtime) pendingCompletionsLocked() []PendingCompletion {
	result := make([]PendingCompletion, 0, len(r.pendingCompletions))
	for _, item := range r.pendingCompletions {
		result = append(result, cloneCompletion(item))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].TaskID < result[j].TaskID })
	return result
}

func cloneCompletion(record PendingCompletion) PendingCompletion {
	artifacts := make([]agentprotocol.Artifact, len(record.Artifacts))
	copy(artifacts, record.Artifacts)
	for i := range artifacts {
		artifacts[i].Recipients = append([]string(nil), artifacts[i].Recipients...)
	}
	record.Artifacts = artifacts
	return record
}

func validateCompletionArtifacts(artifacts []agentprotocol.Artifact) error {
	if err := agentprotocol.ValidateArtifacts(artifacts); err != nil {
		return err
	}
	payload, err := json.Marshal(agentprotocol.Envelope{Version: agentprotocol.VersionV1, Artifacts: artifacts})
	if err != nil {
		return err
	}
	if len(payload) > agentprotocol.MaxOutputBytes {
		return agentprotocol.ErrOutputTooLarge
	}
	return nil
}

func validatePendingCompletions(checkpoint Checkpoint, graph *taskgraph.TaskGraph, attempts map[string]int, canceled map[string]struct{}) (map[string]PendingCompletion, error) {
	result := make(map[string]PendingCompletion, len(checkpoint.PendingCompletions))
	for _, record := range checkpoint.PendingCompletions {
		item, exists := graph.Task(record.TaskID)
		_, duplicate := result[record.TaskID]
		_, cancelRequested := canceled[record.TaskID]
		if !exists || duplicate || cancelRequested || item.Status != domain.TaskStatusRunning ||
			record.AttemptNumber <= 0 || record.AttemptNumber != attempts[record.TaskID] || record.PreparedAt.IsZero() {
			return nil, fmt.Errorf("%w: invalid pending completion %q", ErrInvalidCheckpoint, record.TaskID)
		}
		if err := validateCompletionArtifacts(record.Artifacts); err != nil {
			return nil, errors.Join(ErrInvalidCheckpoint, err)
		}
		result[record.TaskID] = cloneCompletion(record)
	}
	return result, nil
}
