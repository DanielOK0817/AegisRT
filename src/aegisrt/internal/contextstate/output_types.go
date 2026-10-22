package contextstate

import (
	"time"

	"aegisrt/internal/contextvm"
)

type OutputTarget struct {
	AgentID               string
	ExpectedManifestID    string
	TaskTokenBudget       int64
	PrincipalPinnedBudget int64
}

type ArtifactCommand struct {
	Name       string
	Content    string
	Visibility Visibility
	Recipients []string
	Pin        bool
	Cold       bool
	Targets    []string
}

type OutputCommand struct {
	AttemptNumber   int
	TaskID          string
	WorkflowID      string
	ProducerAgentID string
	Targets         []OutputTarget
	Artifacts       []ArtifactCommand
}

type ArtifactRecord struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	Visibility Visibility       `json:"visibility"`
	Recipients []string         `json:"recipients,omitempty"`
	Targets    []string         `json:"targets"`
	Pin        bool             `json:"pin,omitempty"`
	Cold       bool             `json:"cold,omitempty"`
	PageID     contextvm.PageID `json:"page_id"`
	TokenCount int              `json:"token_count"`
}

type OutputRecord struct {
	AttemptNumber   int              `json:"attempt_number,omitempty"`
	TaskID          string           `json:"task_id"`
	WorkflowID      string           `json:"workflow_id"`
	ProducerAgentID string           `json:"producer_agent_id"`
	Fingerprint     string           `json:"fingerprint"`
	PublishedAt     time.Time        `json:"published_at"`
	Artifacts       []ArtifactRecord `json:"artifacts"`
}

type OutputResult struct {
	Record           OutputRecord   `json:"record"`
	Appends          []AppendResult `json:"appends"`
	IdempotentReplay bool           `json:"idempotent_replay"`
}
