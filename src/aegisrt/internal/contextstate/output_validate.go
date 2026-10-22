package contextstate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"aegisrt/internal/contextvm"
)

const (
	maxOutputArtifacts  = 32
	maxOutputRecipients = 32
	maxArtifactTargets  = maxOutputRecipients + 1
	maxOutputTargets    = maxOutputArtifacts * maxArtifactTargets
)

func validateOutputCommand(command OutputCommand, config Config) (string, map[string]OutputTarget, error) {
	if command.AttemptNumber < 0 {
		return "", nil, ErrInvalidOutputPublication
	}
	if !validIdempotencyKey(command.TaskID) || !validIdempotencyKey(command.WorkflowID) ||
		!validIdempotencyKey(command.ProducerAgentID) || len(command.Artifacts) > maxOutputArtifacts ||
		len(command.Targets) > maxOutputTargets {
		return "", nil, ErrInvalidOutputPublication
	}
	if len(command.Artifacts) == 0 && len(command.Targets) != 0 ||
		len(command.Artifacts) != 0 && len(command.Targets) == 0 {
		return "", nil, ErrInvalidOutputPublication
	}
	targets := make(map[string]OutputTarget, len(command.Targets))
	for _, target := range command.Targets {
		if !validIdempotencyKey(target.AgentID) || target.ExpectedManifestID == "" ||
			target.TaskTokenBudget <= 0 || target.PrincipalPinnedBudget < 0 {
			return "", nil, ErrInvalidOutputPublication
		}
		if _, err := hex.DecodeString(target.ExpectedManifestID); err != nil || len(target.ExpectedManifestID) != 64 {
			return "", nil, ErrInvalidOutputPublication
		}
		if _, exists := targets[target.AgentID]; exists {
			return "", nil, ErrInvalidOutputPublication
		}
		targets[target.AgentID] = target
	}
	if len(command.Artifacts) != 0 {
		if _, exists := targets[command.ProducerAgentID]; !exists {
			return "", nil, ErrInvalidOutputPublication
		}
	}
	usedTargets := make(map[string]struct{}, len(targets))
	for _, artifact := range command.Artifacts {
		for _, agentID := range artifact.Targets {
			usedTargets[agentID] = struct{}{}
		}
	}
	if len(usedTargets) != len(targets) {
		return "", nil, ErrInvalidOutputPublication
	}
	names := make(map[string]struct{}, len(command.Artifacts))
	type retention struct{ pin, cold bool }
	retentionByPage := make(map[string]retention, len(command.Artifacts))
	for _, artifact := range command.Artifacts {
		if !validOutputArtifact(artifact, command.ProducerAgentID, targets, config) {
			return "", nil, ErrInvalidOutputPublication
		}
		if _, exists := names[artifact.Name]; exists {
			return "", nil, ErrInvalidOutputPublication
		}
		names[artifact.Name] = struct{}{}
		pageKey := string(artifact.Visibility) + "\x00" + artifact.Content
		flags := retention{pin: artifact.Pin, cold: artifact.Cold}
		if existing, exists := retentionByPage[pageKey]; exists && existing != flags {
			return "", nil, ErrInvalidOutputPublication
		}
		retentionByPage[pageKey] = flags
	}
	fingerprint, err := outputFingerprint(command)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %v", ErrInvalidOutputPublication, err)
	}
	return fingerprint, targets, nil
}

func validOutputArtifact(
	artifact ArtifactCommand,
	producerAgentID string,
	targets map[string]OutputTarget,
	config Config,
) bool {
	if !validArtifactName(artifact.Name) || len(artifact.Content) == 0 ||
		len(artifact.Content) > config.Limits.MaxPageBytes || strings.TrimSpace(artifact.Content) == "" ||
		artifact.Visibility != VisibilityWorkflow && artifact.Visibility != VisibilityAgent ||
		artifact.Pin && artifact.Cold || len(artifact.Targets) == 0 || len(artifact.Targets) > maxArtifactTargets ||
		!validOutputRecipients(artifact.Recipients) {
		return false
	}
	targetSet := make(map[string]struct{}, len(artifact.Targets))
	for _, agentID := range artifact.Targets {
		if _, exists := targets[agentID]; !exists {
			return false
		}
		if _, exists := targetSet[agentID]; exists {
			return false
		}
		targetSet[agentID] = struct{}{}
	}
	if _, exists := targetSet[producerAgentID]; !exists {
		return false
	}
	if artifact.Visibility == VisibilityAgent {
		return artifact.Recipients == nil && len(targetSet) == 1
	}
	if artifact.Recipients == nil {
		return true
	}
	expected := map[string]struct{}{producerAgentID: {}}
	for _, recipient := range artifact.Recipients {
		if !validIdempotencyKey(recipient) {
			return false
		}
		expected[recipient] = struct{}{}
	}
	if len(expected) != len(targetSet) {
		return false
	}
	for agentID := range expected {
		if _, exists := targetSet[agentID]; !exists {
			return false
		}
	}
	return true
}

func outputFingerprint(command OutputCommand) (string, error) {
	type semanticArtifact struct {
		Name       string     `json:"name"`
		Content    string     `json:"content"`
		Visibility Visibility `json:"visibility"`
		Recipients []string   `json:"recipients"`
		Pin        bool       `json:"pin"`
		Cold       bool       `json:"cold"`
	}
	value := struct {
		AttemptNumber                       int `json:"AttemptNumber,omitempty"`
		TaskID, WorkflowID, ProducerAgentID string
		Artifacts                           []semanticArtifact
	}{
		AttemptNumber: command.AttemptNumber,
		TaskID:        command.TaskID, WorkflowID: command.WorkflowID, ProducerAgentID: command.ProducerAgentID,
		Artifacts: make([]semanticArtifact, 0, len(command.Artifacts)),
	}
	for _, artifact := range command.Artifacts {
		value.Artifacts = append(value.Artifacts, semanticArtifact{
			Name: artifact.Name, Content: artifact.Content, Visibility: artifact.Visibility,
			Recipients: cloneStringsPreservingNil(artifact.Recipients), Pin: artifact.Pin, Cold: artifact.Cold,
		})
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func outputArtifactID(taskID, name string) string {
	digest := sha256.Sum256([]byte(taskID + "\x00" + name))
	return hex.EncodeToString(digest[:])
}

func outputTargetKey(artifactID, agentID string) string {
	digest := sha256.Sum256([]byte(artifactID + "\x00" + agentID))
	return "output-" + hex.EncodeToString(digest[:])
}

func validArtifactName(value string) bool {
	if len(value) == 0 || len(value) > 64 || !alphaNumeric(value[0]) {
		return false
	}
	for index := 1; index < len(value); index++ {
		char := value[index]
		if !alphaNumeric(char) && char != '.' && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

func validOutputRecipients(values []string) bool {
	if values == nil {
		return true
	}
	if len(values) == 0 || len(values) > maxOutputRecipients {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validIdempotencyKey(value) {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func alphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func validateStoredOutput(
	store *contextvm.Store,
	history map[string]contextvm.Manifest,
	record OutputRecord,
) error {
	if record.AttemptNumber < 0 {
		return ErrInvalidCheckpoint
	}
	if !validIdempotencyKey(record.TaskID) || !validIdempotencyKey(record.WorkflowID) ||
		!validIdempotencyKey(record.ProducerAgentID) || record.PublishedAt.IsZero() ||
		len(record.Artifacts) > maxOutputArtifacts {
		return ErrInvalidCheckpoint
	}
	command := OutputCommand{
		AttemptNumber: record.AttemptNumber,
		TaskID:        record.TaskID, WorkflowID: record.WorkflowID, ProducerAgentID: record.ProducerAgentID,
		Artifacts: make([]ArtifactCommand, 0, len(record.Artifacts)),
	}
	names := make(map[string]struct{}, len(record.Artifacts))
	for _, artifact := range record.Artifacts {
		page, err := store.Lookup(artifact.PageID)
		if err != nil || page.TokenCount != artifact.TokenCount || artifact.ID != outputArtifactID(record.TaskID, artifact.Name) ||
			!validArtifactName(artifact.Name) || !validOutputRecipients(artifact.Recipients) || artifact.Pin && artifact.Cold ||
			!validStoredTargets(artifact, record.ProducerAgentID) ||
			!storedArtifactReachable(history, record, artifact) {
			return ErrInvalidCheckpoint
		}
		if _, exists := names[artifact.Name]; exists {
			return ErrInvalidCheckpoint
		}
		names[artifact.Name] = struct{}{}
		if artifact.Visibility == VisibilityWorkflow {
			if page.Scope != contextvm.ScopeWorkflow || page.WorkflowID != record.WorkflowID || page.AgentID != "" {
				return ErrInvalidCheckpoint
			}
		} else if artifact.Visibility == VisibilityAgent {
			if page.Scope != contextvm.ScopeAgent || page.WorkflowID != record.WorkflowID ||
				page.AgentID != record.ProducerAgentID || artifact.Recipients != nil {
				return ErrInvalidCheckpoint
			}
		} else {
			return ErrInvalidCheckpoint
		}
		command.Artifacts = append(command.Artifacts, ArtifactCommand{
			Name: artifact.Name, Content: page.Content, Visibility: artifact.Visibility,
			Recipients: cloneStringsPreservingNil(artifact.Recipients), Pin: artifact.Pin, Cold: artifact.Cold,
		})
	}
	fingerprint, err := outputFingerprint(command)
	if err != nil || fingerprint != record.Fingerprint {
		return ErrInvalidCheckpoint
	}
	return nil
}

func validStoredTargets(artifact ArtifactRecord, producerAgentID string) bool {
	if len(artifact.Targets) == 0 || len(artifact.Targets) > maxArtifactTargets {
		return false
	}
	targets := make(map[string]struct{}, len(artifact.Targets))
	for _, agentID := range artifact.Targets {
		if !validIdempotencyKey(agentID) {
			return false
		}
		if _, exists := targets[agentID]; exists {
			return false
		}
		targets[agentID] = struct{}{}
	}
	if _, exists := targets[producerAgentID]; !exists {
		return false
	}
	if artifact.Visibility == VisibilityAgent {
		return len(targets) == 1 && artifact.Recipients == nil
	}
	if artifact.Recipients == nil {
		return true
	}
	expected := map[string]struct{}{producerAgentID: {}}
	for _, recipient := range artifact.Recipients {
		expected[recipient] = struct{}{}
		if _, exists := targets[recipient]; !exists {
			return false
		}
	}
	return len(targets) == len(expected)
}

func storedArtifactReachable(
	history map[string]contextvm.Manifest,
	record OutputRecord,
	artifact ArtifactRecord,
) bool {
	for _, agentID := range artifact.Targets {
		found := false
		for _, manifest := range history {
			if manifest.WorkflowID != record.WorkflowID || manifest.AgentID != agentID {
				continue
			}
			for _, ref := range manifest.Refs() {
				wantPin := artifact.Pin && agentID == record.ProducerAgentID
				if ref.PageID == artifact.PageID && ref.Pin == wantPin && ref.Cold == artifact.Cold {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
