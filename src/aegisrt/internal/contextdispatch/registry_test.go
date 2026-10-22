package contextdispatch

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"aegisrt/internal/contextvm"
)

func TestRegistrySetGetDeepCopies(t *testing.T) {
	registry := NewRegistry()
	manifest := mustManifest(t, "workflow-1", "agent-1",
		[]contextvm.ContextRef{{PageID: testPageID(1), Pin: true}},
		[]contextvm.ContextRef{{PageID: testPageID(2), Cold: true}},
	)
	if err := registry.Set(manifest); err != nil {
		t.Fatal(err)
	}

	manifest.SharedPages[0].Pin = false
	manifest.PrivatePages[0].Cold = false
	got, err := registry.Get("workflow-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.SharedPages[0].Pin || !got.PrivatePages[0].Cold {
		t.Fatalf("Set retained caller-owned slices: %#v", got)
	}

	got.SharedPages[0].Pin = false
	again, err := registry.Get("workflow-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if !again.SharedPages[0].Pin {
		t.Fatal("Get exposed the registry's backing slice")
	}
}

func TestRegistryReturnsExplicitMissingError(t *testing.T) {
	_, err := NewRegistry().Get("workflow-1", "agent-1")
	if !errors.Is(err, ErrManifestNotFound) {
		t.Fatalf("Get error = %v, want ErrManifestNotFound", err)
	}
}

func TestRegistryEnsureEmptyIsAtomicAndPreservesExisting(t *testing.T) {
	registry := NewRegistry()
	empty, err := registry.EnsureEmpty("workflow-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Refs()) != 0 {
		t.Fatalf("empty manifest has refs: %#v", empty)
	}
	existing := mustManifest(t, "workflow-1", "agent-1",
		[]contextvm.ContextRef{{PageID: testPageID(7)}}, nil)
	if err := registry.Set(existing); err != nil {
		t.Fatal(err)
	}
	got, err := registry.EnsureEmpty("workflow-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != existing.ID || len(got.SharedPages) != 1 {
		t.Fatalf("EnsureEmpty replaced existing manifest: %#v", got)
	}
}

func TestRegistryConcurrentEnsureEmptyReturnsOneManifest(t *testing.T) {
	registry := NewRegistry()
	const workers = 64
	ids := make(chan string, workers)
	errs := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			manifest, err := registry.EnsureEmpty("workflow-1", "agent-1")
			if err != nil {
				errs <- err
				return
			}
			ids <- manifest.ID
		}()
	}
	wait.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	want := ""
	for id := range ids {
		if want == "" {
			want = id
		}
		if id != want {
			t.Fatalf("EnsureEmpty returned different IDs: %q and %q", want, id)
		}
	}
}

func TestRegistryValidatesEntriesAndSupportsZeroValue(t *testing.T) {
	registry := &Registry{}
	manifest := mustManifest(t, "workflow-1", "agent-1", nil, nil)
	if err := registry.Set(manifest); err != nil {
		t.Fatalf("zero-value Registry.Set() error = %v", err)
	}
	if _, err := registry.Get("workflow-1", "agent-1"); err != nil {
		t.Fatalf("zero-value Registry.Get() error = %v", err)
	}
	if err := registry.Set(contextvm.Manifest{}); !errors.Is(err, ErrInvalidRegistryManifest) {
		t.Fatalf("invalid Set error = %v, want ErrInvalidRegistryManifest", err)
	}
	if _, err := registry.Get("bad workflow", "agent-1"); !errors.Is(err, ErrInvalidRegistryKey) {
		t.Fatalf("invalid Get error = %v, want ErrInvalidRegistryKey", err)
	}

	var unavailable *Registry
	if err := unavailable.Set(manifest); !errors.Is(err, ErrRegistryUnavailable) {
		t.Fatalf("nil Set error = %v, want ErrRegistryUnavailable", err)
	}
	if _, err := unavailable.Get("workflow-1", "agent-1"); !errors.Is(err, ErrRegistryUnavailable) {
		t.Fatalf("nil Get error = %v, want ErrRegistryUnavailable", err)
	}
}

func TestRegistryConcurrentSetGet(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Set(mustManifest(t, "workflow-1", "agent-1", nil, nil)); err != nil {
		t.Fatal(err)
	}

	const workers = 64
	errs := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			manifest, err := contextvm.NewManifest("workflow-1", "agent-1",
				[]contextvm.ContextRef{{PageID: testPageID(index + 1)}}, nil)
			if err != nil {
				errs <- err
				return
			}
			if err := registry.Set(manifest); err != nil {
				errs <- err
				return
			}
			got, err := registry.Get("workflow-1", "agent-1")
			if err != nil {
				errs <- err
				return
			}
			if got.WorkflowID != "workflow-1" || got.AgentID != "agent-1" {
				errs <- fmt.Errorf("unexpected principal: %#v", got)
				return
			}
			got.SharedPages = nil
		}(index)
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if _, err := registry.Get("workflow-1", "agent-1"); err != nil {
		t.Fatal(err)
	}
}

func mustManifest(t *testing.T, workflowID, agentID string, shared, private []contextvm.ContextRef) contextvm.Manifest {
	t.Helper()
	manifest, err := contextvm.NewManifest(workflowID, agentID, shared, private)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func testPageID(value int) contextvm.PageID {
	return contextvm.PageID(fmt.Sprintf("%064x", value))
}
