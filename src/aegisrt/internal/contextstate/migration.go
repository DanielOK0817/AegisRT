package contextstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"aegisrt/internal/contextvm"
	"aegisrt/internal/domain"
)

var ErrMigrationOwnershipRequired = errors.New("complete task ownership mapping required: legacy replay history was evicted")

// MigrationBinding is an explicit administrative assignment for a legacy
// checkpoint whose evicted history cannot establish complete task ownership.
type MigrationBinding struct {
	TaskID     string                 `json:"task_id"`
	WorkflowID string                 `json:"workflow_id"`
	AgentID    string                 `json:"agent_id"`
	Refs       []contextvm.ContextRef `json:"refs"`
}

// MigrateLegacyCheckpoint is pure: it validates a copy and returns new bytes.
// It never guesses ownership from Agent identity and never changes its input.
func MigrateLegacyCheckpoint(config Config, payload []byte, tasks []domain.TaskSpec, explicit []MigrationBinding, now time.Time) ([]byte, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	if len(payload) > config.Limits.MaxCheckpointBytes {
		return nil, ErrCheckpointCapacity
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var old checkpoint
	if err := decoder.Decode(&old); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidCheckpoint
	}
	if old.FormatVersion != 1 && old.FormatVersion != 2 {
		return nil, ErrInvalidCheckpoint
	}
	if old.RuntimeInstanceID != config.RuntimeInstanceID || old.Generation == 0 || old.SavedAt.IsZero() {
		return nil, ErrInvalidCheckpoint
	}
	counter, summarizer := componentIdentities(config)
	if old.CounterIdentity != "" && old.CounterIdentity != counter {
		return nil, ErrInvalidCheckpoint
	}
	if old.SummarizerIdentity != "" && old.SummarizerIdentity != summarizer {
		return nil, ErrInvalidCheckpoint
	}
	if old.CounterIdentity == "" && counter != "whitespace-v1" {
		return nil, fmt.Errorf("legacy counter identity only establishes whitespace-v1")
	}
	if old.SummarizerIdentity == "" && summarizer != "none-v1" {
		return nil, fmt.Errorf("legacy persistent state did not support summaries")
	}
	known := make(map[string]domain.TaskSpec, len(tasks))
	for _, task := range tasks {
		if err := task.Validate(); err != nil {
			return nil, err
		}
		if _, exists := known[task.ID]; exists {
			return nil, ErrInvalidTaskBinding
		}
		known[task.ID] = task
	}
	old.FormatVersion = checkpointFormatVersion
	old.CounterIdentity, old.SummarizerIdentity = counter, summarizer
	old.ConfigFingerprint = config.Fingerprint
	value, err := buildComponents(config, old)
	if err != nil {
		return nil, err
	}
	if explicit != nil {
		bindings := make([]taskInputBinding, 0, len(explicit))
		for _, b := range explicit {
			bindings = append(bindings, taskInputBinding{b.TaskID, b.WorkflowID, b.AgentID, b.Refs})
		}
		if err := restoreTaskInputs(&value, bindings); err != nil {
			return nil, err
		}
	} else if len(old.TaskInputs) == 0 {
		if old.NextSequence != uint64(len(old.Idempotency)) {
			return nil, ErrMigrationOwnershipRequired
		}
		records := append([]idempotencyRecord(nil), old.Idempotency...)
		sort.Slice(records, func(i, j int) bool { return records[i].Sequence < records[j].Sequence })
		for i, record := range records {
			if record.Sequence != uint64(i+1) {
				return nil, ErrMigrationOwnershipRequired
			}
			manifest := value.history[record.Result.ManifestID]
			page, err := value.store.Lookup(record.Result.PageID)
			if err != nil {
				return nil, err
			}
			var reference contextvm.ContextRef
			for _, ref := range manifest.Refs() {
				if ref.PageID == record.Result.PageID {
					reference = ref
				}
			}
			visibility := VisibilityAgent
			if page.Scope == contextvm.ScopeWorkflow {
				visibility = VisibilityWorkflow
			}
			fingerprint, err := validateAppendCommand(AppendCommand{TaskID: record.TaskID, WorkflowID: manifest.WorkflowID, AgentID: manifest.AgentID,
				ExpectedManifestID: manifest.ID, IdempotencyKey: record.Key, Content: page.Content, Visibility: visibility, Pin: reference.Pin, Cold: reference.Cold,
				TaskTokenBudget: int64(config.Limits.MaxPageTokens), PrincipalPinnedBudget: int64(config.Limits.MaxManifestTokens)}, config)
			if err != nil || fingerprint != record.Fingerprint {
				return nil, fmt.Errorf("%w: legacy ownership fingerprint mismatch", ErrInvalidCheckpoint)
			}
			isOutput := false
			if output, ok := value.outputs[record.TaskID]; ok {
				for _, a := range output.Artifacts {
					if a.PageID == record.Result.PageID && record.Key == outputTargetKey(a.ID, manifest.AgentID) {
						isOutput = true
					}
				}
			}
			if isOutput {
				continue
			}
			key := taskInputKey{record.TaskID, manifest.WorkflowID, manifest.AgentID}
			binding := value.inputs[key]
			binding.TaskID, binding.WorkflowID, binding.AgentID = key.taskID, key.workflowID, key.agentID
			for _, ref := range manifest.Refs() {
				if ref.PageID != record.Result.PageID {
					continue
				}
				duplicate := false
				for _, existing := range binding.Refs {
					duplicate = duplicate || existing.PageID == ref.PageID
				}
				if !duplicate {
					binding.Refs = append(binding.Refs, ref)
				}
			}
			value.inputs[key] = binding
		}
		if err := restoreTaskInputs(&value, sortedTaskInputs(value)); err != nil {
			return nil, err
		}
	}
	for key := range value.inputs {
		task, ok := known[key.taskID]
		if !ok || task.WorkflowID != key.workflowID || task.AgentID != key.agentID {
			return nil, fmt.Errorf("%w: migrated binding does not match Runtime task %q", ErrInvalidTaskBinding, key.taskID)
		}
	}
	for _, output := range value.outputs {
		task, ok := known[output.TaskID]
		if !ok || task.WorkflowID != output.WorkflowID || task.AgentID != output.ProducerAgentID {
			return nil, ErrInvalidTaskBinding
		}
	}
	for key := range value.heads {
		head, _ := value.registry.Get(key.workflowID, key.agentID)
		covered := make(map[contextvm.PageID]bool)
		for inputKey, binding := range value.inputs {
			if inputKey.workflowID == key.workflowID && inputKey.agentID == key.agentID {
				for _, ref := range binding.Refs {
					covered[ref.PageID] = true
				}
			}
		}
		for _, output := range value.outputs {
			if output.WorkflowID == key.workflowID {
				for _, artifact := range output.Artifacts {
					if artifactDeliveredTo(artifact, key.agentID) {
						covered[artifact.PageID] = true
					}
				}
			}
		}
		for _, ref := range head.Refs() {
			if !covered[ref.PageID] {
				return nil, fmt.Errorf("%w: unassigned legacy reference", ErrMigrationOwnershipRequired)
			}
		}
	}
	if old.Generation == ^uint64(0) {
		return nil, ErrInvalidCheckpoint
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	converted := checkpointFromComponents(value, config.Fingerprint, config.RuntimeInstanceID, old.Generation+1, now)
	result, err := json.Marshal(converted)
	if err != nil {
		return nil, err
	}
	if len(result) > config.Limits.MaxCheckpointBytes {
		return nil, ErrCheckpointCapacity
	}
	checked, err := decodeCheckpoint(result)
	if err != nil {
		return nil, err
	}
	if _, err := buildComponents(config, checked); err != nil {
		return nil, err
	}
	return result, nil
}
