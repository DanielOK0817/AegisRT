package runtimecore

import (
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"strconv"
	"sync"
	"testing"
	"time"

	"aegisrt/internal/domain"
)

func TestCheckpointGenerationsSerializeConcurrentMutations(t *testing.T) {
	store := &recordingCheckpointStore{}
	capacity := testCapacity()
	now := time.Unix(7_000, 0).UTC()
	runtime := openTestRuntime(t, store, capacity, now)

	const mutationCount = 24
	errorsChannel := make(chan error, mutationCount)
	stopReaders := make(chan struct{})
	var readers sync.WaitGroup
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stopReaders:
					return
				default:
					_ = runtime.Stats()
					_ = runtime.InstanceID()
					_ = runtime.LegacyContextMigrationPending()
				}
			}
		}()
	}

	var writers sync.WaitGroup
	for index := range mutationCount {
		writers.Add(1)
		go func(index int) {
			defer writers.Done()
			id := "concurrent-" + strconv.Itoa(index)
			if err := runtime.ApplyPatch([]domain.TaskSpec{task(id, "workflow", nil, 1, 10)}, now); err != nil {
				errorsChannel <- err
			}
		}(index)
	}
	writers.Wait()
	close(stopReaders)
	readers.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		t.Errorf("concurrent mutation: %v", err)
	}

	generations := store.Generations()
	if len(generations) != mutationCount+1 {
		t.Fatalf("saved generations = %v, want %d entries", generations, mutationCount+1)
	}
	for index, generation := range generations {
		if want := uint64(index + 1); generation != want {
			t.Fatalf("generation[%d] = %d, want %d", index, generation, want)
		}
	}
	if stats := runtime.Stats(); stats.TaskCount != mutationCount || stats.Generation != mutationCount+1 {
		t.Fatalf("final runtime stats = %+v", stats)
	}
}

func TestCheckpointGenerationExhaustionFailsClosed(t *testing.T) {
	store := &memoryCheckpointStore{}
	capacity := testCapacity()
	now := time.Unix(8_000, 0).UTC()
	_ = openTestRuntime(t, store, capacity, now)
	var checkpoint Checkpoint
	if err := json.Unmarshal(store.data, &checkpoint); err != nil {
		t.Fatal(err)
	}
	checkpoint.Generation = math.MaxUint64
	var err error
	store.data, err = json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	runtime, _ := reopenTestRuntime(t, store, capacity, now.Add(time.Second))
	err = runtime.ApplyPatch([]domain.TaskSpec{task("overflow", "workflow", nil, 1, 10)}, now)
	if !errors.Is(err, ErrCheckpointUnavailable) {
		t.Fatalf("ApplyPatch error = %v, want ErrCheckpointUnavailable", err)
	}
	stats := runtime.Stats()
	if stats.Generation != math.MaxUint64 || stats.TaskCount != 0 || runtime.Healthy() {
		t.Fatalf("runtime after generation exhaustion = %+v healthy=%v", stats, runtime.Healthy())
	}
}

type recordingCheckpointStore struct {
	mu          sync.Mutex
	data        []byte
	generations []uint64
}

func (s *recordingCheckpointStore) Load() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		return nil, fs.ErrNotExist
	}
	return append([]byte(nil), s.data...), nil
}

func (s *recordingCheckpointStore) Save(data []byte) error {
	var checkpoint Checkpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = append([]byte(nil), data...)
	s.generations = append(s.generations, checkpoint.Generation)
	return nil
}

func (s *recordingCheckpointStore) Generations() []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uint64(nil), s.generations...)
}
