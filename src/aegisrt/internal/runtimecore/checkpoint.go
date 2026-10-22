package runtimecore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"sort"
	"strings"
	"time"

	"aegisrt/internal/domain"
	"aegisrt/internal/scheduler"
	"aegisrt/internal/taskgraph"
)

const checkpointFormatVersion = 2

var (
	ErrCheckpointUnavailable = errors.New("runtime checkpoint unavailable")
	ErrInvalidCheckpoint     = errors.New("invalid runtime checkpoint")
	ErrRuntimeUnavailable    = errors.New("runtime unavailable")
	ErrRecoveryBlocked       = errors.New("runtime recovery blocked")
)

type RecoveryHook func(taskID string, attemptNumber int) error

// PersistentOptions controls explicitly authorized checkpoint compatibility
// operations. Legacy migration is disabled unless requested here.
type PersistentOptions struct {
	AllowLegacyV1Migration bool
	RecoveryHook           RecoveryHook
}

type CheckpointStore interface {
	Load() ([]byte, error)
	Save([]byte) error
}

type Checkpoint struct {
	FormatVersion                 int                 `json:"format_version"`
	InstanceID                    string              `json:"instance_id"`
	LegacyContextMigrationPending bool                `json:"legacy_context_migration_pending"`
	Generation                    uint64              `json:"generation"`
	SavedAt                       time.Time           `json:"saved_at"`
	ConfigFingerprint             string              `json:"config_fingerprint"`
	Graph                         taskgraph.State     `json:"graph"`
	Scheduler                     scheduler.State     `json:"scheduler"`
	ReadySince                    []ReadyTimestamp    `json:"ready_since,omitempty"`
	Attempts                      []AttemptCounter    `json:"attempts,omitempty"`
	CancelRequested               []string            `json:"cancel_requested,omitempty"`
	PendingCompletions            []PendingCompletion `json:"pending_completions,omitempty"`
}

type ReadyTimestamp struct {
	TaskID  string    `json:"task_id"`
	ReadyAt time.Time `json:"ready_at"`
}

type AttemptCounter struct {
	TaskID string `json:"task_id"`
	Number int    `json:"number"`
}

type RecoveryReport struct {
	Loaded          bool     `json:"loaded"`
	Migrated        bool     `json:"migrated"`
	Recovered       bool     `json:"recovered"`
	RequeuedTaskIDs []string `json:"requeued_task_ids,omitempty"`
	FailedTaskIDs   []string `json:"failed_task_ids,omitempty"`
	CanceledTaskIDs []string `json:"canceled_task_ids,omitempty"`
}

func NewPersistent(
	policy scheduler.Policy,
	capacity domain.ResourceVector,
	configFingerprint string,
	store CheckpointStore,
	now time.Time,
	recoveryHooks ...RecoveryHook,
) (*Runtime, RecoveryReport, error) {
	now, err := validatePersistentArguments(store, configFingerprint, now)
	if err != nil {
		return nil, RecoveryReport{}, err
	}
	if len(recoveryHooks) > 1 {
		return nil, RecoveryReport{}, errors.New("at most one recovery hook is supported")
	}
	options := PersistentOptions{}
	if len(recoveryHooks) == 1 {
		options.RecoveryHook = recoveryHooks[0]
	}
	return newPersistent(policy, capacity, configFingerprint, store, now, options)
}

// NewPersistentWithOptions opens a durable Runtime with explicit compatibility
// options. Callers must opt in to each legacy migration.
func NewPersistentWithOptions(
	policy scheduler.Policy,
	capacity domain.ResourceVector,
	configFingerprint string,
	store CheckpointStore,
	now time.Time,
	options PersistentOptions,
) (*Runtime, RecoveryReport, error) {
	now, err := validatePersistentArguments(store, configFingerprint, now)
	if err != nil {
		return nil, RecoveryReport{}, err
	}
	return newPersistent(policy, capacity, configFingerprint, store, now, options)
}

func validatePersistentArguments(
	store CheckpointStore,
	configFingerprint string,
	now time.Time,
) (time.Time, error) {
	if store == nil {
		return time.Time{}, errors.New("checkpoint store is required")
	}
	if strings.TrimSpace(configFingerprint) == "" || strings.TrimSpace(configFingerprint) != configFingerprint {
		return time.Time{}, errors.New("config fingerprint is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return now, nil
}

func newPersistent(
	policy scheduler.Policy,
	capacity domain.ResourceVector,
	configFingerprint string,
	store CheckpointStore,
	now time.Time,
	options PersistentOptions,
) (*Runtime, RecoveryReport, error) {
	runtime, err := New(policy, capacity)
	if err != nil {
		return nil, RecoveryReport{}, err
	}
	runtime.checkpoint = store
	runtime.configFingerprint = configFingerprint
	runtime.recoveryHook = options.RecoveryHook

	payload, err := store.Load()
	if errors.Is(err, fs.ErrNotExist) {
		if err = runtime.persistCurrentLocked(now); err != nil {
			return nil, RecoveryReport{}, err
		}
		return runtime, RecoveryReport{}, nil
	}
	if err != nil {
		return nil, RecoveryReport{}, fmt.Errorf("%w: load: %v", ErrCheckpointUnavailable, err)
	}
	checkpoint, err := decodeCheckpoint(payload)
	migrating := false
	if err != nil {
		if !options.AllowLegacyV1Migration {
			return nil, RecoveryReport{}, err
		}
		legacy, legacyErr := decodeLegacyV1Checkpoint(payload)
		if legacyErr != nil {
			return nil, RecoveryReport{}, legacyErr
		}
		checkpoint = legacy.upgrade(runtime.instanceID)
		migrating = true
	}
	if checkpoint.ConfigFingerprint != configFingerprint {
		return nil, RecoveryReport{}, fmt.Errorf("%w: configuration fingerprint mismatch", ErrInvalidCheckpoint)
	}
	if err = runtime.restoreExactLocked(checkpoint); err != nil {
		return nil, RecoveryReport{}, err
	}
	report, err := runtime.normalizeRecoveredLocked(now)
	if err != nil {
		return nil, RecoveryReport{}, err
	}
	report.Loaded = true
	report.Migrated = migrating
	if report.Recovered || report.Migrated {
		if err = runtime.persistCurrentLocked(now); err != nil {
			return nil, RecoveryReport{}, err
		}
	}
	return runtime, report, nil
}

func decodeCheckpoint(payload []byte) (Checkpoint, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var checkpoint Checkpoint
	if err := decoder.Decode(&checkpoint); err != nil {
		return Checkpoint{}, fmt.Errorf("%w: decode: %v", ErrInvalidCheckpoint, err)
	}
	if err := ensureCheckpointEOF(decoder); err != nil {
		return Checkpoint{}, err
	}
	if checkpoint.FormatVersion != checkpointFormatVersion {
		return Checkpoint{}, fmt.Errorf("%w: unsupported format version %d", ErrInvalidCheckpoint, checkpoint.FormatVersion)
	}
	if err := validateRuntimeInstanceID(checkpoint.InstanceID); err != nil {
		return Checkpoint{}, fmt.Errorf("%w: instance_id: %v", ErrInvalidCheckpoint, err)
	}
	if checkpoint.Generation == 0 || checkpoint.SavedAt.IsZero() {
		return Checkpoint{}, fmt.Errorf("%w: generation and saved_at are required", ErrInvalidCheckpoint)
	}
	if strings.TrimSpace(checkpoint.ConfigFingerprint) == "" || strings.TrimSpace(checkpoint.ConfigFingerprint) != checkpoint.ConfigFingerprint {
		return Checkpoint{}, fmt.Errorf("%w: config_fingerprint is required", ErrInvalidCheckpoint)
	}
	return checkpoint, nil
}

func ensureCheckpointEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("%w: trailing JSON value", ErrInvalidCheckpoint)
		}
		return fmt.Errorf("%w: trailing data: %v", ErrInvalidCheckpoint, err)
	}
	return nil
}

func (r *Runtime) mutationSnapshotLocked(now time.Time) *Checkpoint {
	if r.checkpoint == nil {
		return nil
	}
	checkpoint := r.checkpointLocked(now)
	checkpoint.Generation = r.generation
	checkpoint.SavedAt = r.lastCheckpointAt
	return &checkpoint
}

func (r *Runtime) commitMutationLocked(before *Checkpoint, now time.Time) error {
	if r.checkpoint == nil {
		return nil
	}
	if err := r.persistCurrentLocked(now); err == nil {
		return nil
	} else {
		persistErr := err
		if isCommitUncertain(persistErr) {
			r.poisoned = persistErr
			return errors.Join(ErrCheckpointUnavailable, persistErr)
		}
		rollbackErr := r.restoreExactLocked(*before)
		r.poisoned = errors.Join(persistErr, rollbackErr)
		return errors.Join(ErrCheckpointUnavailable, persistErr, rollbackErr)
	}
}

func (r *Runtime) persistCurrentLocked(now time.Time) error {
	if r.generation == math.MaxUint64 {
		return errors.Join(ErrCheckpointUnavailable, errors.New("checkpoint generation exhausted"))
	}
	// Callers observe time before waiting for this Runtime's lock. Preserve
	// the latest durable observation when such calls arrive out of order.
	if now.Before(r.lastCheckpointAt) {
		now = r.lastCheckpointAt
	}
	checkpoint := r.checkpointLocked(now)
	checkpoint.Generation = r.generation + 1
	payload, err := json.Marshal(checkpoint)
	if err != nil {
		return fmt.Errorf("%w: encode: %v", ErrCheckpointUnavailable, err)
	}
	if err = r.checkpoint.Save(payload); err != nil {
		if isCommitUncertain(err) {
			r.generation = checkpoint.Generation
			r.lastCheckpointAt = checkpoint.SavedAt
		}
		return errors.Join(ErrCheckpointUnavailable, fmt.Errorf("save checkpoint: %w", err))
	}
	r.generation = checkpoint.Generation
	r.lastCheckpointAt = checkpoint.SavedAt
	return nil
}

type uncertainCommit interface {
	CommitUncertain() bool
}

func isCommitUncertain(err error) bool {
	var marker uncertainCommit
	return errors.As(err, &marker) && marker.CommitUncertain()
}

func (r *Runtime) checkpointLocked(now time.Time) Checkpoint {
	ready := make([]ReadyTimestamp, 0, len(r.readySince))
	for taskID, readyAt := range r.readySince {
		ready = append(ready, ReadyTimestamp{TaskID: taskID, ReadyAt: readyAt})
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i].TaskID < ready[j].TaskID })
	attempts := make([]AttemptCounter, 0, len(r.attempts))
	for taskID, number := range r.attempts {
		if number > 0 {
			attempts = append(attempts, AttemptCounter{TaskID: taskID, Number: number})
		}
	}
	sort.Slice(attempts, func(i, j int) bool { return attempts[i].TaskID < attempts[j].TaskID })
	cancelRequested := make([]string, 0, len(r.cancelRequested))
	for taskID := range r.cancelRequested {
		cancelRequested = append(cancelRequested, taskID)
	}
	sort.Strings(cancelRequested)
	return Checkpoint{
		FormatVersion: checkpointFormatVersion, InstanceID: r.instanceID,
		LegacyContextMigrationPending: r.legacyContextMigrationPending,
		SavedAt:                       now.UTC(),
		ConfigFingerprint:             r.configFingerprint, Graph: r.graph.State(),
		Scheduler: r.scheduler.State(), ReadySince: ready, Attempts: attempts,
		CancelRequested:    cancelRequested,
		PendingCompletions: r.pendingCompletionsLocked(),
	}
}

func (r *Runtime) ensureHealthyLocked() error {
	if r.poisoned == nil {
		return nil
	}
	return errors.Join(ErrRuntimeUnavailable, r.poisoned)
}
