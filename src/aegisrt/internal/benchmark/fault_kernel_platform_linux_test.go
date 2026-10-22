//go:build linux

package benchmark

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aegisrt/internal/resourcecontrol"
)

func TestValidateFaultKernelRuntimeDirectoryRequiresPrivateOwnedDirectory(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := validateFaultKernelRuntimeDirectory(directory, uint32(os.Geteuid()))
	if err != nil || resolved != filepath.Clean(directory) {
		t.Fatalf("resolved=%q err=%v", resolved, err)
	}
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := validateFaultKernelRuntimeDirectory(directory, uint32(os.Geteuid())); err == nil {
		t.Fatal("public runtime directory accepted")
	}
}

func TestCountFaultKernelEscapesUsesProcessIdentityAndParentage(t *testing.T) {
	root := t.TempDir()
	procRoot := filepath.Join(root, "proc")
	group := filepath.Join(root, "group")
	if err := os.MkdirAll(group, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(group, "cgroup.procs"), []byte("101\n102\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeFaultKernelTestStat(t, procRoot, 101, 1, 1001)
	writeFaultKernelTestStat(t, procRoot, 102, 101, 1002)
	writeFaultKernelTestStat(t, procRoot, 103, 101, 1003)
	escapes, err := countFaultKernelEscapes(procRoot, group)
	if err != nil || escapes != 1 {
		t.Fatalf("escapes=%d err=%v", escapes, err)
	}
}

func TestCountLiveFaultKernelIdentitiesUsesPIDAndStartTime(t *testing.T) {
	procRoot := filepath.Join(t.TempDir(), "proc")
	writeFaultKernelTestStat(t, procRoot, 201, 1, 2001)
	identities := []faultKernelProcessIdentity{
		{pid: 201, started: 2001},
		{pid: 201, started: 1999},
	}
	live, err := countLiveFaultKernelIdentities(procRoot, identities)
	if err != nil || live != 1 {
		t.Fatalf("live=%d err=%v", live, err)
	}
	if err := os.RemoveAll(filepath.Join(procRoot, "201")); err != nil {
		t.Fatal(err)
	}
	live, err = countLiveFaultKernelIdentities(procRoot, identities)
	if err != nil || live != 0 {
		t.Fatalf("removed identity live=%d err=%v", live, err)
	}
}

func TestFaultKernelSampleCopiesAnonymousKernelObservation(t *testing.T) {
	populated, members := true, 2
	cpu, memory, pids := "10000 100000", "67108864", "64"
	observation := resourcecontrol.CgroupObservation{
		Exists: true, Populated: &populated, MemberCount: &members,
		ControllerReadback: resourcecontrol.CgroupControllerReadback{
			CPUMax: &cpu, MemoryMax: &memory, PIDsMax: &pids,
		},
	}
	sample := newFaultKernelCgroupSample(
		"run-0123456789abcdef01234567", "f4-m-7-0", "measurement", 1, 0,
		"attempt1_ready", 12, 1, observation, 0,
	)
	if !sample.Exists || sample.MemberCount == nil || *sample.MemberCount != 2 ||
		sample.ControllerReadback.CPUMax == nil || *sample.ControllerReadback.CPUMax != cpu {
		t.Fatalf("sample=%+v", sample)
	}
	populated, members, cpu = false, 0, "changed"
	if sample.Populated == nil || !*sample.Populated || *sample.MemberCount != 2 ||
		*sample.ControllerReadback.CPUMax != "10000 100000" {
		t.Fatal("sample retained mutable observation pointers")
	}
}

func TestFaultKernelMarkerRejectsInvalidContentWithoutLeakingPath(t *testing.T) {
	directory := t.TempDir()
	marker := filepath.Join(directory, "attempt-1.ready")
	if err := os.WriteFile(marker, []byte("WRONG\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ready, err := faultKernelMarkerReady(directory, "attempt-1")
	if ready || !errors.Is(err, ErrFaultKernelUnavailable) {
		t.Fatalf("ready=%v err=%v", ready, err)
	}
	if strings.Contains(err.Error(), directory) {
		t.Fatalf("readiness error leaked private path: %v", err)
	}
}

func writeFaultKernelTestStat(t *testing.T, root string, pid, parent int, started uint64) {
	t.Helper()
	directory := filepath.Join(root, faultKernelDecimal(pid))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	fields := make([]string, 20)
	fields[0], fields[1], fields[19] = "S", faultKernelDecimal(parent), faultKernelUint(started)
	for index := 2; index < 19; index++ {
		fields[index] = "0"
	}
	line := faultKernelDecimal(pid) + " (fixture process) " + strings.Join(fields, " ") + "\n"
	if err := os.WriteFile(filepath.Join(directory, "stat"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}
