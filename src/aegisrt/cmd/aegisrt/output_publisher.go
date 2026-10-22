package main

import (
	"context"
	"errors"
	"sync"

	"aegisrt/internal/agentprotocol"
	"aegisrt/internal/controlplane"
)

var errOutputPublisherBound = errors.New("output publisher proxy is already bound")

// outputPublisherProxy breaks the composition-time cycle between Coordinator
// and ContextControl. Binding happens once before the Runtime service starts.
type outputPublisherProxy struct {
	mu     sync.RWMutex
	target controlplane.OutputPublisher
}

func (p *outputPublisherProxy) Bind(target controlplane.OutputPublisher) error {
	if target == nil {
		return controlplane.ErrOutputPublisherRequired
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.target != nil {
		return errOutputPublisherBound
	}
	p.target = target
	return nil
}

func (p *outputPublisherProxy) PublishAgentOutput(
	ctx context.Context,
	taskID string,
	attempt int,
	artifacts []agentprotocol.Artifact,
) error {
	p.mu.RLock()
	target := p.target
	p.mu.RUnlock()
	if target == nil {
		return controlplane.ErrOutputPublisherRequired
	}
	return target.PublishAgentOutput(ctx, taskID, attempt, artifacts)
}

func (p *outputPublisherProxy) ValidateAgentOutput(ctx context.Context, taskID string, attempt int, artifacts []agentprotocol.Artifact) error {
	p.mu.RLock()
	target := p.target
	p.mu.RUnlock()
	if target == nil {
		return controlplane.ErrOutputPublisherRequired
	}
	if validator, ok := target.(controlplane.OutputValidator); ok {
		return validator.ValidateAgentOutput(ctx, taskID, attempt, artifacts)
	}
	return nil
}
