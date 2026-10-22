package runtimeapp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"aegisrt/internal/agentprotocol"
	"aegisrt/internal/contextstate"
	"aegisrt/internal/domain"
	"aegisrt/internal/taskgraph"
)

var (
	ErrOutputRecipientForbidden = errors.New("agent output recipient is not a direct dependant")
	ErrOutputFanoutExceeded     = errors.New("agent output fanout exceeds protocol limit")
)

type channelPlan struct {
	artifact agentprotocol.Artifact
	targets  []string
}

// PublishAgentOutput persists Agent artifacts into the producer and direct
// dependant Context manifests. The task graph, not Agent-supplied ownership,
// defines the maximum recipient set.
func (c *ContextControl) PublishAgentOutput(ctx context.Context, taskID string, attempt int, artifacts []agentprotocol.Artifact) error {
	return c.applyAgentOutput(ctx, taskID, attempt, artifacts, false)
}

func (c *ContextControl) ValidateAgentOutput(ctx context.Context, taskID string, attempt int, artifacts []agentprotocol.Artifact) error {
	return c.applyAgentOutput(ctx, taskID, attempt, artifacts, true)
}

func (c *ContextControl) applyAgentOutput(ctx context.Context, taskID string, attempt int, artifacts []agentprotocol.Artifact, validateOnly bool) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if attempt <= 0 {
		return contextstate.ErrInvalidOutputPublication
	}
	if c.contexts == nil {
		return contextstate.ErrUnavailable
	}
	validated, err := validateChannelArtifacts(artifacts)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	producer, exists := c.control.Task(taskID)
	if !exists {
		return fmt.Errorf("%w: %q", taskgraph.ErrTaskNotFound, taskID)
	}
	tasks := c.control.Tasks()
	plans, err := c.planChannelFanout(producer.Spec, tasks, validated)
	if err != nil {
		return err
	}
	command, err := c.channelOutputCommand(producer.Spec, tasks, plans)
	if err != nil {
		return err
	}
	command.AttemptNumber = attempt
	if validateOnly {
		validator, ok := c.contexts.(interface {
			ValidateOutput(context.Context, contextstate.OutputCommand) error
		})
		if !ok {
			return contextstate.ErrUnavailable
		}
		return validator.ValidateOutput(ctx, command)
	}
	_, err = c.contexts.PublishOutput(ctx, command, time.Now().UTC())
	return err
}

func validateChannelArtifacts(artifacts []agentprotocol.Artifact) ([]agentprotocol.Artifact, error) {
	if err := agentprotocol.ValidateArtifacts(artifacts); err != nil {
		return nil, err
	}
	result := append([]agentprotocol.Artifact(nil), artifacts...)
	for index := range result {
		if artifacts[index].Recipients != nil {
			result[index].Recipients = append([]string{}, artifacts[index].Recipients...)
		}
	}
	return result, nil
}

func (c *ContextControl) planChannelFanout(
	producer domain.TaskSpec,
	tasks []taskgraph.TaskSnapshot,
	artifacts []agentprotocol.Artifact,
) ([]channelPlan, error) {
	direct := make(map[string]struct{})
	for _, candidate := range tasks {
		if candidate.Spec.WorkflowID == producer.WorkflowID && containsDependency(candidate.Spec.Dependencies, producer.ID) {
			direct[candidate.Spec.AgentID] = struct{}{}
		}
	}
	plans := make([]channelPlan, 0, len(artifacts))
	for _, artifact := range artifacts {
		targets := map[string]struct{}{producer.AgentID: {}}
		if artifact.Visibility == "workflow" {
			if len(artifact.Recipients) == 0 {
				for agentID := range direct {
					targets[agentID] = struct{}{}
				}
			} else {
				for _, agentID := range artifact.Recipients {
					if _, allowed := direct[agentID]; !allowed {
						return nil, fmt.Errorf("%w: %q", ErrOutputRecipientForbidden, agentID)
					}
					targets[agentID] = struct{}{}
				}
			}
		}
		ordered := make([]string, 0, len(targets))
		for agentID := range targets {
			if _, known := c.agents[agentID]; !known {
				return nil, fmt.Errorf("%w: %q", ErrUnknownAgent, agentID)
			}
			ordered = append(ordered, agentID)
		}
		if len(ordered) > agentprotocol.MaxRecipients+1 {
			return nil, fmt.Errorf("%w: artifact %q has %d targets", ErrOutputFanoutExceeded, artifact.Name, len(ordered))
		}
		sort.Strings(ordered)
		plans = append(plans, channelPlan{artifact: artifact, targets: ordered})
	}
	return plans, nil
}

func (c *ContextControl) channelOutputCommand(
	producer domain.TaskSpec,
	tasks []taskgraph.TaskSnapshot,
	plans []channelPlan,
) (contextstate.OutputCommand, error) {
	targetIDs := make(map[string]struct{})
	for _, plan := range plans {
		for _, agentID := range plan.targets {
			targetIDs[agentID] = struct{}{}
		}
	}
	orderedTargets := make([]string, 0, len(targetIDs))
	for agentID := range targetIDs {
		orderedTargets = append(orderedTargets, agentID)
	}
	sort.Strings(orderedTargets)
	command := contextstate.OutputCommand{
		TaskID: producer.ID, WorkflowID: producer.WorkflowID, ProducerAgentID: producer.AgentID,
		Targets:   make([]contextstate.OutputTarget, 0, len(orderedTargets)),
		Artifacts: make([]contextstate.ArtifactCommand, 0, len(plans)),
	}
	producerBudget := producer.Resources.TokenBudget
	if len(plans) != 0 && producerBudget <= 0 {
		return contextstate.OutputCommand{}, contextstate.ErrTaskContextDisabled
	}
	for _, agentID := range orderedTargets {
		snapshot, err := c.contexts.Inspect(producer.WorkflowID, agentID)
		if err != nil {
			return contextstate.OutputCommand{}, err
		}
		pinnedBudget, exists := channelPrincipalBudget(tasks, producer.WorkflowID, agentID)
		if !exists {
			return contextstate.OutputCommand{}, contextstate.ErrInvalidTaskBinding
		}
		command.Targets = append(command.Targets, contextstate.OutputTarget{
			AgentID: agentID, ExpectedManifestID: snapshot.ManifestID,
			TaskTokenBudget: producerBudget, PrincipalPinnedBudget: pinnedBudget,
		})
	}
	for _, plan := range plans {
		command.Artifacts = append(command.Artifacts, contextstate.ArtifactCommand{
			Name: plan.artifact.Name, Content: plan.artifact.Content,
			Visibility: contextstate.Visibility(plan.artifact.Visibility),
			Recipients: append([]string(nil), plan.artifact.Recipients...),
			Pin:        plan.artifact.Pin, Cold: plan.artifact.Cold,
			Targets: append([]string(nil), plan.targets...),
		})
	}
	return command, nil
}

func channelPrincipalBudget(tasks []taskgraph.TaskSnapshot, workflowID, agentID string) (int64, bool) {
	var activeBudget, fallbackBudget int64
	activeFound, fallbackFound := false, false
	for _, task := range tasks {
		if task.Spec.WorkflowID != workflowID || task.Spec.AgentID != agentID {
			continue
		}
		budget := task.Spec.Resources.TokenBudget
		if !fallbackFound || budget < fallbackBudget {
			fallbackBudget, fallbackFound = budget, true
		}
		if !task.Status.IsTerminal() && (!activeFound || budget < activeBudget) {
			activeBudget, activeFound = budget, true
		}
	}
	if activeFound {
		return activeBudget, true
	}
	return fallbackBudget, fallbackFound
}

func containsDependency(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
