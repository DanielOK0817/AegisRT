package contextvm

import "fmt"

type AccessRequest struct {
	WorkflowID string `json:"workflow_id"`
	AgentID    string `json:"agent_id"`
}

func (r AccessRequest) Validate() error {
	if err := validateOwnerID("workflow_id", r.WorkflowID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidAccess, err)
	}
	if err := validateOwnerID("agent_id", r.AgentID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidAccess, err)
	}
	return nil
}

func Authorize(page Page, request AccessRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if err := page.Validate(); err != nil {
		return err
	}
	allowed := false
	switch page.Scope {
	case ScopeSystem:
		allowed = true
	case ScopeWorkflow:
		allowed = page.WorkflowID == request.WorkflowID
	case ScopeAgent, ScopeEphemeral:
		allowed = page.WorkflowID == request.WorkflowID && page.AgentID == request.AgentID
	}
	if !allowed {
		return fmt.Errorf("%w: scope=%s workflow=%q agent=%q",
			ErrAccessDenied, page.Scope, request.WorkflowID, request.AgentID)
	}
	return nil
}
