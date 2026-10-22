package runtimecore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"aegisrt/internal/scheduler"
	"aegisrt/internal/taskgraph"
)

const legacyCheckpointFormatV1 = 1

type legacyV1Checkpoint struct {
	FormatVersion     int              `json:"format_version"`
	Generation        uint64           `json:"generation"`
	SavedAt           time.Time        `json:"saved_at"`
	ConfigFingerprint string           `json:"config_fingerprint"`
	Graph             taskgraph.State  `json:"graph"`
	Scheduler         scheduler.State  `json:"scheduler"`
	ReadySince        []ReadyTimestamp `json:"ready_since,omitempty"`
	Attempts          []AttemptCounter `json:"attempts,omitempty"`
	CancelRequested   []string         `json:"cancel_requested,omitempty"`
}

func decodeLegacyV1Checkpoint(payload []byte) (legacyV1Checkpoint, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var checkpoint legacyV1Checkpoint
	if err := decoder.Decode(&checkpoint); err != nil {
		return legacyV1Checkpoint{}, fmt.Errorf("%w: decode legacy v1: %v", ErrInvalidCheckpoint, err)
	}
	if err := ensureCheckpointEOF(decoder); err != nil {
		return legacyV1Checkpoint{}, err
	}
	if checkpoint.FormatVersion != legacyCheckpointFormatV1 {
		return legacyV1Checkpoint{}, fmt.Errorf(
			"%w: unsupported legacy format version %d", ErrInvalidCheckpoint, checkpoint.FormatVersion,
		)
	}
	if checkpoint.Generation == 0 || checkpoint.SavedAt.IsZero() {
		return legacyV1Checkpoint{}, fmt.Errorf(
			"%w: legacy generation and saved_at are required", ErrInvalidCheckpoint,
		)
	}
	if strings.TrimSpace(checkpoint.ConfigFingerprint) == "" ||
		strings.TrimSpace(checkpoint.ConfigFingerprint) != checkpoint.ConfigFingerprint {
		return legacyV1Checkpoint{}, fmt.Errorf(
			"%w: legacy config_fingerprint is required", ErrInvalidCheckpoint,
		)
	}
	return checkpoint, nil
}

func (c legacyV1Checkpoint) upgrade(instanceID string) Checkpoint {
	return Checkpoint{
		FormatVersion:                 checkpointFormatVersion,
		InstanceID:                    instanceID,
		LegacyContextMigrationPending: true,
		Generation:                    c.Generation,
		SavedAt:                       c.SavedAt,
		ConfigFingerprint:             c.ConfigFingerprint,
		Graph:                         c.Graph,
		Scheduler:                     c.Scheduler,
		ReadySince:                    c.ReadySince,
		Attempts:                      c.Attempts,
		CancelRequested:               c.CancelRequested,
	}
}

// LegacyContextMigrationPending reports whether the Runtime checkpoint was
// upgraded from v1 but its separately stored Context state still needs an
// explicit migration.
func (r *Runtime) LegacyContextMigrationPending() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.legacyContextMigrationPending
}

// CompleteLegacyContextMigration durably records that legacy Context state has
// been migrated. The operation is idempotent and uses normal checkpoint
// rollback and poison semantics.
func (r *Runtime) CompleteLegacyContextMigration(now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureHealthyLocked(); err != nil {
		return err
	}
	if !r.legacyContextMigrationPending {
		return nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	before := r.mutationSnapshotLocked(now)
	r.legacyContextMigrationPending = false
	return r.commitMutationLocked(before, now)
}
