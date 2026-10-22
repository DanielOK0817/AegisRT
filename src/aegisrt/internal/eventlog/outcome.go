package eventlog

import (
	"math"
	"time"
)

// SchedulingPayload contains only numeric selection evidence, never a reason,
// resource key, goal, or command supplied by a task.
type SchedulingPayload struct {
	Priority               int     `json:"priority"`
	InheritedPriority      int     `json:"inherited_priority"`
	CriticalPath           int     `json:"critical_path"`
	UnlockCount            int     `json:"unlock_count"`
	WorkflowVRuntime       float64 `json:"workflow_vruntime"`
	WorkflowReservedCharge float64 `json:"workflow_reserved_charge"`
	DominantShare          float64 `json:"dominant_share"`
}

func (p SchedulingPayload) Valid() bool {
	if p.Priority < 0 || p.InheritedPriority < 0 || p.CriticalPath < 0 || p.UnlockCount < 0 {
		return false
	}
	for _, value := range []float64{p.WorkflowVRuntime, p.WorkflowReservedCharge, p.DominantShare} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return false
		}
	}
	return p.DominantShare <= 1
}

// LifecyclePayload records the process evidence available at observation time.
// Absent timestamps mean that the corresponding phase was not observed.
type LifecyclePayload struct {
	Phase                string     `json:"phase"`
	PreparationStartedAt *time.Time `json:"preparation_started_at,omitempty"`
	ProcessStartedAt     *time.Time `json:"process_started_at,omitempty"`
	WaitCompletedAt      *time.Time `json:"wait_completed_at,omitempty"`
}

func (p LifecyclePayload) Valid(observedAt time.Time) bool {
	if p.Phase != "before_start" && p.Phase != "process_started" && p.Phase != "process_exited" {
		return false
	}
	if (p.ProcessStartedAt != nil) != (p.Phase != "before_start") ||
		(p.WaitCompletedAt != nil) != (p.Phase == "process_exited") {
		return false
	}
	previous := time.Time{}
	for _, value := range []*time.Time{p.PreparationStartedAt, p.ProcessStartedAt, p.WaitCompletedAt} {
		if value == nil {
			continue
		}
		if value.IsZero() || value.Before(previous) || value.After(observedAt) {
			return false
		}
		if _, err := value.MarshalJSON(); err != nil {
			return false
		}
		previous = *value
	}
	return p.PreparationStartedAt != nil || p.ProcessStartedAt != nil
}

// OutputPayload is emitted only after successful task completion. It excludes
// artifact content, names, recipient identities, and storage paths.
type OutputPayload struct {
	ArtifactCount int   `json:"artifact_count"`
	ContentBytes  int64 `json:"content_bytes"`
}

func (p OutputPayload) Valid() bool {
	return p.ArtifactCount >= 0 && p.ContentBytes >= 0 && (p.ArtifactCount != 0 || p.ContentBytes == 0)
}
