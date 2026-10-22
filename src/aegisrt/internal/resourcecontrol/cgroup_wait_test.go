package resourcecontrol

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWaitCgroupEmptyObservesPopulatedZero(t *testing.T) {
	observations := []bool{true, true, false}
	reads := 0
	err := waitCgroupEmptyWithReader("cgroup.events", 2*time.Second, time.Millisecond,
		func(string) (bool, error) {
			value := observations[reads]
			reads++
			return value, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if reads != len(observations) {
		t.Fatalf("cgroup.events reads = %d, want %d", reads, len(observations))
	}
}

func TestWaitCgroupEmptyHasBoundedDeadline(t *testing.T) {
	started := time.Now()
	err := waitCgroupEmptyWithReader("cgroup.events", 5*time.Millisecond, time.Millisecond,
		func(string) (bool, error) { return true, nil })
	if err == nil || !strings.Contains(err.Error(), "remained populated") {
		t.Fatalf("wait error = %v", err)
	}
	if time.Since(started) > 100*time.Millisecond {
		t.Fatal("cgroup empty wait exceeded its bounded deadline")
	}
}

func TestReadCgroupPopulatedRequiresValidEvents(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "cgroup.events")
	for _, test := range []struct {
		name      string
		content   string
		populated bool
		wantErr   bool
	}{
		{name: "populated", content: "populated 1\nfrozen 0\n", populated: true},
		{name: "empty", content: "populated 0\nfrozen 0\n"},
		{name: "missing", content: "frozen 0\n", wantErr: true},
		{name: "invalid", content: "populated 2\n", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			populated, err := readCgroupPopulated(path)
			if test.wantErr {
				if err == nil {
					t.Fatal("invalid cgroup.events was accepted")
				}
				return
			}
			if err != nil || populated != test.populated {
				t.Fatalf("read result = (%v, %v), want (%v, nil)", populated, err, test.populated)
			}
		})
	}
}

func TestWaitCgroupEmptyPropagatesReadFailure(t *testing.T) {
	want := errors.New("read failed")
	err := waitCgroupEmptyWithReader("cgroup.events", time.Second, time.Millisecond,
		func(string) (bool, error) { return false, want })
	if !errors.Is(err, want) {
		t.Fatalf("wait error = %v, want wrapped read failure", err)
	}
}

func TestDestroyRefusesCgroupThatRemainsPopulated(t *testing.T) {
	root := t.TempDir()
	driver, err := NewCgroupV2Driver(root, "runtime-a")
	if err != nil {
		t.Fatal(err)
	}
	groupPath := filepath.Join(root, "aegisrt", "runtime-a", "busy-attempt")
	if err := os.MkdirAll(groupPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(groupPath, "cgroup.events"), []byte("populated 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = driver.destroy("busy-attempt", 5*time.Millisecond, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "remained populated") {
		t.Fatalf("Destroy error = %v", err)
	}
	if _, statErr := os.Stat(groupPath); statErr != nil {
		t.Fatalf("populated cgroup was removed: %v", statErr)
	}
}
