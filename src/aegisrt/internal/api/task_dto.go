package api

import (
	"fmt"
	"math"
	"time"

	"aegisrt/internal/domain"
	"aegisrt/internal/taskgraph"
)

type taskDTO struct {
	ID             string                `json:"id"`
	WorkflowID     string                `json:"workflow_id"`
	AgentID        string                `json:"agent_id"`
	Goal           string                `json:"goal"`
	Dependencies   []string              `json:"dependencies,omitempty"`
	Priority       int                   `json:"priority"`
	Resources      domain.ResourceVector `json:"resources"`
	MaxAttempts    int                   `json:"max_attempts"`
	TimeoutMillis  int64                 `json:"timeout_ms"`
	IdempotencyKey string                `json:"idempotency_key"`
}

func (dto taskDTO) domain() (domain.TaskSpec, error) {
	if dto.TimeoutMillis <= 0 {
		return domain.TaskSpec{}, fmt.Errorf("timeout_ms must be positive")
	}
	if dto.TimeoutMillis > math.MaxInt64/int64(time.Millisecond) {
		return domain.TaskSpec{}, fmt.Errorf("timeout_ms exceeds the maximum supported duration")
	}
	return domain.TaskSpec{
		ID:             dto.ID,
		WorkflowID:     dto.WorkflowID,
		AgentID:        dto.AgentID,
		Goal:           dto.Goal,
		Dependencies:   append([]string(nil), dto.Dependencies...),
		Priority:       dto.Priority,
		Resources:      dto.Resources.Clone(),
		MaxAttempts:    dto.MaxAttempts,
		Timeout:        time.Duration(dto.TimeoutMillis) * time.Millisecond,
		IdempotencyKey: dto.IdempotencyKey,
	}, nil
}

func view(snapshot taskgraph.TaskSnapshot) taskView {
	return taskView{
		Task: taskDTO{
			ID:             snapshot.Spec.ID,
			WorkflowID:     snapshot.Spec.WorkflowID,
			AgentID:        snapshot.Spec.AgentID,
			Goal:           snapshot.Spec.Goal,
			Dependencies:   append([]string(nil), snapshot.Spec.Dependencies...),
			Priority:       snapshot.Spec.Priority,
			Resources:      snapshot.Spec.Resources.Clone(),
			MaxAttempts:    snapshot.Spec.MaxAttempts,
			TimeoutMillis:  snapshot.Spec.Timeout.Milliseconds(),
			IdempotencyKey: snapshot.Spec.IdempotencyKey,
		},
		Status: snapshot.Status,
	}
}

type taskView struct {
	Task   taskDTO           `json:"task"`
	Status domain.TaskStatus `json:"status"`
}
