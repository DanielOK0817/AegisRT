package scheduler

import (
	"fmt"
	"sync"
	"time"
)

type Engine struct {
	mu           sync.Mutex
	policy       Policy
	capacity     ResourceVector
	allocated    ResourceVector
	reservations map[string]Candidate
}

func NewEngine(policy Policy, capacity ResourceVector) (*Engine, error) {
	if policy == nil {
		return nil, fmt.Errorf("policy is required")
	}
	if err := capacity.Validate(); err != nil {
		return nil, err
	}
	return &Engine{
		policy:       policy,
		capacity:     capacity.Clone(),
		reservations: make(map[string]Candidate),
	}, nil
}

func (e *Engine) DecideAndReserve(candidates []Candidate, now time.Time) (Decision, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	available, err := e.capacity.Sub(e.allocated)
	if err != nil {
		return Decision{}, err
	}
	var decision Decision
	if policy, ok := e.policy.(*DAGDRCFS); ok {
		// Derive pending charges from reservations so release and checkpoint
		// rollback cannot leave a second, inconsistent fairness ledger.
		pending := make([]Candidate, 0, len(e.reservations))
		for _, candidate := range e.reservations {
			pending = append(pending, candidate)
		}
		decision, err = policy.selectWithReservations(candidates, available, now, pending)
	} else {
		decision, err = e.policy.Select(candidates, available, now)
	}
	if err != nil {
		return Decision{}, err
	}
	if _, exists := e.reservations[decision.Candidate.TaskID]; exists {
		return Decision{}, ErrAlreadyReserved
	}
	next, err := e.allocated.AddChecked(decision.Candidate.Resources)
	if err != nil {
		return Decision{}, err
	}
	if !next.FitsWithin(e.capacity) {
		return Decision{}, ErrInsufficientResource
	}
	e.allocated = next
	e.reservations[decision.Candidate.TaskID] = cloneCandidate(decision.Candidate)
	return decision, nil
}

func (e *Engine) Complete(taskID string, actualUsage ResourceVector) error {
	return e.CompleteAt(taskID, actualUsage, time.Now())
}

func (e *Engine) CompleteAt(taskID string, actualUsage ResourceVector, now time.Time) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	candidate, exists := e.reservations[taskID]
	if !exists {
		return ErrNotReserved
	}
	if err := actualUsage.Validate(); err != nil {
		return err
	}
	next, err := e.allocated.Sub(candidate.Resources)
	if err != nil {
		return err
	}
	if policy, ok := e.policy.(*DAGDRCFS); ok && policy.durationAccounting {
		if err := policy.accountDuration(candidate, now); err != nil {
			return err
		}
	} else if accounting, ok := e.policy.(interface {
		Account(string, ResourceVector, int) error
	}); ok {
		if err := accounting.Account(candidate.WorkflowID, actualUsage, candidate.Priority); err != nil {
			return err
		}
	}
	e.allocated = next
	delete(e.reservations, taskID)
	return nil
}

func (e *Engine) Capacity() ResourceVector {
	e.mu.Lock()
	defer e.mu.Unlock()
	return copyVector(e.capacity)
}

func (e *Engine) Allocated() ResourceVector {
	e.mu.Lock()
	defer e.mu.Unlock()
	return copyVector(e.allocated)
}

func (e *Engine) PolicyName() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.policy.Name()
}

func copyVector(vector ResourceVector) ResourceVector {
	return vector.Clone()
}
