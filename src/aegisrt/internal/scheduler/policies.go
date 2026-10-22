package scheduler

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

type FIFO struct{}

func (FIFO) Name() string { return "fifo" }

func (p FIFO) Select(candidates []Candidate, available ResourceVector, _ time.Time) (Decision, error) {
	feasible, err := feasibleCandidates(candidates, available)
	if err != nil {
		return Decision{}, err
	}
	sort.Slice(feasible, func(i, j int) bool {
		if feasible[i].ReadyAt.Equal(feasible[j].ReadyAt) {
			return feasible[i].TaskID < feasible[j].TaskID
		}
		return feasible[i].ReadyAt.Before(feasible[j].ReadyAt)
	})
	selected := feasible[0]
	return Decision{
		Policy:        p.Name(),
		Candidate:     selected,
		Reason:        "earliest feasible ready task",
		DominantShare: selected.Resources.DominantShare(available),
	}, nil
}

type Priority struct {
	AgingInterval time.Duration
}

func (Priority) Name() string { return "priority" }

func (p Priority) Select(candidates []Candidate, available ResourceVector, now time.Time) (Decision, error) {
	feasible, err := feasibleCandidates(candidates, available)
	if err != nil {
		return Decision{}, err
	}
	sort.Slice(feasible, func(i, j int) bool {
		iScore := candidateScore(feasible[i], now, p.AgingInterval)
		jScore := candidateScore(feasible[j], now, p.AgingInterval)
		if iScore != jScore {
			return iScore > jScore
		}
		if !feasible[i].ReadyAt.Equal(feasible[j].ReadyAt) {
			return feasible[i].ReadyAt.Before(feasible[j].ReadyAt)
		}
		return feasible[i].TaskID < feasible[j].TaskID
	})
	selected := feasible[0]
	return Decision{
		Policy:        p.Name(),
		Candidate:     selected,
		Reason:        fmt.Sprintf("highest effective task score %.2f", candidateScore(selected, now, p.AgingInterval)),
		DominantShare: selected.Resources.DominantShare(available),
	}, nil
}

// DAGDRCFS combines workflow-level dominant-resource fairness with DAG-aware
// ranking inside the selected workflow.
type DAGDRCFS struct {
	mu                 sync.RWMutex
	capacity           ResourceVector
	agingInterval      time.Duration
	vruntime           map[string]float64
	durationAccounting bool
}

func NewDAGDRCFS(capacity ResourceVector, agingInterval time.Duration) (*DAGDRCFS, error) {
	if err := capacity.Validate(); err != nil {
		return nil, err
	}
	if agingInterval <= 0 {
		agingInterval = time.Second
	}
	return &DAGDRCFS{
		capacity:      capacity,
		agingInterval: agingInterval,
		vruntime:      make(map[string]float64),
	}, nil
}

func (p *DAGDRCFS) Name() string {
	if p.durationAccounting {
		return "dag-drcfs-time"
	}
	return "dag-drcfs"
}

func (p *DAGDRCFS) Select(candidates []Candidate, available ResourceVector, now time.Time) (Decision, error) {
	return p.selectWithReservations(candidates, available, now, nil)
}

func (p *DAGDRCFS) selectWithReservations(candidates []Candidate, available ResourceVector, now time.Time, reservations []Candidate) (Decision, error) {
	feasible, err := feasibleCandidates(candidates, available)
	if err != nil {
		return Decision{}, err
	}

	p.mu.RLock()
	defer p.mu.RUnlock()
	pending := make(map[string]float64)
	// Stable summation prevents map iteration from changing near-tie decisions.
	sort.Slice(reservations, func(i, j int) bool { return reservations[i].TaskID < reservations[j].TaskID })
	for _, reservation := range reservations {
		weight := 1.0
		if p.durationAccounting && !reservation.ServiceStartedAt.IsZero() {
			// A queued scheduling call can carry an observation from before a
			// different worker acquired the lock and recorded its process start.
			// Selection charges no negative interval; settlement still rejects
			// an end timestamp preceding its own execution start.
			weight += math.Max(0, now.Sub(reservation.ServiceStartedAt).Seconds())
		}
		pending[reservation.WorkflowID] += weight * reservation.Resources.DominantShare(p.capacity) / float64(reservation.Priority+1)
	}
	workflows := make(map[string][]Candidate)
	for _, candidate := range feasible {
		workflows[candidate.WorkflowID] = append(workflows[candidate.WorkflowID], candidate)
	}

	workflowIDs := make([]string, 0, len(workflows))
	for workflowID := range workflows {
		workflowIDs = append(workflowIDs, workflowID)
	}
	sort.Slice(workflowIDs, func(i, j int) bool {
		iRuntime := p.vruntime[workflowIDs[i]] + pending[workflowIDs[i]]
		jRuntime := p.vruntime[workflowIDs[j]] + pending[workflowIDs[j]]
		if iRuntime != jRuntime {
			return iRuntime < jRuntime
		}
		return workflowIDs[i] < workflowIDs[j]
	})

	workflowID := workflowIDs[0]
	workflowTasks := workflows[workflowID]
	sort.Slice(workflowTasks, func(i, j int) bool {
		iScore := candidateScore(workflowTasks[i], now, p.agingInterval)
		jScore := candidateScore(workflowTasks[j], now, p.agingInterval)
		if iScore != jScore {
			return iScore > jScore
		}
		return workflowTasks[i].TaskID < workflowTasks[j].TaskID
	})
	selected := workflowTasks[0]
	return Decision{
		Policy:                 p.Name(),
		Candidate:              selected,
		Reason:                 fmt.Sprintf("workflow %s has minimum vruntime plus reserved charge %.6f (settled %.6f, reserved %.6f); task score %.2f", workflowID, p.vruntime[workflowID]+pending[workflowID], p.vruntime[workflowID], pending[workflowID], candidateScore(selected, now, p.agingInterval)),
		WorkflowVRuntime:       p.vruntime[workflowID],
		WorkflowReservedCharge: pending[workflowID],
		DominantShare:          selected.Resources.DominantShare(p.capacity),
	}, nil
}

func (p *DAGDRCFS) Account(workflowID string, usage ResourceVector, priority int) error {
	if workflowID == "" {
		return fmt.Errorf("workflow_id is required")
	}
	if err := usage.Validate(); err != nil {
		return err
	}
	weight := float64(priority + 1)
	increment := usage.DominantShare(p.capacity) / weight
	if math.IsNaN(increment) || math.IsInf(increment, 0) || increment < 0 {
		return fmt.Errorf("accounting would produce invalid vruntime increment %v", increment)
	}
	p.mu.Lock()
	next := p.vruntime[workflowID] + increment
	if math.IsNaN(next) || math.IsInf(next, 0) || next < 0 {
		p.mu.Unlock()
		return fmt.Errorf("accounting would produce invalid vruntime %v", next)
	}
	p.vruntime[workflowID] = next
	p.mu.Unlock()
	return nil
}

func (p *DAGDRCFS) VRuntime(workflowID string) float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.vruntime[workflowID]
}

func candidateScore(candidate Candidate, now time.Time, agingInterval time.Duration) float64 {
	age := 0.0
	if agingInterval > 0 && now.After(candidate.ReadyAt) {
		age = now.Sub(candidate.ReadyAt).Seconds() / agingInterval.Seconds()
	}
	return float64(candidate.Priority*4+candidate.InheritedPriority*4+candidate.CriticalPath*2+candidate.UnlockCount) + age
}

func feasibleCandidates(candidates []Candidate, available ResourceVector) ([]Candidate, error) {
	if err := available.Validate(); err != nil {
		return nil, err
	}
	feasible := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if err := candidate.Validate(); err != nil {
			return nil, fmt.Errorf("candidate %q: %w", candidate.TaskID, err)
		}
		if candidate.Resources.FitsWithin(available) {
			feasible = append(feasible, candidate)
		}
	}
	if len(feasible) == 0 {
		return nil, ErrNoCandidate
	}
	return feasible, nil
}
