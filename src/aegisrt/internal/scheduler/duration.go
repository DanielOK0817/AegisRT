package scheduler

import (
	"fmt"
	"math"
	"time"
)

// NewDurationDAGDRCFS accounts reserved resource share times observed execution
// seconds. This measures occupancy of declared capacity, not consumed CPU time.
// Its distinct policy identity prevents loading count-based fairness history.
func NewDurationDAGDRCFS(capacity ResourceVector, agingInterval time.Duration) (*DAGDRCFS, error) {
	p, err := NewDAGDRCFS(capacity, agingInterval)
	if err != nil {
		return nil, err
	}
	p.durationAccounting = true
	return p, nil
}

// MarkStarted is called only after the worker has observed a successful start.
// The enclosing Runtime persists this timestamp with its RUNNING transition.
func (e *Engine) MarkStarted(taskID string, now time.Time) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	p, ok := e.policy.(*DAGDRCFS)
	if !ok || !p.durationAccounting {
		return nil
	}
	c, exists := e.reservations[taskID]
	if !exists {
		return ErrNotReserved
	}
	if now.IsZero() {
		return fmt.Errorf("process start time is required")
	}
	if !c.ServiceStartedAt.IsZero() {
		return nil
	}
	c.ServiceStartedAt = now
	e.reservations[taskID] = c
	return nil
}

func (p *DAGDRCFS) accountDuration(c Candidate, now time.Time) error {
	if c.ServiceStartedAt.IsZero() {
		return nil
	}
	if now.IsZero() || now.Before(c.ServiceStartedAt) {
		return fmt.Errorf("accounting clock precedes process start")
	}
	increment := c.Resources.DominantShare(p.capacity) * now.Sub(c.ServiceStartedAt).Seconds() / float64(c.Priority+1)
	p.mu.Lock()
	defer p.mu.Unlock()
	next := p.vruntime[c.WorkflowID] + increment
	if math.IsNaN(next) || math.IsInf(next, 0) || next < 0 {
		return fmt.Errorf("invalid duration accounting charge")
	}
	p.vruntime[c.WorkflowID] = next
	return nil
}
