package benchmark

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestFaultKernelWorkloadIsStableSafeAndBindsRuntimeConfig(t *testing.T) {
	plan, err := NewFaultKernelSmokePlan([]uint32{7, 11}, 1, FaultKernelDefaultTrialTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	first, cells, err := newFaultKernelWorkload(plan)
	if err != nil {
		t.Fatal(err)
	}
	second, secondCells, err := newFaultKernelWorkload(plan)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, err := marshalFaultKernelWorkload(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, _ := marshalFaultKernelWorkload(second)
	if !bytes.Equal(firstJSON, secondJSON) || len(cells) != 2 || len(secondCells) != 2 {
		t.Fatal("fault-kernel workload is not stable")
	}
	if err := scanAnonymous(firstJSON); err != nil {
		t.Fatal(err)
	}
	if len(first.Agents) != 2 || len(first.Cells) != 2 ||
		first.Cells[0].Target.MaxAttempts != 2 || first.Cells[0].Survivor.MaxAttempts != 1 {
		t.Fatalf("workload did not bind full Agent/task semantics: %+v", first)
	}
	configHash, err := faultKernelRuntimeConfigHash(first.RuntimeConfig)
	if err != nil || !validFaultKernelHash(configHash) {
		t.Fatalf("config hash=%q err=%v", configHash, err)
	}
	for _, cell := range cells {
		if cell.Target.MaxAttempts != 2 || cell.Survivor.MaxAttempts != 1 ||
			cell.Target.WorkflowID != cell.Survivor.WorkflowID || cell.PairID == "" {
			t.Fatalf("invalid cell: %+v", cell)
		}
	}
	mutated := first
	mutated.Cells = append([]faultKernelWorkloadCell(nil), first.Cells...)
	mutated.Cells[0].Target.Priority++
	mutatedJSON, err := marshalFaultKernelWorkload(mutated)
	if err != nil {
		t.Fatal(err)
	}
	if hashBytes(mutatedJSON) == hashBytes(firstJSON) {
		t.Fatal("task semantic drift did not change the canonical workload hash")
	}
	changedConfig := first.RuntimeConfig
	changedConfig.EventCapacity++
	changedHash, err := faultKernelRuntimeConfigHash(changedConfig)
	if err != nil || changedHash == configHash {
		t.Fatal("Runtime configuration drift did not change its hash")
	}
}

func TestFaultKernelRegistryKeepsPathsPrivateAndLoadsStrictly(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ready := t.TempDir()
	payload, err := faultKernelRegistryJSON(executable, ready)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(payload) || !bytes.Contains(payload, []byte(`"--spawn-child"`)) ||
		!bytes.Contains(payload, []byte(`"--mode=success"`)) {
		t.Fatalf("invalid registry: %s", payload)
	}
	if err := scanAnonymous(payload); err == nil {
		t.Fatal("private registry unexpectedly passed publication anonymity scan")
	}
	path := filepath.Join(ready, "agents.json")
	if err := writeFaultKernelPrivateFile(path, payload); err != nil {
		t.Fatal(err)
	}
	if err := validateFaultKernelRegistry(path, executable, ready); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("private registry mode=%v", info.Mode())
	}
}

func TestFaultKernelDaemonArgumentsAreDerivedFromHashedRuntimeConfig(t *testing.T) {
	config := fixedFaultKernelRuntimeConfig()
	paths := faultKernelTrialPaths{registry: "registry", token: "token", state: "state"}
	arguments := faultKernelDaemonArguments(paths, "auto")
	if strings.Contains(strings.Join(arguments, " "), "runtime-namespace") {
		t.Fatal("daemon arguments contain a non-public namespace override")
	}
	want := map[string]string{
		"-scheduler-policy":  config.SchedulerPolicy,
		"-cpu-milli":         strconv.FormatInt(config.Capacity.CPUMilli, 10),
		"-memory-mib":        strconv.FormatInt(config.Capacity.MemoryMiB, 10),
		"-llm-slots":         strconv.FormatInt(config.Capacity.LLMSlots, 10),
		"-token-budget":      strconv.FormatInt(config.Capacity.TokenBudget, 10),
		"-max-workers":       strconv.Itoa(config.MaxWorkers),
		"-poll-interval":     (time.Duration(config.PollIntervalMS) * time.Millisecond).String(),
		"-event-capacity":    strconv.Itoa(config.EventCapacity),
		"-pids-max":          strconv.FormatInt(config.PIDsMax, 10),
		"-cpu-period-micros": strconv.FormatInt(config.CPUPeriodMicros, 10),
		"-shutdown-timeout":  (time.Duration(config.ShutdownMS) * time.Millisecond).String(),
	}
	for flag, value := range want {
		if got, ok := faultKernelArgumentValue(arguments, flag); !ok || got != value {
			t.Fatalf("%s=%q found=%t, want %q", flag, got, ok, value)
		}
	}
}

func faultKernelArgumentValue(arguments []string, name string) (string, bool) {
	for index := 0; index+1 < len(arguments); index += 2 {
		if arguments[index] == name {
			return arguments[index+1], true
		}
	}
	return "", false
}

func TestFaultKernelComponentManifestIsCanonicalAndRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	harness := filepath.Join(directory, "harness")
	daemon := filepath.Join(directory, "daemon")
	if err := os.WriteFile(harness, []byte("harness-bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(daemon, []byte("daemon-bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	revision := strings.Repeat("a", 40)
	manifest, payload, digest, err := buildFaultKernelComponentManifestForOwner(
		harness, daemon, revision, faultKernelTestComponentOwner(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Components) != 3 || hashBytes(payload) != digest || !validFaultKernelHash(digest) {
		t.Fatalf("manifest=%+v digest=%q", manifest, digest)
	}
	link := filepath.Join(directory, "daemon-link")
	if err := os.Symlink(daemon, link); err == nil {
		if _, _, _, err := buildFaultKernelComponentManifestForOwner(
			harness, link, revision, faultKernelTestComponentOwner(),
		); err == nil {
			t.Fatal("symlinked component accepted")
		}
	}
}
