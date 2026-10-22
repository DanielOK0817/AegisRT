package contextvm

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
)

func mustPage(t *testing.T, spec PageSpec) Page {
	t.Helper()
	page, err := NewPage(spec, WhitespaceCounter{})
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func mustPut(t *testing.T, store *Store, page Page) ContextRef {
	t.Helper()
	ref, _, err := store.Put(page)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestNewPageContentAddressAndValidation(t *testing.T) {
	spec := PageSpec{Content: "immutable shared content", Scope: ScopeWorkflow, WorkflowID: "workflow-1"}
	first := mustPage(t, spec)
	second := mustPage(t, spec)
	if first.ID != second.ID {
		t.Fatalf("same immutable page has IDs %s and %s", first.ID, second.ID)
	}
	sum := sha256.Sum256([]byte(spec.Content))
	if first.ContentHash != hex.EncodeToString(sum[:]) {
		t.Fatalf("ContentHash = %s, want SHA-256(body)", first.ContentHash)
	}
	agentPage := mustPage(t, PageSpec{Content: spec.Content, Scope: ScopeAgent, WorkflowID: "workflow-1", AgentID: "agent-1"})
	if first.ID == agentPage.ID {
		t.Fatal("security-distinct pages must not share an object ID")
	}

	tampered := first
	tampered.Content = "changed"
	if err := tampered.Validate(); !errors.Is(err, ErrInvalidPage) {
		t.Fatalf("tampered Validate() error = %v, want ErrInvalidPage", err)
	}
}

func TestNewPageRejectsInvalidSpecs(t *testing.T) {
	validSource := ContextRef{PageID: PageID(fmt.Sprintf("%064x", 1))}
	tests := []struct {
		name string
		spec PageSpec
	}{
		{name: "blank content", spec: PageSpec{Content: " ", Scope: ScopeSystem}},
		{name: "unknown scope", spec: PageSpec{Content: "x", Scope: Scope("PUBLIC")}},
		{name: "owned system page", spec: PageSpec{Content: "x", Scope: ScopeSystem, WorkflowID: "workflow-1"}},
		{name: "workflow missing owner", spec: PageSpec{Content: "x", Scope: ScopeWorkflow}},
		{name: "workflow has agent", spec: PageSpec{Content: "x", Scope: ScopeWorkflow, WorkflowID: "workflow-1", AgentID: "agent-1"}},
		{name: "agent missing owner", spec: PageSpec{Content: "x", Scope: ScopeAgent, WorkflowID: "workflow-1"}},
		{name: "bad source ref", spec: PageSpec{Content: "x", Scope: ScopeSystem, SourceRefs: []ContextRef{{PageID: "bad"}}}},
		{name: "duplicate source ref", spec: PageSpec{Content: "x", Scope: ScopeSystem, SourceRefs: []ContextRef{validSource, validSource}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewPage(test.spec, WhitespaceCounter{}); !errors.Is(err, ErrInvalidPage) {
				t.Fatalf("NewPage() error = %v, want ErrInvalidPage", err)
			}
		})
	}
	if _, err := NewPage(PageSpec{Content: "x", Scope: ScopeSystem}, nil); !errors.Is(err, ErrInvalidPage) {
		t.Fatalf("nil counter error = %v, want ErrInvalidPage", err)
	}
}

func TestStoreExactDedupAndImmutableGet(t *testing.T) {
	store := NewStore()
	source := ContextRef{PageID: PageID(fmt.Sprintf("%064x", 7))}
	page := mustPage(t, PageSpec{Content: "one two three", Scope: ScopeAgent, WorkflowID: "workflow-1", AgentID: "agent-1", SourceRefs: []ContextRef{source}})
	ref, reused, err := store.Put(page)
	if err != nil || reused {
		t.Fatalf("first Put() = (%v, %v, %v)", ref, reused, err)
	}
	if _, reused, err = store.Put(page); err != nil || !reused {
		t.Fatalf("second Put() reused = %v, error = %v", reused, err)
	}
	stats := store.Stats()
	want := StoreStats{PutCalls: 2, UniquePages: 1, UniqueBlobs: 1, ReusedPuts: 1, LogicalBytes: uint64(2 * len(page.Content)), PhysicalBytes: uint64(len(page.Content)), LogicalTokens: 6, PhysicalTokens: 3, ReusedTokens: 3}
	if !reflect.DeepEqual(stats, want) {
		t.Fatalf("Stats() = %#v, want %#v", stats, want)
	}

	access := AccessRequest{WorkflowID: "workflow-1", AgentID: "agent-1"}
	got, err := store.Get(ref, access)
	if err != nil {
		t.Fatal(err)
	}
	got.SourceRefs[0].PageID = PageID(fmt.Sprintf("%064x", 9))
	again, err := store.Get(ref, access)
	if err != nil {
		t.Fatal(err)
	}
	if again.SourceRefs[0] != source || page.SourceRefs[0] != source {
		t.Fatal("Store.Get exposed mutable SourceRefs")
	}
}

func TestStoreDeduplicatesContentWithoutMergingACL(t *testing.T) {
	store := NewStore()
	system := mustPage(t, PageSpec{Content: "same exact content", Scope: ScopeSystem})
	private := mustPage(t, PageSpec{Content: "same exact content", Scope: ScopeAgent, WorkflowID: "workflow-1", AgentID: "agent-1"})
	systemRef := mustPut(t, store, system)
	privateRef := mustPut(t, store, private)
	stats := store.Stats()
	if stats.UniquePages != 2 || stats.UniqueBlobs != 1 || stats.ReusedPuts != 1 ||
		stats.LogicalTokens != 6 || stats.PhysicalTokens != 3 || stats.ReusedTokens != 3 ||
		stats.LogicalBytes != uint64(2*len(system.Content)) || stats.PhysicalBytes != uint64(len(system.Content)) {
		t.Fatalf("cross-scope dedup Stats() = %#v", stats)
	}
	otherAgent := AccessRequest{WorkflowID: "workflow-1", AgentID: "agent-2"}
	if _, err := store.Get(systemRef, otherAgent); err != nil {
		t.Fatalf("system page should remain public: %v", err)
	}
	if _, err := store.Get(privateRef, otherAgent); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("private ACL was merged during dedup: %v", err)
	}
}

func TestStoreACL(t *testing.T) {
	store := NewStore()
	pages := map[string]Page{
		"system":    mustPage(t, PageSpec{Content: "system", Scope: ScopeSystem}),
		"workflow":  mustPage(t, PageSpec{Content: "workflow", Scope: ScopeWorkflow, WorkflowID: "workflow-1"}),
		"agent":     mustPage(t, PageSpec{Content: "agent", Scope: ScopeAgent, WorkflowID: "workflow-1", AgentID: "agent-1"}),
		"ephemeral": mustPage(t, PageSpec{Content: "ephemeral", Scope: ScopeEphemeral, WorkflowID: "workflow-1", AgentID: "agent-1"}),
	}
	refs := make(map[string]ContextRef, len(pages))
	for name, page := range pages {
		refs[name] = mustPut(t, store, page)
	}
	tests := []struct {
		name    string
		page    string
		access  AccessRequest
		allowed bool
	}{
		{name: "system across workflow", page: "system", access: AccessRequest{WorkflowID: "workflow-2", AgentID: "agent-2"}, allowed: true},
		{name: "workflow owner", page: "workflow", access: AccessRequest{WorkflowID: "workflow-1", AgentID: "agent-2"}, allowed: true},
		{name: "workflow isolation", page: "workflow", access: AccessRequest{WorkflowID: "workflow-2", AgentID: "agent-1"}},
		{name: "agent owner", page: "agent", access: AccessRequest{WorkflowID: "workflow-1", AgentID: "agent-1"}, allowed: true},
		{name: "agent isolation", page: "agent", access: AccessRequest{WorkflowID: "workflow-1", AgentID: "agent-2"}},
		{name: "agent cross workflow", page: "agent", access: AccessRequest{WorkflowID: "workflow-2", AgentID: "agent-1"}},
		{name: "ephemeral isolation", page: "ephemeral", access: AccessRequest{WorkflowID: "workflow-1", AgentID: "agent-2"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := store.Get(refs[test.page], test.access)
			if test.allowed && err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			if !test.allowed && !errors.Is(err, ErrAccessDenied) {
				t.Fatalf("Get() error = %v, want ErrAccessDenied", err)
			}
		})
	}
}

func TestStoreConcurrentDedup(t *testing.T) {
	store := NewStore()
	page := mustPage(t, PageSpec{Content: "concurrent immutable page", Scope: ScopeWorkflow, WorkflowID: "workflow-1"})
	access := AccessRequest{WorkflowID: "workflow-1", AgentID: "agent-1"}
	const workers = 64
	errs := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			ref, _, err := store.Put(page)
			if err == nil {
				_, err = store.Get(ref, access)
			}
			if err != nil {
				errs <- err
			}
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	stats := store.Stats()
	if stats.PutCalls != workers || stats.UniquePages != 1 || stats.UniqueBlobs != 1 || stats.ReusedPuts != workers-1 || stats.GetCalls != workers {
		t.Fatalf("concurrent Stats() = %#v", stats)
	}
}
