package benchmark

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"
)

const (
	resourceCPUFixtureDuration = 700 * time.Millisecond
	resourceMemoryFixtureBytes = 256 << 20
	resourceMemoryFixtureHold  = 800 * time.Millisecond
	resourcePIDFixtureChildren = 24
	resourcePIDFixtureHold     = 900 * time.Millisecond
)

func RunResourceKernelFixture(ctx context.Context, args []string, stderr io.Writer) int {
	if ctx == nil || len(args) != 1 {
		fmt.Fprintln(stderr, "invalid resource fixture invocation")
		return 2
	}
	switch args[0] {
	case "cpu":
		return runResourceCPUFixture(ctx, stderr)
	case "memory":
		const chunkBytes = 8 << 20
		chunks := make([][]byte, 0, resourceMemoryFixtureBytes/chunkBytes)
		for allocated := 0; allocated < resourceMemoryFixtureBytes; allocated += chunkBytes {
			chunk := make([]byte, chunkBytes)
			for index := 0; index < len(chunk); index += 4096 {
				chunk[index] = byte(index)
			}
			chunks = append(chunks, chunk)
		}
		select {
		case <-ctx.Done():
			return 1
		case <-time.After(resourceMemoryFixtureHold):
		}
		runtime.KeepAlive(chunks)
		return 0
	case "pids":
		return runResourcePIDFixture(ctx, stderr)
	case "hold":
		select {
		case <-ctx.Done():
			return 1
		case <-time.After(resourcePIDFixtureHold):
			return 0
		}
	default:
		fmt.Fprintln(stderr, "unknown resource fixture mode")
		return 2
	}
}

func runResourceCPUFixture(ctx context.Context, stderr io.Writer) int {
	runtime.GOMAXPROCS(1)
	started, ok := resourceProcessCPUTime()
	if !ok {
		fmt.Fprintln(stderr, "process CPU clock is unavailable")
		return 1
	}
	target := started + resourceCPUFixtureDuration
	var value uint64 = 1
	for {
		for iteration := 0; iteration < 1<<20; iteration++ {
			value = value*1664525 + 1013904223
		}
		select {
		case <-ctx.Done():
			return 1
		default:
		}
		current, available := resourceProcessCPUTime()
		if !available {
			fmt.Fprintln(stderr, "process CPU clock became unavailable")
			return 1
		}
		if current >= target {
			runtime.KeepAlive(value)
			return 0
		}
	}
}

func runResourcePIDFixture(ctx context.Context, stderr io.Writer) int {
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "resolve fixture executable failed")
		return 1
	}
	children := make([]*exec.Cmd, 0, resourcePIDFixtureChildren)
	for index := 0; index < resourcePIDFixtureChildren; index++ {
		command := exec.CommandContext(ctx, executable, "resource-fixture", "hold")
		if err := command.Start(); err != nil {
			continue
		}
		children = append(children, command)
	}
	for _, child := range children {
		_ = child.Wait()
	}
	return 0
}
