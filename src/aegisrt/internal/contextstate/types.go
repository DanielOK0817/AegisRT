package contextstate

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"aegisrt/internal/contextvm"
)

type CheckpointStore interface {
	Load() ([]byte, error)
	Save([]byte) error
}

type Config struct {
	Fingerprint       string
	RuntimeInstanceID string
	Counter           contextvm.Counter
	Summarizer        contextvm.Summarizer
	Limits            Limits
}

type Limits struct {
	MaxPageBytes       int
	MaxPageTokens      int
	MaxManifestPages   int
	MaxManifestTokens  int
	MaxLedgerEntries   int
	MaxCheckpointBytes int
}

func DefaultLimits() Limits {
	return Limits{
		MaxPageBytes: 64 << 10, MaxPageTokens: 8192,
		MaxManifestPages: 128, MaxManifestTokens: 65536,
		MaxLedgerEntries: 4096, MaxCheckpointBytes: 12 << 20,
	}
}

func (c Config) validate() error {
	if strings.TrimSpace(c.Fingerprint) == "" || strings.TrimSpace(c.Fingerprint) != c.Fingerprint {
		return fmt.Errorf("context fingerprint is required")
	}
	if err := validateRuntimeBindingID(c.RuntimeInstanceID); err != nil {
		return fmt.Errorf("context Runtime instance ID: %w", err)
	}
	if c.Counter == nil {
		return fmt.Errorf("context token counter is required")
	}
	limits := c.Limits
	if limits.MaxPageBytes <= 0 || limits.MaxPageTokens <= 0 || limits.MaxManifestPages <= 0 ||
		limits.MaxManifestTokens <= 0 || limits.MaxLedgerEntries <= 0 || limits.MaxCheckpointBytes <= 0 {
		return fmt.Errorf("all context limits must be positive")
	}
	return nil
}

func validateRuntimeBindingID(value string) error {
	if len(value) != 64 || value != strings.ToLower(value) {
		return fmt.Errorf("must be 64 lowercase hexadecimal characters")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return fmt.Errorf("must encode exactly 32 bytes as hexadecimal")
	}
	return nil
}

type Visibility string

const (
	VisibilityWorkflow Visibility = "workflow"
	VisibilityAgent    Visibility = "agent"
)

type AppendCommand struct {
	TaskID                string
	WorkflowID            string
	AgentID               string
	ExpectedManifestID    string
	IdempotencyKey        string
	Content               string
	Visibility            Visibility
	Pin                   bool
	Cold                  bool
	TaskTokenBudget       int64
	PrincipalPinnedBudget int64
}

type Snapshot struct {
	ManifestID    string `json:"id"`
	Revision      uint64 `json:"revision"`
	PageCount     int    `json:"page_count"`
	LogicalTokens int    `json:"logical_tokens"`
	PinnedTokens  int    `json:"pinned_tokens"`
}

type AppendResult struct {
	TaskID           string           `json:"task_id"`
	PageID           contextvm.PageID `json:"page_id"`
	TokenCount       int              `json:"token_count"`
	ManifestID       string           `json:"manifest_id"`
	Revision         uint64           `json:"revision"`
	ManifestChanged  bool             `json:"manifest_changed"`
	IdempotentReplay bool             `json:"idempotent_replay"`
}

type Stats struct {
	CounterIdentity    string               `json:"counter_identity"`
	SummarizerIdentity string               `json:"summarizer_identity"`
	SummaryCount       int                  `json:"summary_count"`
	Durable            bool                 `json:"durable"`
	CheckpointHealthy  bool                 `json:"checkpoint_healthy"`
	Generation         uint64               `json:"generation"`
	LastCheckpointAt   time.Time            `json:"last_checkpoint_at,omitempty"`
	ManifestCount      int                  `json:"manifest_count"`
	HeadCount          int                  `json:"head_count"`
	LedgerEntries      int                  `json:"ledger_entries"`
	OutputCount        int                  `json:"output_count"`
	ArtifactCount      int                  `json:"artifact_count"`
	Store              contextvm.StoreStats `json:"store"`
}
