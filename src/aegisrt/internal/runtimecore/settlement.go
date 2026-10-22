package runtimecore

import (
	"aegisrt/internal/domain"
	"aegisrt/internal/taskgraph"
)

// declaredAttemptCharge is a per-attempt scheduling charge, not measured CPU
// time or token use. A durable RUNNING observation charges the declared vector
// once on cancellation, interruption, or recovery. ADMITTED has no confirmed
// start and releases its reservation without a charge.
func declaredAttemptCharge(item taskgraph.TaskSnapshot) domain.ResourceVector {
	if item.Status == domain.TaskStatusRunning {
		return item.Spec.Resources.Clone()
	}
	return domain.ResourceVector{}
}
