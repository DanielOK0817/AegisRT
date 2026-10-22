package contextdispatch

import (
	"context"
	"errors"
	"fmt"

	"aegisrt/internal/contextvm"
	"aegisrt/internal/domain"
)

var (
	ErrInvalidResolver          = errors.New("invalid context resolver")
	ErrInvalidResolutionRequest = errors.New("invalid context resolution request")
	ErrTaskAgentMismatch        = errors.New("task agent does not match agent specification")
	ErrManifestPrincipal        = errors.New("context manifest principal does not match task")
)

type Resolution struct {
	Text       string                    `json:"text"`
	ManifestID string                    `json:"manifest_id"`
	Refs       []contextvm.ContextRef    `json:"refs"`
	Metrics    contextvm.AssemblyMetrics `json:"metrics"`
}

// Resolver validates the runtime principal, obtains its manifest snapshot,
// and delegates selection, ACL enforcement, budgeting, and summarization to
// ContextVM's Assembler.
type Resolver struct {
	registry  *Registry
	assembler *contextvm.Assembler
}

func NewResolver(registry *Registry, assembler *contextvm.Assembler) (*Resolver, error) {
	if registry == nil || assembler == nil {
		return nil, fmt.Errorf("%w: registry and assembler are required", ErrInvalidResolver)
	}
	return &Resolver{registry: registry, assembler: assembler}, nil
}

func (r *Resolver) Resolve(
	ctx context.Context,
	task domain.TaskSpec,
	agent domain.AgentSpec,
	tokenBudget int,
) (Resolution, error) {
	if r == nil || r.registry == nil || r.assembler == nil {
		return Resolution{}, ErrInvalidResolver
	}
	if err := ValidateResolutionRequest(ctx, task, agent, tokenBudget); err != nil {
		return Resolution{}, err
	}
	manifest, err := r.registry.Get(task.WorkflowID, agent.ID)
	if err != nil {
		return Resolution{}, err
	}
	return r.ResolveManifest(ctx, task, agent, tokenBudget, manifest)
}

// ResolveManifest assembles an already selected task view and still enforces
// the task principal, page ACLs, and token budget.
func (r *Resolver) ResolveManifest(ctx context.Context, task domain.TaskSpec, agent domain.AgentSpec,
	tokenBudget int, manifest contextvm.Manifest) (Resolution, error) {
	if r == nil || r.assembler == nil {
		return Resolution{}, ErrInvalidResolver
	}
	if err := ValidateResolutionRequest(ctx, task, agent, tokenBudget); err != nil {
		return Resolution{}, err
	}
	if manifest.WorkflowID != task.WorkflowID || manifest.AgentID != agent.ID {
		return Resolution{}, fmt.Errorf("%w: manifest workflow=%q agent=%q, task workflow=%q agent=%q",
			ErrManifestPrincipal, manifest.WorkflowID, manifest.AgentID, task.WorkflowID, agent.ID)
	}
	assembly, err := r.assembler.Assemble(ctx, contextvm.AssembleRequest{
		Manifest: manifest,
		Access: contextvm.AccessRequest{
			WorkflowID: task.WorkflowID,
			AgentID:    agent.ID,
		},
		TokenBudget: tokenBudget,
	})
	if err != nil {
		return Resolution{}, fmt.Errorf("resolve context for workflow %q agent %q: %w",
			task.WorkflowID, agent.ID, err)
	}
	return Resolution{
		Text:       assembly.Text,
		ManifestID: manifest.ID,
		Refs:       append([]contextvm.ContextRef(nil), assembly.Refs...),
		Metrics:    assembly.Metrics,
	}, nil
}

func ValidateResolutionRequest(
	ctx context.Context,
	task domain.TaskSpec,
	agent domain.AgentSpec,
	tokenBudget int,
) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is required", ErrInvalidResolutionRequest)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := task.Validate(); err != nil {
		return fmt.Errorf("%w: task: %v", ErrInvalidResolutionRequest, err)
	}
	if err := agent.Validate(); err != nil {
		return fmt.Errorf("%w: agent: %v", ErrInvalidResolutionRequest, err)
	}
	if task.AgentID != agent.ID {
		return fmt.Errorf("%w: task=%q agent=%q",
			ErrTaskAgentMismatch, task.AgentID, agent.ID)
	}
	if tokenBudget < 0 {
		return fmt.Errorf("%w: %d", contextvm.ErrInvalidBudget, tokenBudget)
	}
	return nil
}
