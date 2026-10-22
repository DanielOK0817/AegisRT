package contextstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"sync"
	"time"

	"aegisrt/internal/contextdispatch"
	"aegisrt/internal/contextvm"
	"aegisrt/internal/domain"
)

type Repository struct {
	mu         sync.RWMutex
	config     Config
	checkpoint CheckpointStore
	components
	generation       uint64
	lastCheckpointAt time.Time
	poisoned         error
}

func New(config Config) (*Repository, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	value, err := buildComponents(config, checkpoint{})
	if err != nil {
		return nil, err
	}
	return &Repository{config: config, components: value}, nil
}

func NewPersistent(config Config, store CheckpointStore, allowCreate bool, now time.Time) (*Repository, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	if err := validatePersistentIdentities(config); err != nil {
		return nil, err
	}
	if store == nil {
		return nil, errors.New("context checkpoint store is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	payload, err := store.Load()
	if errors.Is(err, fs.ErrNotExist) {
		if !allowCreate {
			return nil, errors.Join(ErrMigrationRequired, err)
		}
		value, buildErr := buildComponents(config, checkpoint{})
		if buildErr != nil {
			return nil, buildErr
		}
		repository := &Repository{config: config, checkpoint: store, components: value}
		if commitErr := repository.commitLocked(value, now); commitErr != nil {
			return nil, commitErr
		}
		return repository, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: load context checkpoint: %w", ErrUnavailable, err)
	}
	if len(payload) > config.Limits.MaxCheckpointBytes {
		return nil, fmt.Errorf("%w: checkpoint exceeds configured byte limit", ErrInvalidCheckpoint)
	}
	stored, err := decodeCheckpoint(payload)
	if err != nil {
		return nil, err
	}
	counterIdentity, summarizerIdentity := componentIdentities(config)
	if stored.CounterIdentity == "" || stored.SummarizerIdentity == "" {
		return nil, fmt.Errorf("%w: missing persisted counter or summarizer identity", ErrMigrationRequired)
	}
	if stored.CounterIdentity != counterIdentity || stored.SummarizerIdentity != summarizerIdentity {
		return nil, fmt.Errorf("%w: counter or summarizer identity mismatch", ErrInvalidCheckpoint)
	}
	if stored.ConfigFingerprint != config.Fingerprint {
		return nil, fmt.Errorf("%w: configuration fingerprint mismatch", ErrInvalidCheckpoint)
	}
	if stored.RuntimeInstanceID != config.RuntimeInstanceID {
		return nil, fmt.Errorf("%w: Runtime instance binding mismatch", ErrInvalidCheckpoint)
	}
	value, err := buildComponents(config, stored)
	if err != nil {
		return nil, err
	}
	return &Repository{config: config, checkpoint: store, components: value,
		generation: stored.Generation, lastCheckpointAt: stored.SavedAt}, nil
}

func (r *Repository) EnsureEmptyWithOwnership(workflowID, agentID string) (contextvm.Manifest, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureHealthyLocked(); err != nil {
		return contextvm.Manifest{}, false, err
	}
	if current, err := r.registry.Get(workflowID, agentID); err == nil {
		return current, false, nil
	} else if !errors.Is(err, contextdispatch.ErrManifestNotFound) {
		return contextvm.Manifest{}, false, err
	}
	candidate, err := r.cloneComponentsLocked()
	if err != nil {
		return contextvm.Manifest{}, false, r.poisonLocked(err)
	}
	manifest, err := contextvm.NewManifest(workflowID, agentID, nil, nil)
	if err != nil {
		return contextvm.Manifest{}, false, err
	}
	if err := candidate.registry.Set(manifest); err != nil {
		return contextvm.Manifest{}, false, err
	}
	key := principal{workflowID: workflowID, agentID: agentID}
	candidate.history[manifest.ID] = cloneManifest(manifest)
	candidate.heads[key] = manifest.ID
	return manifest, true, r.commitLocked(candidate, time.Now().UTC())
}

func (r *Repository) DeleteIfID(workflowID, agentID, manifestID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureHealthyLocked(); err != nil {
		return false, err
	}
	key := principal{workflowID: workflowID, agentID: agentID}
	if r.heads[key] != manifestID {
		return false, nil
	}
	candidate, err := r.cloneComponentsLocked()
	if err != nil {
		return false, r.poisonLocked(err)
	}
	if !isPristineRollbackManifest(candidate, key, manifestID) {
		return false, nil
	}
	deleted, err := candidate.registry.DeleteIfID(workflowID, agentID, manifestID)
	if err != nil || !deleted {
		return deleted, err
	}
	delete(candidate.heads, key)
	delete(candidate.history, manifestID)
	return true, r.commitLocked(candidate, time.Now().UTC())
}

func isPristineRollbackManifest(value components, key principal, manifestID string) bool {
	manifest, exists := value.history[manifestID]
	if !exists || manifest.WorkflowID != key.workflowID || manifest.AgentID != key.agentID ||
		manifest.ParentID != "" || manifest.Revision != 1 || len(manifest.Refs()) != 0 {
		return false
	}
	for _, candidate := range value.history {
		if candidate.ParentID == manifestID {
			return false
		}
	}
	for _, record := range value.ledger {
		if record.Result.ManifestID == manifestID {
			return false
		}
	}
	return true
}

func (r *Repository) Get(workflowID, agentID string) (contextvm.Manifest, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := r.ensureHealthyLocked(); err != nil {
		return contextvm.Manifest{}, err
	}
	return r.registry.Get(workflowID, agentID)
}

func (r *Repository) Resolve(ctx context.Context, task domain.TaskSpec, agent domain.AgentSpec, budget int) (contextdispatch.Resolution, error) {
	if err := contextdispatch.ValidateResolutionRequest(ctx, task, agent, budget); err != nil {
		return contextdispatch.Resolution{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureHealthyLocked(); err != nil {
		return contextdispatch.Resolution{}, err
	}
	artifacts, err := r.dependencyArtifacts(task)
	if err != nil {
		return contextdispatch.Resolution{}, err
	}
	if len(artifacts) > 0 && budget <= 0 {
		return contextdispatch.Resolution{}, ErrTaskContextDisabled
	}
	if r.config.Summarizer == nil {
		return resolveTaskInputs(ctx, r.components, task, agent, budget)
	}
	candidate, err := r.cloneComponentsLocked()
	if err != nil {
		return contextdispatch.Resolution{}, r.poisonLocked(err)
	}
	resolution, resolveErr := resolveTaskInputs(ctx, candidate, task, agent, budget)
	if resolveErr != nil {
		return contextdispatch.Resolution{}, resolveErr
	}
	if err := ctx.Err(); err != nil {
		return contextdispatch.Resolution{}, err
	}
	if len(candidate.summaries) != len(r.summaries) {
		if err := r.commitLocked(candidate, time.Now().UTC()); err != nil {
			return contextdispatch.Resolution{}, err
		}
	} else {
		// Successful reads retain transient access metrics without writing an
		// unchanged durable snapshot or advancing its generation.
		r.components = candidate
	}
	return resolution, nil
}

func (r *Repository) Healthy() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.poisoned == nil
}

func (r *Repository) Stats() Stats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	artifactCount := 0
	for _, output := range r.outputs {
		artifactCount += len(output.Artifacts)
	}
	return Stats{Durable: r.checkpoint != nil, CheckpointHealthy: r.poisoned == nil,
		CounterIdentity: r.counterIdentity, SummarizerIdentity: r.summarizerIdentity, SummaryCount: len(r.summaries),
		Generation: r.generation, LastCheckpointAt: r.lastCheckpointAt,
		ManifestCount: len(r.history), HeadCount: len(r.heads), LedgerEntries: len(r.ledger),
		OutputCount: len(r.outputs), ArtifactCount: artifactCount,
		Store: r.store.Stats()}
}

func (r *Repository) cloneComponentsLocked() (components, error) {
	stored := checkpointFromComponents(
		r.components, r.config.Fingerprint, r.config.RuntimeInstanceID, r.generation, r.lastCheckpointAt,
	)
	return buildComponentsWithStore(r.config, stored, r.store.Clone())
}

func (r *Repository) commitLocked(candidate components, now time.Time) error {
	if r.checkpoint == nil {
		r.components = candidate
		return nil
	}
	if r.generation == math.MaxUint64 {
		return r.poisonLocked(errors.New("context checkpoint generation exhausted"))
	}
	stored := checkpointFromComponents(
		candidate, r.config.Fingerprint, r.config.RuntimeInstanceID, r.generation+1, now,
	)
	payload, err := json.Marshal(stored)
	if err != nil {
		return r.poisonLocked(fmt.Errorf("encode context checkpoint: %w", err))
	}
	if len(payload) > r.config.Limits.MaxCheckpointBytes {
		return fmt.Errorf("%w: encoded checkpoint is %d bytes", ErrCheckpointCapacity, len(payload))
	}
	if err = r.checkpoint.Save(payload); err != nil {
		if isCommitUncertain(err) {
			r.components = candidate
			r.generation = stored.Generation
			r.lastCheckpointAt = stored.SavedAt
		}
		return r.poisonLocked(fmt.Errorf("save context checkpoint: %w", err))
	}
	r.components = candidate
	r.generation = stored.Generation
	r.lastCheckpointAt = stored.SavedAt
	return nil
}

func (r *Repository) ensureHealthyLocked() error {
	if r == nil || r.poisoned != nil {
		if r == nil {
			return ErrUnavailable
		}
		return errors.Join(ErrUnavailable, r.poisoned)
	}
	return nil
}

func (r *Repository) poisonLocked(cause error) error {
	r.poisoned = cause
	return errors.Join(ErrUnavailable, cause)
}

type uncertainCommit interface{ CommitUncertain() bool }

func isCommitUncertain(err error) bool {
	var marker uncertainCommit
	return errors.As(err, &marker) && marker.CommitUncertain()
}
