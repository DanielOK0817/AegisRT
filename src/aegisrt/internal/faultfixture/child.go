package faultfixture

import (
	"context"
	"flag"
	"io"
	"os"
	"os/exec"
	"time"
)

const (
	HiddenChildCommand = "fault-agent-child"
	maxHold            = 30 * time.Second
)

type fixtureChild interface {
	Start() error
	Kill() error
	Wait() error
}

type childFactory func(time.Duration) (fixtureChild, error)

type commandChild struct {
	command *exec.Cmd
}

func (child *commandChild) Start() error {
	return child.command.Start()
}

func (child *commandChild) Kill() error {
	return child.command.Process.Kill()
}

func (child *commandChild) Wait() error {
	return child.command.Wait()
}

func newFixtureChild(hold time.Duration) (fixtureChild, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	command := exec.Command(executable, HiddenChildCommand, "--hold="+hold.String())
	command.Stdin = nil
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return &commandChild{command: command}, nil
}

func reapChild(child fixtureChild) {
	_ = child.Kill()
	_ = child.Wait()
}

// RunChild runs the private, bounded child used to make a deterministic Agent
// process tree. It deliberately performs no I/O or network operations.
func RunChild(ctx context.Context, args []string) int {
	if ctx == nil {
		return 2
	}
	flags := flag.NewFlagSet(HiddenChildCommand, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	hold := flags.Duration("hold", 0, "bounded child hold")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *hold <= 0 || *hold > maxHold {
		return 2
	}
	timer := time.NewTimer(*hold)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return 1
	case <-timer.C:
		return 0
	}
}
