package contextstate

import (
	"aegisrt/internal/domain"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type contextIdentity struct {
	FormatVersion     int                   `json:"format_version"`
	RuntimeInstanceID string                `json:"runtime_instance_id"`
	Counter           string                `json:"counter"`
	Summarizer        string                `json:"summarizer"`
	Limits            contextLimitsIdentity `json:"limits"`
	AgentIDs          []string              `json:"agent_ids"`
}

type contextLimitsIdentity struct {
	MaxPageBytes       int `json:"max_page_bytes"`
	MaxPageTokens      int `json:"max_page_tokens"`
	MaxManifestPages   int `json:"max_manifest_pages"`
	MaxManifestTokens  int `json:"max_manifest_tokens"`
	MaxLedgerEntries   int `json:"max_ledger_entries"`
	MaxCheckpointBytes int `json:"max_checkpoint_bytes"`
}

func ConfigFingerprint(
	agents []domain.AgentSpec,
	runtimeInstanceID string,
	limits Limits,
	counterIdentity, summarizerIdentity string,
) (string, error) {
	decodedID, err := hex.DecodeString(runtimeInstanceID)
	if len(runtimeInstanceID) != 64 || runtimeInstanceID != strings.ToLower(runtimeInstanceID) ||
		err != nil || len(decodedID) != 32 {
		return "", fmt.Errorf("invalid Runtime instance ID for Context identity")
	}
	ids := make([]string, 0, len(agents))
	seen := make(map[string]struct{}, len(agents))
	for _, agent := range agents {
		if err := agent.Validate(); err != nil {
			return "", fmt.Errorf("validate Context identity agent %q: %w", agent.ID, err)
		}
		if _, exists := seen[agent.ID]; exists {
			return "", fmt.Errorf("duplicate Context identity agent %q", agent.ID)
		}
		seen[agent.ID] = struct{}{}
		ids = append(ids, agent.ID)
	}
	sort.Strings(ids)
	payload, err := json.Marshal(contextIdentity{
		FormatVersion: 3, RuntimeInstanceID: runtimeInstanceID,
		Counter: counterIdentity, Summarizer: summarizerIdentity,
		Limits: contextLimitsIdentity{
			MaxPageBytes: limits.MaxPageBytes, MaxPageTokens: limits.MaxPageTokens,
			MaxManifestPages: limits.MaxManifestPages, MaxManifestTokens: limits.MaxManifestTokens,
			MaxLedgerEntries: limits.MaxLedgerEntries, MaxCheckpointBytes: limits.MaxCheckpointBytes,
		},
		AgentIDs: ids,
	})
	if err != nil {
		return "", fmt.Errorf("encode Context configuration identity: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}
