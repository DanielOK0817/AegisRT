package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"aegisrt/internal/domain"
)

func TestAgentListRequiresAuthorizationAndProjectsSafeFields(t *testing.T) {
	agents := []domain.AgentSpec{{
		ID: "worker", Name: "Generic Worker", Runtime: "process",
		Entrypoint: `C:\private\agent.exe`, Args: []string{"--secret-argument"},
		Capabilities: []string{"analysis", "report"},
		Resources: domain.ResourceVector{
			CPUMilli: 500, MemoryMiB: 256, LLMSlots: 1, TokenBudget: 8000,
			ToolSlots: map[string]int64{"browser": 1},
		},
		MaxConcurrency: 2, ExecutionTimeout: 45 * time.Second,
	}}
	server := NewServer(Config{Agents: agents, AuthToken: []byte(apiToken)})

	unauthorized := performAuthorizedRequest(server.Handler(), http.MethodGet, "/v1/agents", "", "", "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want 401", unauthorized.Code)
	}

	response := performAuthorizedRequest(server.Handler(), http.MethodGet, "/v1/agents", "", apiToken, "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, forbidden := range []string{`C:\private\agent.exe`, "--secret-argument", "entrypoint", "args"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("agent response leaked %q: %s", forbidden, body)
		}
	}
	var payload struct {
		Agents []struct {
			ID                     string                `json:"id"`
			Name                   string                `json:"name"`
			Runtime                string                `json:"runtime"`
			Capabilities           []string              `json:"capabilities"`
			Resources              domain.ResourceVector `json:"resources"`
			MaxConcurrency         int                   `json:"max_concurrency"`
			ExecutionTimeoutMillis int64                 `json:"execution_timeout_ms"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Agents) != 1 || payload.Agents[0].ID != "worker" ||
		payload.Agents[0].ExecutionTimeoutMillis != 45000 ||
		payload.Agents[0].Resources.ToolSlots["browser"] != 1 {
		t.Fatalf("agents = %#v", payload.Agents)
	}
}

func TestAgentListOwnsImmutableProjection(t *testing.T) {
	agents := []domain.AgentSpec{{
		ID: "worker", Name: "Original", Runtime: "process", Entrypoint: "private",
		Capabilities:   []string{"analysis"},
		Resources:      domain.ResourceVector{ToolSlots: map[string]int64{"browser": 1}},
		MaxConcurrency: 1, ExecutionTimeout: time.Second,
	}}
	server := NewServer(Config{Agents: agents, AuthToken: []byte(apiToken)})
	agents[0].Name = "Mutated"
	agents[0].Capabilities[0] = "mutated"
	agents[0].Resources.ToolSlots["browser"] = 99

	response := performAuthorizedRequest(server.Handler(), http.MethodGet, "/v1/agents", "", apiToken, "")
	body := response.Body.String()
	if response.Code != http.StatusOK || strings.Contains(body, "Mutated") ||
		strings.Contains(body, "mutated") || strings.Contains(body, `"browser":99`) {
		t.Fatalf("mutable agent projection: status=%d body=%s", response.Code, body)
	}
}
