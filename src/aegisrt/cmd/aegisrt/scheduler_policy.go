package main

import (
	"fmt"
	"time"

	"aegisrt/internal/domain"
	"aegisrt/internal/scheduler"
)

func buildSchedulingPolicy(name string, capacity domain.ResourceVector) (scheduler.Policy, error) {
	switch name {
	case "fifo":
		return scheduler.FIFO{}, nil
	case "priority":
		return scheduler.Priority{AgingInterval: time.Second}, nil
	case "dag-drcfs":
		return scheduler.NewDAGDRCFS(capacity, time.Second)
	case "dag-drcfs-time":
		return scheduler.NewDurationDAGDRCFS(capacity, time.Second)
	default:
		return nil, fmt.Errorf("unsupported scheduler policy %q", name)
	}
}
