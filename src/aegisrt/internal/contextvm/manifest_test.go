package contextvm

import (
	"errors"
	"reflect"
	"testing"
)

func TestManifestScopeAndACLValidation(t *testing.T) {
	store := NewStore()
	system := mustPut(t, store, mustPage(t, PageSpec{Content: "system", Scope: ScopeSystem}))
	workflow := mustPut(t, store, mustPage(t, PageSpec{Content: "workflow", Scope: ScopeWorkflow, WorkflowID: "workflow-1"}))
	agent1 := mustPut(t, store, mustPage(t, PageSpec{Content: "private one", Scope: ScopeAgent, WorkflowID: "workflow-1", AgentID: "agent-1"}))
	agent2 := mustPut(t, store, mustPage(t, PageSpec{Content: "private two", Scope: ScopeAgent, WorkflowID: "workflow-1", AgentID: "agent-2"}))
	otherWorkflow := mustPut(t, store, mustPage(t, PageSpec{Content: "other", Scope: ScopeWorkflow, WorkflowID: "workflow-2"}))

	tests := []struct {
		name    string
		shared  []ContextRef
		private []ContextRef
		valid   bool
	}{
		{name: "valid", shared: []ContextRef{system, workflow}, private: []ContextRef{agent1}, valid: true},
		{name: "private in shared section", shared: []ContextRef{agent1}},
		{name: "shared in private section", private: []ContextRef{workflow}},
		{name: "cross agent private", private: []ContextRef{agent2}},
		{name: "cross workflow shared", shared: []ContextRef{otherWorkflow}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest, err := NewManifest("workflow-1", "agent-1", test.shared, test.private)
			if err != nil {
				t.Fatal(err)
			}
			err = manifest.Validate(store)
			if test.valid && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if !test.valid && (!errors.Is(err, ErrInvalidManifest) || !errors.Is(err, ErrAccessDenied)) {
				t.Fatalf("Validate() error = %v, want manifest/access errors", err)
			}
		})
	}
}

func TestManifestCopyOnWritePreservesOriginal(t *testing.T) {
	store := NewStore()
	sharedPage := mustPage(t, PageSpec{Content: "shared base", Scope: ScopeWorkflow, WorkflowID: "workflow-1"})
	sharedRef := mustPut(t, store, sharedPage)
	sharedRef.Pin = true
	privateRef := mustPut(t, store, mustPage(t, PageSpec{Content: "existing private", Scope: ScopeAgent, WorkflowID: "workflow-1", AgentID: "agent-1"}))
	original, err := NewManifest("workflow-1", "agent-1", []ContextRef{sharedRef}, []ContextRef{privateRef})
	if err != nil {
		t.Fatal(err)
	}
	before := original
	before.SharedPages = cloneRefs(original.SharedPages)
	before.PrivatePages = cloneRefs(original.PrivatePages)

	replacement := mustPage(t, PageSpec{
		Content:    "agent modified copy",
		Scope:      ScopeAgent,
		WorkflowID: "workflow-1",
		AgentID:    "agent-1",
		SourceRefs: []ContextRef{{PageID: sharedPage.ID}},
	})
	next, err := original.CopyOnWrite(sharedPage.ID, replacement)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, before) {
		t.Fatalf("CopyOnWrite mutated original:\n got %#v\nwant %#v", original, before)
	}
	if next.ParentID != original.ID || next.Revision != original.Revision+1 || next.ID == original.ID {
		t.Fatalf("COW lineage is incorrect: %#v", next)
	}
	if len(next.SharedPages) != 0 || len(next.PrivatePages) != 2 {
		t.Fatalf("COW page sets = shared %v private %v", next.SharedPages, next.PrivatePages)
	}
	newRef := next.PrivatePages[1]
	if newRef.PageID != replacement.ID || !newRef.Pin || newRef.Cold {
		t.Fatalf("replacement ref = %#v", newRef)
	}
	if _, _, err := store.Put(replacement); err != nil {
		t.Fatal(err)
	}
	if err := next.Validate(store); err != nil {
		t.Fatalf("new manifest Validate() error = %v", err)
	}
	storedBase, err := store.Get(sharedRef, AccessRequest{WorkflowID: "workflow-1", AgentID: "agent-2"})
	if err != nil || storedBase.Content != "shared base" {
		t.Fatalf("shared base changed: page=%#v error=%v", storedBase, err)
	}
}

func TestManifestCopyOnWriteRejectsInvalidReplacement(t *testing.T) {
	shared := mustPage(t, PageSpec{Content: "base", Scope: ScopeWorkflow, WorkflowID: "workflow-1"})
	manifest, err := NewManifest("workflow-1", "agent-1", []ContextRef{{PageID: shared.ID}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	validReplacement := mustPage(t, PageSpec{Content: "copy", Scope: ScopeAgent, WorkflowID: "workflow-1", AgentID: "agent-1", SourceRefs: []ContextRef{{PageID: shared.ID}}})
	wrongScope := mustPage(t, PageSpec{Content: "copy", Scope: ScopeWorkflow, WorkflowID: "workflow-1", SourceRefs: []ContextRef{{PageID: shared.ID}}})
	wrongAgent := mustPage(t, PageSpec{Content: "copy", Scope: ScopeAgent, WorkflowID: "workflow-1", AgentID: "agent-2", SourceRefs: []ContextRef{{PageID: shared.ID}}})
	noLineage := mustPage(t, PageSpec{Content: "copy", Scope: ScopeAgent, WorkflowID: "workflow-1", AgentID: "agent-1"})
	tests := []struct {
		name   string
		source PageID
		page   Page
		want   error
	}{
		{name: "source absent", source: PageID(validReplacement.ContentHash), page: validReplacement, want: ErrCOWSourceNotFound},
		{name: "wrong scope", source: shared.ID, page: wrongScope, want: ErrInvalidCOWPage},
		{name: "wrong agent", source: shared.ID, page: wrongAgent, want: ErrInvalidCOWPage},
		{name: "missing lineage", source: shared.ID, page: noLineage, want: ErrInvalidCOWPage},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := manifest.CopyOnWrite(test.source, test.page); !errors.Is(err, test.want) {
				t.Fatalf("CopyOnWrite() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestManifestOwnsInputAndOutputSlices(t *testing.T) {
	page := mustPage(t, PageSpec{Content: "shared", Scope: ScopeSystem})
	input := []ContextRef{{PageID: page.ID, Pin: true}}
	manifest, err := NewManifest("workflow-1", "agent-1", input, nil)
	if err != nil {
		t.Fatal(err)
	}
	input[0].Pin = false
	refs := manifest.Refs()
	refs[0].Pin = false
	if !manifest.SharedPages[0].Pin {
		t.Fatal("manifest aliases caller-owned reference slices")
	}
	tampered := manifest
	tampered.AgentID = "agent-2"
	if err := tampered.Validate(NewStore()); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("tampered Validate() error = %v", err)
	}
}
