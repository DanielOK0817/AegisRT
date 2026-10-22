package api

import (
	"sort"
	"time"

	"aegisrt/internal/domain"
)

// agentDTO intentionally omits Entrypoint and Args. Those fields describe
// local execution details and must never cross the HTTP trust boundary.
type agentDTO struct {
	ID                     string                `json:"id"`
	Name                   string                `json:"name"`
	Runtime                string                `json:"runtime"`
	Capabilities           []string              `json:"capabilities"`
	Resources              domain.ResourceVector `json:"resources"`
	MaxConcurrency         int                   `json:"max_concurrency"`
	ExecutionTimeoutMillis int64                 `json:"execution_timeout_ms"`
}

func projectAgents(agents []domain.AgentSpec) []agentDTO {
	result := make([]agentDTO, 0, len(agents))
	for _, agent := range agents {
		capabilities := append([]string{}, agent.Capabilities...)
		sort.Strings(capabilities)
		result = append(result, agentDTO{
			ID: agent.ID, Name: agent.Name, Runtime: agent.Runtime,
			Capabilities: capabilities, Resources: agent.Resources.Clone(),
			MaxConcurrency:         agent.MaxConcurrency,
			ExecutionTimeoutMillis: durationMillis(agent.ExecutionTimeout),
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func durationMillis(value time.Duration) int64 { return value.Milliseconds() }
