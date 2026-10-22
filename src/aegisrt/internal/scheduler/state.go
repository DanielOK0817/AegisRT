package scheduler

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// WorkflowVRuntime is a deterministic, serializable fairness-accounting row.
type WorkflowVRuntime struct {
	WorkflowID string  `json:"workflow_id"`
	VRuntime   float64 `json:"vruntime"`
}

// PolicyState contains optional durable state owned by a scheduling policy.
type PolicyState struct {
	WorkflowVRuntime []WorkflowVRuntime `json:"workflow_vruntime,omitempty"`
}

// State is the durable representation of a scheduler Engine.
type State struct {
	PolicyName   string         `json:"policy_name"`
	Capacity     ResourceVector `json:"capacity"`
	Allocated    ResourceVector `json:"allocated"`
	Reservations []Candidate    `json:"reservations"`
	PolicyState  PolicyState    `json:"policy_state"`
}

// State returns a deep copy with reservations and policy rows sorted by id.
func (e *Engine) State() State {
	e.mu.Lock()
	defer e.mu.Unlock()

	state := State{
		PolicyName:   e.policy.Name(),
		Capacity:     e.capacity.Clone(),
		Allocated:    e.allocated.Clone(),
		Reservations: make([]Candidate, 0, len(e.reservations)),
	}
	for _, reservation := range e.reservations {
		state.Reservations = append(state.Reservations, cloneCandidate(reservation))
	}
	sort.Slice(state.Reservations, func(i, j int) bool {
		return state.Reservations[i].TaskID < state.Reservations[j].TaskID
	})
	if policy, ok := e.policy.(*DAGDRCFS); ok {
		state.PolicyState = policy.FairnessState()
	}
	return state
}

// Restore validates the complete snapshot before replacing any engine or
// policy state. The configured policy and capacity are immutable identities.
func (e *Engine) Restore(state State) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if state.PolicyName != e.policy.Name() {
		return fmt.Errorf("scheduler policy mismatch: state=%q configured=%q", state.PolicyName, e.policy.Name())
	}
	if err := state.Capacity.Validate(); err != nil {
		return fmt.Errorf("invalid scheduler capacity: %w", err)
	}
	if !equalResources(state.Capacity, e.capacity) {
		return fmt.Errorf("scheduler capacity mismatch")
	}
	if err := state.Allocated.Validate(); err != nil {
		return fmt.Errorf("invalid allocated resources: %w", err)
	}
	if !state.Allocated.FitsWithin(e.capacity) {
		return fmt.Errorf("allocated resources exceed capacity")
	}

	reservations := make(map[string]Candidate, len(state.Reservations))
	total := ResourceVector{}
	for index, reservation := range state.Reservations {
		if err := reservation.Validate(); err != nil {
			return fmt.Errorf("invalid reservations[%d]: %w", index, err)
		}
		if _, exists := reservations[reservation.TaskID]; exists {
			return fmt.Errorf("duplicate reservation task id %q", reservation.TaskID)
		}
		if !reservation.Resources.FitsWithin(e.capacity) {
			return fmt.Errorf("reservation %q exceeds capacity", reservation.TaskID)
		}
		var err error
		total, err = total.AddChecked(reservation.Resources)
		if err != nil {
			return fmt.Errorf("reservation resource sum: %w", err)
		}
		reservations[reservation.TaskID] = cloneCandidate(reservation)
	}
	if !total.FitsWithin(e.capacity) {
		return fmt.Errorf("reservation resource sum exceeds capacity")
	}
	if !equalResources(total, state.Allocated) {
		return fmt.Errorf("allocated resources do not equal reservation sum")
	}

	var fairness map[string]float64
	if policy, ok := e.policy.(*DAGDRCFS); ok {
		policy.mu.RLock()
		policyCapacity := policy.capacity.Clone()
		policy.mu.RUnlock()
		if !equalResources(policyCapacity, e.capacity) {
			return fmt.Errorf("dag-drcfs capacity does not match engine capacity")
		}
		var err error
		fairness, err = validateFairnessState(state.PolicyState)
		if err != nil {
			return err
		}
	} else if len(state.PolicyState.WorkflowVRuntime) != 0 {
		return fmt.Errorf("policy %q does not support workflow vruntime state", e.policy.Name())
	}

	if policy, ok := e.policy.(*DAGDRCFS); ok {
		policy.replaceFairnessState(fairness)
	}
	e.allocated = state.Allocated.Clone()
	e.reservations = reservations
	return nil
}

// FairnessState returns a sorted deep copy of DAG-DRCFS virtual runtimes.
func (p *DAGDRCFS) FairnessState() PolicyState {
	p.mu.RLock()
	defer p.mu.RUnlock()

	state := PolicyState{WorkflowVRuntime: make([]WorkflowVRuntime, 0, len(p.vruntime))}
	for workflowID, vruntime := range p.vruntime {
		state.WorkflowVRuntime = append(state.WorkflowVRuntime, WorkflowVRuntime{
			WorkflowID: workflowID,
			VRuntime:   vruntime,
		})
	}
	sort.Slice(state.WorkflowVRuntime, func(i, j int) bool {
		return state.WorkflowVRuntime[i].WorkflowID < state.WorkflowVRuntime[j].WorkflowID
	})
	return state
}

// RestoreFairnessState transactionally replaces DAG-DRCFS fairness state.
func (p *DAGDRCFS) RestoreFairnessState(state PolicyState) error {
	vruntime, err := validateFairnessState(state)
	if err != nil {
		return err
	}
	p.replaceFairnessState(vruntime)
	return nil
}

func (p *DAGDRCFS) replaceFairnessState(vruntime map[string]float64) {
	p.mu.Lock()
	p.vruntime = vruntime
	p.mu.Unlock()
}

func validateFairnessState(state PolicyState) (map[string]float64, error) {
	result := make(map[string]float64, len(state.WorkflowVRuntime))
	for index, item := range state.WorkflowVRuntime {
		if item.WorkflowID == "" || strings.TrimSpace(item.WorkflowID) != item.WorkflowID {
			return nil, fmt.Errorf("invalid workflow vruntime[%d] id %q", index, item.WorkflowID)
		}
		if math.IsNaN(item.VRuntime) || math.IsInf(item.VRuntime, 0) || item.VRuntime < 0 {
			return nil, fmt.Errorf("invalid workflow %q vruntime %v", item.WorkflowID, item.VRuntime)
		}
		if _, exists := result[item.WorkflowID]; exists {
			return nil, fmt.Errorf("duplicate workflow vruntime id %q", item.WorkflowID)
		}
		result[item.WorkflowID] = item.VRuntime
	}
	return result, nil
}

func cloneCandidate(candidate Candidate) Candidate {
	result := candidate
	result.Resources = candidate.Resources.Clone()
	return result
}

func equalResources(left, right ResourceVector) bool {
	if left.CPUMilli != right.CPUMilli || left.MemoryMiB != right.MemoryMiB ||
		left.LLMSlots != right.LLMSlots || left.TokenBudget != right.TokenBudget {
		return false
	}
	for name, value := range left.ToolSlots {
		if right.ToolSlots[name] != value {
			return false
		}
	}
	for name, value := range right.ToolSlots {
		if left.ToolSlots[name] != value {
			return false
		}
	}
	return true
}
