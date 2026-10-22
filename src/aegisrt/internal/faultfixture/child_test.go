package faultfixture

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type observedChild struct {
	startErr error
	started  bool
	killed   bool
	waited   bool
}

func (child *observedChild) Start() error {
	child.started = true
	return child.startErr
}

func (child *observedChild) Kill() error {
	child.killed = true
	return nil
}

func (child *observedChild) Wait() error {
	child.waited = true
	return nil
}

func TestSpawnChildPublishesReadyOnlyAfterStartAndReapsOnSuccess(t *testing.T) {
	directory := t.TempDir()
	child := &observedChild{}
	factory := func(hold time.Duration) (fixtureChild, error) {
		if hold != maxHold {
			t.Fatalf("child hold = %s, want %s", hold, maxHold)
		}
		return child, nil
	}
	code := run(
		context.Background(),
		[]string{"--mode=success", "--spawn-child", "--ready-dir=" + directory},
		strings.NewReader(validInput), &bytes.Buffer{}, &bytes.Buffer{}, factory,
	)
	if code != 0 {
		t.Fatalf("run exit code = %d", code)
	}
	if !child.started || !child.killed || !child.waited {
		t.Fatalf("child lifecycle = %#v", child)
	}
	if _, err := os.Stat(filepath.Join(directory, "task-1-attempt-1.ready")); err != nil {
		t.Fatal(err)
	}
}

func TestSpawnChildStartFailureDoesNotPublishReady(t *testing.T) {
	directory := t.TempDir()
	child := &observedChild{startErr: errors.New("start failed")}
	code := run(
		context.Background(),
		[]string{"--mode=success", "--spawn-child", "--ready-dir=" + directory},
		strings.NewReader(validInput), &bytes.Buffer{}, &bytes.Buffer{},
		func(time.Duration) (fixtureChild, error) { return child, nil },
	)
	if code != 1 {
		t.Fatalf("run exit code = %d", code)
	}
	if child.killed || child.waited {
		t.Fatalf("unstarted child was reaped: %#v", child)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("readiness published before Start: %#v", entries)
	}
}

func TestSpawnChildReapsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	child := &observedChild{}
	code := run(
		ctx, []string{"--mode=success", "--spawn-child", "--hold=30s"},
		strings.NewReader(validInput), &bytes.Buffer{}, &bytes.Buffer{},
		func(time.Duration) (fixtureChild, error) { return child, nil },
	)
	if code != 1 || !child.killed || !child.waited {
		t.Fatalf("code=%d child=%#v", code, child)
	}
}

func TestRunChildIsBoundedAndSilent(t *testing.T) {
	started := time.Now()
	if code := RunChild(context.Background(), []string{"--hold=1ms"}); code != 0 {
		t.Fatalf("RunChild exit code = %d", code)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("RunChild elapsed = %s", elapsed)
	}
	for _, args := range [][]string{{}, {"--hold=0"}, {"--hold=31s"}, {"--hold=1ms", "extra"}} {
		if code := RunChild(context.Background(), args); code != 2 {
			t.Fatalf("RunChild(%v) exit code = %d", args, code)
		}
	}
}

func TestFixtureChildUsesCurrentExecutableAndDiscardedStreams(t *testing.T) {
	process, err := newFixtureChild(2 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	child, ok := process.(*commandChild)
	if !ok {
		t.Fatalf("child type = %T", process)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{executable, HiddenChildCommand, "--hold=2s"}
	if strings.Join(child.command.Args, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("child args = %#v", child.command.Args)
	}
	if child.command.Stdin != nil || child.command.Stdout != io.Discard || child.command.Stderr != io.Discard {
		t.Fatal("child inherited an input or output stream")
	}
}
