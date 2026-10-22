package contextstate

import (
	"aegisrt/internal/domain"
	"fmt"
)

type dependencyArtifact struct {
	producerTaskID string
	artifactIndex  int
	record         ArtifactRecord
}

func (r *Repository) dependencyArtifacts(task domain.TaskSpec) ([]dependencyArtifact, error) {
	result := make([]dependencyArtifact, 0)
	for _, dependencyID := range task.Dependencies {
		output, exists := r.outputs[dependencyID]
		if !exists {
			return nil, fmt.Errorf("%w: dependency %q", ErrOutputNotFound, dependencyID)
		}
		if output.WorkflowID != task.WorkflowID {
			return nil, ErrInvalidTaskBinding
		}
		for index, artifact := range output.Artifacts {
			if artifact.Visibility != VisibilityWorkflow || !artifactAllowsAgent(artifact, task.AgentID) {
				continue
			}
			result = append(result, dependencyArtifact{
				producerTaskID: output.TaskID, artifactIndex: index, record: artifact,
			})
		}
	}
	return result, nil
}

func artifactAllowsAgent(artifact ArtifactRecord, agentID string) bool {
	if artifact.Recipients == nil {
		return true
	}
	for _, recipient := range artifact.Recipients {
		if recipient == agentID {
			return true
		}
	}
	return false
}
