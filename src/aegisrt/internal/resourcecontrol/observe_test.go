package resourcecontrol

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCgroupV2ObserveReturnsAnonymousReadback(t *testing.T) {
	driver, groupPath := observationFixture(t, "attempt-observe")
	writeFixture(t, filepath.Join(groupPath, "cgroup.events"), "populated 1\nfrozen 0\n")
	writeFixture(t, filepath.Join(groupPath, "cgroup.procs"), "41273\n41274\n")

	observation, err := driver.Observe("attempt-observe")
	if err != nil {
		t.Fatal(err)
	}
	if !observation.Exists || observation.Populated == nil || !*observation.Populated ||
		observation.MemberCount == nil || *observation.MemberCount != 2 {
		t.Fatalf("unexpected observation: %+v", observation)
	}
	readback := observation.ControllerReadback
	if readback.CPUMax == nil || *readback.CPUMax != "50000 100000" ||
		readback.MemoryMax == nil || *readback.MemoryMax != "67108864" ||
		readback.PIDsMax == nil || *readback.PIDsMax != "8" {
		t.Fatalf("unexpected controller readback: %+v", readback)
	}

	encoded, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"41273", "41274", filepath.ToSlash(groupPath), `"path"`, `"pid"`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("observation exposes forbidden process or path data %q: %s", forbidden, text)
		}
	}
}

func TestCgroupV2ObserveMissingGroupUsesExplicitNullSemantics(t *testing.T) {
	driver, err := NewCgroupV2Driver(t.TempDir(), "runtime-observe")
	if err != nil {
		t.Fatal(err)
	}
	observation, err := driver.Observe("missing-attempt")
	if err != nil {
		t.Fatal(err)
	}
	if observation.Exists || observation.Populated != nil || observation.MemberCount != nil ||
		observation.ControllerReadback.CPUMax != nil || observation.ControllerReadback.MemoryMax != nil ||
		observation.ControllerReadback.PIDsMax != nil {
		t.Fatalf("missing observation has ambiguous values: %+v", observation)
	}
	encoded, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"populated":null`, `"member_count":null`, `"cpu_max":null`, `"memory_max":null`, `"pids_max":null`} {
		if !strings.Contains(string(encoded), field) {
			t.Fatalf("missing observation omits explicit null field %s: %s", field, encoded)
		}
	}
}

func TestCgroupV2ObserveRejectsInvalidAttemptID(t *testing.T) {
	driver, err := NewCgroupV2Driver(t.TempDir(), "runtime-observe")
	if err != nil {
		t.Fatal(err)
	}
	for _, attemptID := range []string{"", "../escape", "nested/attempt", "space attempt"} {
		if _, err := driver.Observe(attemptID); !errors.Is(err, ErrInvalidAttemptID) {
			t.Fatalf("Observe(%q) error = %v, want ErrInvalidAttemptID", attemptID, err)
		}
	}
}

func TestCgroupV2ObserveFailsClosedOnMalformedKernelFiles(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		content string
	}{
		{name: "events missing populated", file: "cgroup.events", content: "frozen 0\n"},
		{name: "events invalid populated", file: "cgroup.events", content: "populated 2\n"},
		{name: "events duplicate populated", file: "cgroup.events", content: "populated 1\npopulated 0\n"},
		{name: "process non numeric", file: "cgroup.procs", content: "not-a-pid\n"},
		{name: "process zero", file: "cgroup.procs", content: "0\n"},
		{name: "process duplicate", file: "cgroup.procs", content: "42\n42\n"},
		{name: "cpu missing period", file: "cpu.max", content: "max\n"},
		{name: "cpu zero period", file: "cpu.max", content: "50000 0\n"},
		{name: "memory negative", file: "memory.max", content: "-1\n"},
		{name: "pids extra field", file: "pids.max", content: "8 9\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			driver, groupPath := observationFixture(t, "attempt-malformed")
			writeFixture(t, filepath.Join(groupPath, "cgroup.events"), "populated 1\n")
			writeFixture(t, filepath.Join(groupPath, "cgroup.procs"), "42\n")
			writeFixture(t, filepath.Join(groupPath, test.file), test.content)
			if _, err := driver.Observe("attempt-malformed"); err == nil {
				t.Fatal("Observe accepted malformed kernel data")
			}
		})
	}
}

func TestCgroupV2ObserveFailsClosedOnMissingKernelFile(t *testing.T) {
	driver, groupPath := observationFixture(t, "attempt-missing-file")
	writeFixture(t, filepath.Join(groupPath, "cgroup.events"), "populated 0\n")
	writeFixture(t, filepath.Join(groupPath, "cgroup.procs"), "")
	if err := os.Remove(filepath.Join(groupPath, "memory.max")); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Observe("attempt-missing-file"); err == nil {
		t.Fatal("Observe accepted a partial existing cgroup")
	} else if strings.Contains(err.Error(), groupPath) {
		t.Fatalf("Observe error exposes an absolute cgroup path: %v", err)
	}
}

func observationFixture(t *testing.T, attemptID string) (*CgroupV2Driver, string) {
	t.Helper()
	driver, err := NewCgroupV2Driver(t.TempDir(), "runtime-observe")
	if err != nil {
		t.Fatal(err)
	}
	groupPath, err := driver.Create(attemptID, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(groupPath, "cpu.max"), "50000 100000\n")
	writeFixture(t, filepath.Join(groupPath, "memory.max"), "67108864\n")
	writeFixture(t, filepath.Join(groupPath, "pids.max"), "8\n")
	return driver, groupPath
}
