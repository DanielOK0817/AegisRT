package contextstate

import (
	"context"
	"io/fs"
	"time"
)

// ValidateOutput exercises the publication constraints on an isolated copy.
// It never advances manifests, stores content, or writes a checkpoint.
func (r *Repository) ValidateOutput(ctx context.Context, command OutputCommand) error {
	r.mu.RLock()
	if err := r.ensureHealthyLocked(); err != nil {
		r.mu.RUnlock()
		return err
	}
	value, err := r.cloneComponentsLocked()
	preview := &Repository{
		config: r.config, components: value,
		generation: r.generation, lastCheckpointAt: r.lastCheckpointAt,
	}
	if r.checkpoint != nil {
		preview.checkpoint = outputValidationStore{}
	}
	r.mu.RUnlock()
	if err != nil {
		return err
	}
	_, err = preview.PublishOutput(ctx, command, time.Now().UTC())
	return err
}

// The isolated preview must execute durable serialization and capacity checks
// without writing to the live checkpoint store.
type outputValidationStore struct{}

func (outputValidationStore) Load() ([]byte, error) { return nil, fs.ErrNotExist }
func (outputValidationStore) Save([]byte) error     { return nil }
