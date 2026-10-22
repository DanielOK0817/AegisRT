package contextvm

import "testing"

func TestStoreClonePreservesMetricsWithoutSharingMutations(t *testing.T) {
	original := NewStore()
	page, err := NewPage(PageSpec{Content: "source", Scope: ScopeSystem}, WhitespaceCounter{})
	if err != nil {
		t.Fatal(err)
	}
	ref, _, err := original.Put(page)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := original.Get(ref, AccessRequest{WorkflowID: "w", AgentID: "a"}); err != nil {
		t.Fatal(err)
	}
	before := original.Stats()
	clone := original.Clone()
	if clone.Stats() != before {
		t.Fatal("clone lost transient counters")
	}
	if _, _, err := clone.Put(page); err != nil {
		t.Fatal(err)
	}
	summary, err := NewPage(PageSpec{Content: "derived", Scope: ScopeSystem, SourceRefs: []ContextRef{ref}}, WhitespaceCounter{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := clone.Put(summary); err != nil {
		t.Fatal(err)
	}
	if original.Stats() != before {
		t.Fatal("candidate changed original counters")
	}
	if _, err := original.Lookup(summary.ID); err == nil {
		t.Fatal("candidate page leaked into original")
	}
	if len(clone.State().Pages) != 2 {
		t.Fatal("candidate lost its new page")
	}
}
