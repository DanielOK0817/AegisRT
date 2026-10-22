package contextstate

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"
)

func TestCheckpointRejectsMissingManifestParent(t *testing.T) {
	store, config := populatedCheckpoint(t)
	stored := decodeStoredCheckpoint(t, store.payload)
	if len(stored.Manifests) != 2 {
		t.Fatalf("manifest count = %d, want 2", len(stored.Manifests))
	}
	for index, manifest := range stored.Manifests {
		if manifest.ParentID == "" {
			stored.Manifests = append(stored.Manifests[:index], stored.Manifests[index+1:]...)
			break
		}
	}
	store.payload = encodeStoredCheckpoint(t, stored)
	if _, err := NewPersistent(config, store, false, time.Now()); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("error = %v, want ErrInvalidCheckpoint", err)
	}
}

func TestCheckpointRejectsHeadAndIdempotencyDanglingReferences(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*checkpoint)
	}{
		{name: "head", mutate: func(stored *checkpoint) {
			stored.Heads[0].ManifestID = strings.Repeat("a", 64)
		}},
		{name: "idempotency", mutate: func(stored *checkpoint) {
			stored.Idempotency[0].Result.ManifestID = strings.Repeat("b", 64)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, config := populatedCheckpoint(t)
			stored := decodeStoredCheckpoint(t, store.payload)
			test.mutate(&stored)
			store.payload = encodeStoredCheckpoint(t, stored)
			if _, err := NewPersistent(config, store, false, time.Now()); !errors.Is(err, ErrInvalidCheckpoint) {
				t.Fatalf("error = %v, want ErrInvalidCheckpoint", err)
			}
		})
	}
}

func TestCheckpointRejectsFingerprintUnknownFieldsAndTrailingJSON(t *testing.T) {
	store, config := populatedCheckpoint(t)
	wrong := config
	wrong.Fingerprint = "different-context-v1"
	if _, err := NewPersistent(wrong, store, false, time.Now()); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("fingerprint error = %v, want ErrInvalidCheckpoint", err)
	}
	wrong = config
	wrong.RuntimeInstanceID = strings.Repeat("b", 64)
	if _, err := NewPersistent(wrong, store, false, time.Now()); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("Runtime binding error = %v, want ErrInvalidCheckpoint", err)
	}

	var document map[string]any
	if err := json.Unmarshal(store.payload, &document); err != nil {
		t.Fatal(err)
	}
	document["unexpected"] = true
	unknown, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	store.payload = unknown
	if _, err := NewPersistent(config, store, false, time.Now()); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("unknown-field error = %v, want ErrInvalidCheckpoint", err)
	}

	store, config = populatedCheckpoint(t)
	store.payload = append(store.payload, []byte(` {"trailing":true}`)...)
	if _, err := NewPersistent(config, store, false, time.Now()); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("trailing-data error = %v, want ErrInvalidCheckpoint", err)
	}
}

func TestCheckpointReappliesLimitsAndCanonicalSequence(t *testing.T) {
	t.Run("checkpoint bytes", func(t *testing.T) {
		store, config := populatedCheckpoint(t)
		config.Limits.MaxCheckpointBytes = len(store.payload) - 1
		if _, err := NewPersistent(config, store, false, time.Now()); !errors.Is(err, ErrInvalidCheckpoint) {
			t.Fatalf("error = %v, want ErrInvalidCheckpoint", err)
		}
	})
	t.Run("page bytes", func(t *testing.T) {
		store, config := populatedCheckpoint(t)
		config.Limits.MaxPageBytes = 4
		if _, err := NewPersistent(config, store, false, time.Now()); !errors.Is(err, ErrInvalidCheckpoint) {
			t.Fatalf("error = %v, want ErrInvalidCheckpoint", err)
		}
	})
	t.Run("manifest tokens", func(t *testing.T) {
		store, config := populatedCheckpoint(t)
		config.Limits.MaxManifestTokens = 1
		if _, err := NewPersistent(config, store, false, time.Now()); !errors.Is(err, ErrInvalidCheckpoint) {
			t.Fatalf("error = %v, want ErrInvalidCheckpoint", err)
		}
	})
	t.Run("sequence ahead", func(t *testing.T) {
		store, config := populatedCheckpoint(t)
		stored := decodeStoredCheckpoint(t, store.payload)
		stored.NextSequence++
		store.payload = encodeStoredCheckpoint(t, stored)
		if _, err := NewPersistent(config, store, false, time.Now()); !errors.Is(err, ErrInvalidCheckpoint) {
			t.Fatalf("error = %v, want ErrInvalidCheckpoint", err)
		}
	})
}

func populatedCheckpoint(t *testing.T) (*scriptedStore, Config) {
	t.Helper()
	store := &scriptedStore{loadErr: fs.ErrNotExist}
	config := testConfig()
	repository, err := NewPersistent(config, store, true, time.Unix(200, 0))
	if err != nil {
		t.Fatal(err)
	}
	manifest, _, err := repository.EnsureEmptyWithOwnership("workflow-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.Append(context.Background(), AppendCommand{
		TaskID: "task-1", WorkflowID: "workflow-1", AgentID: "agent-1",
		ExpectedManifestID: manifest.ID, IdempotencyKey: "checkpoint-append",
		Content: "checkpoint context", Visibility: VisibilityAgent,
		TaskTokenBudget: 16, PrincipalPinnedBudget: 16,
	}, time.Unix(201, 0))
	if err != nil {
		t.Fatal(err)
	}
	return store, config
}

func decodeStoredCheckpoint(t *testing.T, payload []byte) checkpoint {
	t.Helper()
	stored, err := decodeCheckpoint(payload)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func encodeStoredCheckpoint(t *testing.T, stored checkpoint) []byte {
	t.Helper()
	payload, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
