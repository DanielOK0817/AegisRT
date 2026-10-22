package main

import (
	"context"
	"errors"
	"testing"

	"aegisrt/internal/agentprotocol"
	"aegisrt/internal/controlplane"
)

func TestOutputPublisherProxyRequiresBinding(t *testing.T) {
	proxy := &outputPublisherProxy{}
	err := proxy.PublishAgentOutput(context.Background(), "task-1", 1, []agentprotocol.Artifact{{
		Name: "result", Content: "value", Visibility: "agent",
	}})
	if !errors.Is(err, controlplane.ErrOutputPublisherRequired) {
		t.Fatalf("PublishAgentOutput error = %v, want ErrOutputPublisherRequired", err)
	}
}

func TestOutputPublisherProxyBindsExactlyOnce(t *testing.T) {
	proxy := &outputPublisherProxy{}
	target := &recordingOutputPublisher{}
	if err := proxy.Bind(target); err != nil {
		t.Fatal(err)
	}
	if err := proxy.Bind(target); !errors.Is(err, errOutputPublisherBound) {
		t.Fatalf("second Bind error = %v, want errOutputPublisherBound", err)
	}
	artifacts := []agentprotocol.Artifact{{Name: "result", Content: "value", Visibility: "agent"}}
	if err := proxy.PublishAgentOutput(context.Background(), "task-1", 1, artifacts); err != nil {
		t.Fatal(err)
	}
	if target.taskID != "task-1" || len(target.artifacts) != 1 || target.artifacts[0].Content != "value" {
		t.Fatalf("unexpected delegation: %+v", target)
	}
}

type recordingOutputPublisher struct {
	taskID    string
	artifacts []agentprotocol.Artifact
}

func (p *recordingOutputPublisher) PublishAgentOutput(
	_ context.Context,
	taskID string,
	attempt int,
	artifacts []agentprotocol.Artifact,
) error {
	p.taskID = taskID
	p.artifacts = artifacts
	return nil
}
