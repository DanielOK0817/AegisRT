package contextstate

import (
	"context"
	"fmt"
	"sort"

	"aegisrt/internal/contextdispatch"
	"aegisrt/internal/contextvm"
	"aegisrt/internal/domain"
)

type taskInputKey struct {
	taskID, workflowID, agentID string
}

// Task ownership is durable and independent of the bounded replay ledger.
// Shared storage or membership in an Agent's head alone grants no task access.
type taskInputBinding struct {
	TaskID     string                 `json:"task_id"`
	WorkflowID string                 `json:"workflow_id"`
	AgentID    string                 `json:"agent_id"`
	Refs       []contextvm.ContextRef `json:"refs"`
}

func applyInputCandidate(value *components, command AppendCommand, fingerprint string, config Config) (AppendResult, bool, error) {
	result, changed, err := applyAppendCandidate(value, command, fingerprint, config)
	if err != nil {
		return result, false, err
	}
	key := taskInputKey{command.TaskID, command.WorkflowID, command.AgentID}
	binding := value.inputs[key]
	for _, ref := range binding.Refs {
		if ref.PageID == result.PageID {
			return result, changed, nil
		}
	}
	binding.TaskID, binding.WorkflowID, binding.AgentID = command.TaskID, command.WorkflowID, command.AgentID
	binding.Refs = append(binding.Refs, contextvm.ContextRef{PageID: result.PageID, Pin: command.Pin, Cold: command.Cold})
	value.inputs[key] = binding
	return result, true, nil
}

func restoreTaskInputs(value *components, bindings []taskInputBinding) error {
	value.inputs = make(map[taskInputKey]taskInputBinding, len(bindings))
	for _, binding := range bindings {
		key := taskInputKey{binding.TaskID, binding.WorkflowID, binding.AgentID}
		if !validIdempotencyKey(binding.TaskID) || len(binding.Refs) == 0 {
			return fmt.Errorf("%w: invalid task input binding", ErrInvalidCheckpoint)
		}
		if _, exists := value.inputs[key]; exists {
			return fmt.Errorf("%w: duplicate task input binding", ErrInvalidCheckpoint)
		}
		head, err := value.registry.Get(binding.WorkflowID, binding.AgentID)
		if err != nil {
			return fmt.Errorf("%w: missing task input principal", ErrInvalidCheckpoint)
		}
		available := make(map[contextvm.PageID]contextvm.ContextRef)
		for _, ref := range head.Refs() {
			available[ref.PageID] = ref
		}
		for _, ref := range binding.Refs {
			if expected, exists := available[ref.PageID]; !exists || expected != ref {
				return fmt.Errorf("%w: missing, duplicate, or inconsistent task input reference", ErrInvalidCheckpoint)
			}
			delete(available, ref.PageID)
		}
		binding.Refs = append([]contextvm.ContextRef(nil), binding.Refs...)
		value.inputs[key] = binding
	}
	return nil
}

func sortedTaskInputs(value components) []taskInputBinding {
	result := make([]taskInputBinding, 0, len(value.inputs))
	for _, binding := range value.inputs {
		binding.Refs = append([]contextvm.ContextRef(nil), binding.Refs...)
		result = append(result, binding)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].TaskID != result[j].TaskID {
			return result[i].TaskID < result[j].TaskID
		}
		if result[i].WorkflowID != result[j].WorkflowID {
			return result[i].WorkflowID < result[j].WorkflowID
		}
		return result[i].AgentID < result[j].AgentID
	})
	return result
}

func resolveTaskInputs(ctx context.Context, value components, task domain.TaskSpec, agent domain.AgentSpec, budget int) (contextdispatch.Resolution, error) {
	head, err := value.registry.Get(task.WorkflowID, task.AgentID)
	if err != nil {
		return contextdispatch.Resolution{}, err
	}
	allowed := make(map[contextvm.PageID]bool)
	dependencyRefs := make([]contextvm.ContextRef, 0)
	for _, ref := range value.inputs[taskInputKey{task.ID, task.WorkflowID, task.AgentID}].Refs {
		allowed[ref.PageID] = true
	}
	for _, dependency := range task.Dependencies {
		output, exists := value.outputs[dependency]
		if !exists {
			return contextdispatch.Resolution{}, ErrOutputNotFound
		}
		if output.WorkflowID != task.WorkflowID {
			return contextdispatch.Resolution{}, ErrInvalidTaskBinding
		}
		for _, artifact := range output.Artifacts {
			if artifact.Visibility == VisibilityWorkflow && artifactAllowsAgent(artifact, task.AgentID) {
				allowed[artifact.PageID] = true
				dependencyRefs = append(dependencyRefs, contextvm.ContextRef{PageID: artifact.PageID, Cold: artifact.Cold})
			}
		}
	}
	// The producer may inspect its own output, but sibling tasks cannot inherit it.
	if own, exists := value.outputs[task.ID]; exists && own.WorkflowID == task.WorkflowID && own.ProducerAgentID == task.AgentID {
		for _, artifact := range own.Artifacts {
			allowed[artifact.PageID] = true
		}
	}
	shared, private := selectInputRefs(head.SharedPages, allowed), selectInputRefs(head.PrivatePages, allowed)
	// The durable output and caller's validated task dependency already bind
	// authorization. Reconstruct references instead of persisting a copy in
	// every late consumer's principal manifest. Page ACLs remain enforced below.
	seen := make(map[contextvm.PageID]bool)
	for _, ref := range append(append([]contextvm.ContextRef{}, shared...), private...) {
		seen[ref.PageID] = true
	}
	added := false
	for _, ref := range dependencyRefs {
		if !seen[ref.PageID] {
			shared = append(shared, ref)
			seen[ref.PageID] = true
			added = true
		}
	}
	selected := head
	if added || len(shared)+len(private) != len(head.Refs()) {
		selected, err = contextvm.NewManifest(task.WorkflowID, task.AgentID, shared, private)
		if err != nil {
			return contextdispatch.Resolution{}, err
		}
	}
	return value.resolver.ResolveManifest(ctx, task, agent, budget, selected)
}

func selectInputRefs(refs []contextvm.ContextRef, allowed map[contextvm.PageID]bool) []contextvm.ContextRef {
	var selected []contextvm.ContextRef
	for _, ref := range refs {
		if allowed[ref.PageID] {
			selected = append(selected, ref)
		}
	}
	return selected
}
