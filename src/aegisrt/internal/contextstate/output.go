package contextstate

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

func (r *Repository) PublishOutput(
	ctx context.Context,
	command OutputCommand,
	now time.Time,
) (OutputResult, error) {
	if ctx == nil {
		return OutputResult{}, errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return OutputResult{}, err
	}
	fingerprint, targets, err := validateOutputCommand(command, r.config)
	if err != nil {
		return OutputResult{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureHealthyLocked(); err != nil {
		return OutputResult{}, err
	}
	existing, replay := r.outputs[command.TaskID]
	if replay && existing.Fingerprint != fingerprint {
		return OutputResult{}, ErrIdempotencyConflict
	}
	if !replay && len(r.outputs) >= r.config.Limits.MaxLedgerEntries {
		return OutputResult{}, ErrOutputCapacity
	}
	requirePrecondition := make(map[string]bool, len(targets))
	for index, artifact := range command.Artifacts {
		var stored ArtifactRecord
		if replay {
			stored = existing.Artifacts[index]
		}
		for _, agentID := range artifact.Targets {
			if !replay || !artifactDeliveredTo(stored, agentID) {
				requirePrecondition[agentID] = true
			}
		}
	}
	currentHeads := make(map[string]string, len(targets))
	for _, target := range targets {
		current, getErr := r.registry.Get(command.WorkflowID, target.AgentID)
		if getErr != nil {
			return OutputResult{}, getErr
		}
		if requirePrecondition[target.AgentID] && current.ID != target.ExpectedManifestID {
			return OutputResult{}, ErrPreconditionFailed
		}
		currentHeads[target.AgentID] = current.ID
	}

	candidate, err := r.cloneComponentsLocked()
	if err != nil {
		return OutputResult{}, r.poisonLocked(err)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	record := OutputRecord{
		AttemptNumber: command.AttemptNumber,
		TaskID:        command.TaskID, WorkflowID: command.WorkflowID,
		ProducerAgentID: command.ProducerAgentID, Fingerprint: fingerprint,
		PublishedAt: now, Artifacts: make([]ArtifactRecord, 0, len(command.Artifacts)),
	}
	if replay {
		record = cloneOutputRecord(existing)
	}
	result := OutputResult{Record: record, IdempotentReplay: replay}
	mutated := !replay
	for index, artifact := range command.Artifacts {
		artifactID := outputArtifactID(command.TaskID, artifact.Name)
		var stored ArtifactRecord
		if replay {
			stored = record.Artifacts[index]
		}
		for _, agentID := range artifact.Targets {
			if err := ctx.Err(); err != nil {
				return OutputResult{}, err
			}
			target := targets[agentID]
			appendCommand := AppendCommand{
				TaskID: command.TaskID, WorkflowID: command.WorkflowID, AgentID: agentID,
				ExpectedManifestID: currentHeads[agentID],
				IdempotencyKey:     outputTargetKey(artifactID, agentID),
				Content:            artifact.Content, Visibility: artifact.Visibility,
				Pin: artifact.Pin && agentID == command.ProducerAgentID, Cold: artifact.Cold,
				TaskTokenBudget: target.TaskTokenBudget, PrincipalPinnedBudget: target.PrincipalPinnedBudget,
			}
			appendFingerprint, validateErr := validateAppendCommand(appendCommand, r.config)
			if validateErr != nil {
				return OutputResult{}, validateErr
			}
			appendResult, changed, applyErr := applyAppendCandidate(
				&candidate, appendCommand, appendFingerprint, r.config,
			)
			if errors.Is(applyErr, errSequenceExhausted) {
				return OutputResult{}, r.poisonLocked(applyErr)
			}
			if applyErr != nil {
				return OutputResult{}, applyErr
			}
			mutated = mutated || changed
			currentHeads[agentID] = appendResult.ManifestID
			result.Appends = append(result.Appends, appendResult)
			if stored.PageID == "" {
				stored = ArtifactRecord{
					ID: artifactID, Name: artifact.Name, Visibility: artifact.Visibility,
					Recipients: cloneStringsPreservingNil(artifact.Recipients),
					Pin:        artifact.Pin, Cold: artifact.Cold,
					PageID: appendResult.PageID, TokenCount: appendResult.TokenCount,
				}
			} else if stored.PageID != appendResult.PageID || stored.TokenCount != appendResult.TokenCount {
				return OutputResult{}, ErrIdempotencyConflict
			}
			if appendArtifactTarget(&stored, agentID) {
				mutated = true
			}
		}
		if !replay {
			record.Artifacts = append(record.Artifacts, stored)
		} else {
			record.Artifacts[index] = stored
		}
	}
	if mutated {
		candidate.outputs[command.TaskID] = cloneOutputRecord(record)
		result.Record = cloneOutputRecord(record)
	}
	if !mutated {
		return result, nil
	}
	if err := r.commitLocked(candidate, now); err != nil {
		return result, err
	}
	return result, nil
}

func (r *Repository) Output(taskID string) (OutputRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := r.ensureHealthyLocked(); err != nil {
		return OutputRecord{}, err
	}
	record, exists := r.outputs[taskID]
	if !exists {
		return OutputRecord{}, fmt.Errorf("%w: %q", ErrOutputNotFound, taskID)
	}
	return cloneOutputRecord(record), nil
}

func cloneOutputRecord(record OutputRecord) OutputRecord {
	record.Artifacts = append([]ArtifactRecord(nil), record.Artifacts...)
	for index := range record.Artifacts {
		record.Artifacts[index].Recipients = cloneStringsPreservingNil(record.Artifacts[index].Recipients)
		record.Artifacts[index].Targets = append([]string(nil), record.Artifacts[index].Targets...)
	}
	return record
}

func artifactDeliveredTo(record ArtifactRecord, agentID string) bool {
	for _, target := range record.Targets {
		if target == agentID {
			return true
		}
	}
	return false
}

func appendArtifactTarget(record *ArtifactRecord, agentID string) bool {
	if artifactDeliveredTo(*record, agentID) {
		return false
	}
	record.Targets = append(record.Targets, agentID)
	sort.Strings(record.Targets)
	return true
}

func cloneStringsPreservingNil(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}
