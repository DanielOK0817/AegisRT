package controlplane

import (
	"aegisrt/internal/runtimecore"
	"aegisrt/internal/taskgraph"
)

// Cancel requests interruption for an active task. The Tick that owns the
// worker performs the terminal transition after the process exits. Queued
// tasks are canceled synchronously.
func (c *Coordinator) Cancel(taskID string) (taskgraph.TaskSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cancel, active := c.active[taskID]; active {
		snapshot, err := c.runtime.RequestCancel(taskID)
		if err != nil {
			return taskgraph.TaskSnapshot{}, err
		}
		cancel()
		return snapshot, nil
	}
	return c.runtime.Cancel(taskID)
}

// Tasks exposes immutable snapshots for the HTTP control surface.
func (c *Coordinator) Tasks() []taskgraph.TaskSnapshot {
	return c.runtime.Tasks()
}

// Stats exposes a point-in-time runtime resource view.
func (c *Coordinator) Stats() runtimecore.Stats {
	return c.runtime.Stats()
}

func (c *Coordinator) Healthy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.publicationErr == nil && c.runtime.Healthy()
}
