package scheduler

import (
	"errors"
	"time"

	"aegisrt/internal/domain"
)

var (
	ErrNoCandidate          = errors.New("no candidate can be scheduled")
	ErrInsufficientResource = domain.ErrInsufficientResource
	ErrAlreadyReserved      = errors.New("task is already reserved")
	ErrNotReserved          = errors.New("task is not reserved")
)

// ResourceVector is the single resource model shared by the runtime domain and
// scheduler. The alias prevents adapters from maintaining parallel schemas.
type ResourceVector = domain.ResourceVector

type Candidate struct {
	TaskID            string         `json:"task_id"`
	WorkflowID        string         `json:"workflow_id"`
	Priority          int            `json:"priority"`
	InheritedPriority int            `json:"inherited_priority"`
	CriticalPath      int            `json:"critical_path"`
	UnlockCount       int            `json:"unlock_count"`
	ReadyAt           time.Time      `json:"ready_at"`
	ServiceStartedAt  time.Time      `json:"service_started_at,omitempty"`
	Resources         ResourceVector `json:"resources"`
}

func (c Candidate) Validate() error {
	if c.TaskID == "" || c.WorkflowID == "" {
		return errors.New("task_id and workflow_id are required")
	}
	if c.Priority < 0 || c.InheritedPriority < 0 || c.CriticalPath < 0 || c.UnlockCount < 0 {
		return errors.New("scheduling attributes must be non-negative")
	}
	return c.Resources.Validate()
}

type Decision struct {
	Policy                 string    `json:"policy"`
	Candidate              Candidate `json:"candidate"`
	Reason                 string    `json:"reason"`
	WorkflowVRuntime       float64   `json:"workflow_vruntime"`
	WorkflowReservedCharge float64   `json:"workflow_reserved_charge"`
	DominantShare          float64   `json:"dominant_share"`
}

type Policy interface {
	Name() string
	Select(candidates []Candidate, available ResourceVector, now time.Time) (Decision, error)
}
