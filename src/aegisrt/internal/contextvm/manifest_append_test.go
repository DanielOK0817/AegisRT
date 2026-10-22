package contextvm

import (
	"errors"
	"math"
	"testing"
)

func TestManifestAppendRoutesOwnedPagesAndPreservesLineage(t *testing.T) {
	manifest, err := NewManifest("workflow-1", "agent-1", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	workflowPage := mustPage(t, PageSpec{
		Content: "workflow context", Scope: ScopeWorkflow, WorkflowID: "workflow-1",
	})
	workflowRef := ContextRef{PageID: workflowPage.ID, Pin: true}
	shared, err := manifest.Append(workflowPage, workflowRef)
	if err != nil {
		t.Fatal(err)
	}
	if shared.ParentID != manifest.ID || shared.Revision != 2 || shared.ID != calculateManifestID(shared) {
		t.Fatalf("shared lineage is invalid: %#v", shared)
	}
	if len(shared.SharedPages) != 1 || shared.SharedPages[0] != workflowRef || len(shared.PrivatePages) != 0 {
		t.Fatalf("workflow page was not routed to shared pages: %#v", shared)
	}
	if len(manifest.Refs()) != 0 {
		t.Fatalf("Append mutated receiver: %#v", manifest)
	}

	agentPage := mustPage(t, PageSpec{
		Content: "agent context", Scope: ScopeAgent, WorkflowID: "workflow-1", AgentID: "agent-1",
	})
	agentRef := ContextRef{PageID: agentPage.ID, Cold: true}
	private, err := shared.Append(agentPage, agentRef)
	if err != nil {
		t.Fatal(err)
	}
	if private.ParentID != shared.ID || private.Revision != 3 || private.ID != calculateManifestID(private) {
		t.Fatalf("private lineage is invalid: %#v", private)
	}
	if len(private.SharedPages) != 1 || len(private.PrivatePages) != 1 || private.PrivatePages[0] != agentRef {
		t.Fatalf("agent page was not routed to private pages: %#v", private)
	}
	store := NewStore()
	mustPut(t, store, workflowPage)
	mustPut(t, store, agentPage)
	if err := private.Validate(store); err != nil {
		t.Fatalf("appended manifest does not validate: %v", err)
	}
	private.SharedPages[0].Pin = false
	if !shared.SharedPages[0].Pin {
		t.Fatal("Append result shares reference backing storage with its parent")
	}
}

func TestManifestAppendRejectsUnsupportedScopesAndWrongOwners(t *testing.T) {
	manifest, err := NewManifest("workflow-1", "agent-1", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		page Page
	}{
		{name: "system", page: mustPage(t, PageSpec{Content: "system", Scope: ScopeSystem})},
		{name: "ephemeral", page: mustPage(t, PageSpec{
			Content: "temporary", Scope: ScopeEphemeral, WorkflowID: "workflow-1", AgentID: "agent-1",
		})},
		{name: "other workflow", page: mustPage(t, PageSpec{
			Content: "workflow", Scope: ScopeWorkflow, WorkflowID: "workflow-2",
		})},
		{name: "other agent", page: mustPage(t, PageSpec{
			Content: "private", Scope: ScopeAgent, WorkflowID: "workflow-1", AgentID: "agent-2",
		})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := manifest.Append(test.page, ContextRef{PageID: test.page.ID}); !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("Append() error = %v, want ErrInvalidManifest", err)
			}
		})
	}
}

func TestManifestAppendRejectsDuplicateReferencesAndFlagConflicts(t *testing.T) {
	page := mustPage(t, PageSpec{
		Content: "workflow context", Scope: ScopeWorkflow, WorkflowID: "workflow-1",
	})
	ref := ContextRef{PageID: page.ID, Pin: true}
	manifest, err := NewManifest("workflow-1", "agent-1", []ContextRef{ref}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manifest.Append(page, ref); !errors.Is(err, ErrDuplicateReference) {
		t.Fatalf("duplicate error = %v, want ErrDuplicateReference", err)
	}
	if _, err = manifest.Append(page, ContextRef{PageID: page.ID, Cold: true}); !errors.Is(err, ErrReferenceFlagsConflict) {
		t.Fatalf("flags error = %v, want ErrReferenceFlagsConflict", err)
	}
	empty, err := NewManifest("workflow-1", "agent-1", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = empty.Append(page, ContextRef{PageID: page.ID, Pin: true, Cold: true}); !errors.Is(err, ErrReferenceFlagsConflict) {
		t.Fatalf("pin/cold conflict error = %v, want ErrReferenceFlagsConflict", err)
	}
	wrongRef := ContextRef{PageID: PageID("cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")}
	if _, err = manifest.Append(page, wrongRef); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("mismatched reference error = %v, want ErrInvalidReference", err)
	}
}

func TestManifestAppendRejectsRevisionOverflow(t *testing.T) {
	manifest, err := NewManifest("workflow-1", "agent-1", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Revision = math.MaxUint64
	manifest.ID = calculateManifestID(manifest)
	page := mustPage(t, PageSpec{
		Content: "workflow context", Scope: ScopeWorkflow, WorkflowID: "workflow-1",
	})
	if _, err = manifest.Append(page, ContextRef{PageID: page.ID}); !errors.Is(err, ErrManifestRevisionOverflow) {
		t.Fatalf("overflow error = %v, want ErrManifestRevisionOverflow", err)
	}
}
