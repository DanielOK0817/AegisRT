//go:build linux

package benchmark

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"aegisrt/internal/resourcecontrol"
)

type faultKernelProcess struct {
	pid, parent int
	started     uint64
}

type faultKernelProcessIdentity struct {
	pid     int
	started uint64
}

func countFaultKernelEscapes(procRoot, groupPath string) (int, error) {
	members, err := readFaultKernelPIDs(filepath.Join(groupPath, "cgroup.procs"))
	if err != nil || len(members) == 0 {
		return 0, faultKernelDaemonFailure("process-members")
	}
	processes, err := snapshotFaultKernelProcesses(procRoot)
	if err != nil {
		return 0, err
	}
	memberSet := make(map[int]uint64, len(members))
	for _, pid := range members {
		process, exists := processes[pid]
		if !exists {
			return 0, faultKernelDaemonFailure("process-identity")
		}
		memberSet[pid] = process.started
	}
	escapes := 0
	for pid := range processes {
		if _, member := memberSet[pid]; member {
			continue
		}
		for parent, hops := processes[pid].parent, 0; parent > 0 && hops < 4096; hops++ {
			if _, member := memberSet[parent]; member {
				escapes++
				break
			}
			ancestor, exists := processes[parent]
			if !exists || ancestor.parent == parent {
				break
			}
			parent = ancestor.parent
		}
		if escapes > 4096 {
			return 0, faultKernelDaemonFailure("process-escape-bound")
		}
	}
	confirmed, err := readFaultKernelPIDs(filepath.Join(groupPath, "cgroup.procs"))
	if err != nil || len(confirmed) != len(members) {
		return 0, faultKernelDaemonFailure("process-identity")
	}
	for index, pid := range confirmed {
		if pid != members[index] {
			return 0, faultKernelDaemonFailure("process-identity")
		}
		current, readErr := readFaultKernelProcess(filepath.Join(procRoot, faultKernelDecimal(pid), "stat"))
		if readErr != nil || current.started != memberSet[pid] {
			return 0, faultKernelDaemonFailure("process-identity")
		}
	}
	return escapes, nil
}

func snapshotFaultKernelProcesses(root string) (map[int]faultKernelProcess, error) {
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) > faultKernelMaximumProcEntries {
		return nil, faultKernelDaemonFailure("process-snapshot")
	}
	result := make(map[int]faultKernelProcess)
	for _, entry := range entries {
		pid, parseErr := strconv.Atoi(entry.Name())
		if parseErr != nil || pid <= 0 || !entry.IsDir() {
			continue
		}
		process, readErr := readFaultKernelProcess(filepath.Join(root, entry.Name(), "stat"))
		if readErr == nil && process.pid == pid {
			result[pid] = process
		}
	}
	return result, nil
}

func readFaultKernelProcess(path string) (faultKernelProcess, error) {
	file, err := os.Open(path)
	if err != nil {
		return faultKernelProcess{}, err
	}
	content, err := io.ReadAll(io.LimitReader(file, 4097))
	closeErr := file.Close()
	if err != nil || closeErr != nil || len(content) == 0 || len(content) > 4096 {
		return faultKernelProcess{}, errors.New("invalid process stat")
	}
	text := strings.TrimSpace(string(content))
	open, close := strings.Index(text, " ("), strings.LastIndex(text, ") ")
	if open <= 0 || close <= open {
		return faultKernelProcess{}, errors.New("invalid process stat")
	}
	pid, err := strconv.Atoi(text[:open])
	fields := strings.Fields(text[close+2:])
	if err != nil || pid <= 0 || len(fields) < 20 {
		return faultKernelProcess{}, errors.New("invalid process stat")
	}
	parent, parentErr := strconv.Atoi(fields[1])
	started, startErr := strconv.ParseUint(fields[19], 10, 64)
	if parentErr != nil || startErr != nil || parent < 0 || started == 0 {
		return faultKernelProcess{}, errors.New("invalid process stat")
	}
	return faultKernelProcess{pid: pid, parent: parent, started: started}, nil
}

func readFaultKernelPIDs(path string) ([]int, error) {
	content, err := os.ReadFile(path)
	if err != nil || len(content) > 64<<10 {
		return nil, errors.New("invalid cgroup process list")
	}
	seen := make(map[int]bool)
	for _, field := range strings.Fields(string(content)) {
		pid, parseErr := strconv.Atoi(field)
		if parseErr != nil || pid <= 0 || seen[pid] || len(seen) >= 4096 {
			return nil, errors.New("invalid cgroup process list")
		}
		seen[pid] = true
	}
	result := make([]int, 0, len(seen))
	for pid := range seen {
		result = append(result, pid)
	}
	sort.Ints(result)
	return result, nil
}

func captureFaultKernelIdentities(procRoot, groupPath string) ([]faultKernelProcessIdentity, error) {
	pids, err := readFaultKernelPIDs(filepath.Join(groupPath, "cgroup.procs"))
	if err != nil || len(pids) == 0 {
		return nil, faultKernelDaemonFailure("process-identity")
	}
	result := make([]faultKernelProcessIdentity, 0, len(pids))
	for _, pid := range pids {
		process, err := readFaultKernelProcess(filepath.Join(procRoot, faultKernelDecimal(pid), "stat"))
		if err != nil || process.pid != pid {
			return nil, faultKernelDaemonFailure("process-identity")
		}
		result = append(result, faultKernelProcessIdentity{pid: pid, started: process.started})
	}
	return result, nil
}

func countLiveFaultKernelIdentities(procRoot string, identities []faultKernelProcessIdentity) (int, error) {
	if len(identities) == 0 || len(identities) > 4096 {
		return 0, faultKernelDaemonFailure("process-identity")
	}
	live := 0
	for _, identity := range identities {
		process, err := readFaultKernelProcess(filepath.Join(procRoot, faultKernelDecimal(identity.pid), "stat"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, faultKernelDaemonFailure("process-identity")
		}
		if process.started == identity.started {
			live++
		}
	}
	return live, nil
}

func faultKernelDecimal(value int) string { return strconv.Itoa(value) }
func faultKernelUint(value uint64) string { return strconv.FormatUint(value, 10) }

func newFaultKernelCgroupSample(
	runID, pairID, phase string, sequence, order int, stage string, monotonicNS int64,
	attempt int, observation resourcecontrol.CgroupObservation, escapes int,
) FaultKernelCgroupSample {
	return FaultKernelCgroupSample{
		SchemaVersion: FaultKernelCgroupSampleSchemaV1, RunID: runID, PairID: pairID,
		TrialOrder: order, Phase: phase, SampleSequence: sequence, Stage: stage,
		MonotonicNS: monotonicNS, AttemptNumber: attempt, Exists: observation.Exists,
		Populated: cloneFaultKernelBool(observation.Populated), MemberCount: cloneFaultKernelInt(observation.MemberCount),
		EscapedDescendantCount: escapes, ControllerReadback: FaultKernelControllerReadback{
			CPUMax:    cloneFaultKernelString(observation.ControllerReadback.CPUMax),
			MemoryMax: cloneFaultKernelString(observation.ControllerReadback.MemoryMax),
			PIDsMax:   cloneFaultKernelString(observation.ControllerReadback.PIDsMax),
		},
	}
}

func faultKernelObservationFromSample(sample FaultKernelCgroupSample) faultKernelAttemptObservation {
	result := faultKernelAttemptObservation{exists: sample.Exists, escapes: sample.EscapedDescendantCount}
	result.kernel = resourcecontrol.CgroupObservation{Exists: sample.Exists,
		Populated: cloneFaultKernelBool(sample.Populated), MemberCount: cloneFaultKernelInt(sample.MemberCount),
		ControllerReadback: resourcecontrol.CgroupControllerReadback{
			CPUMax:    cloneFaultKernelString(sample.ControllerReadback.CPUMax),
			MemoryMax: cloneFaultKernelString(sample.ControllerReadback.MemoryMax),
			PIDsMax:   cloneFaultKernelString(sample.ControllerReadback.PIDsMax),
		},
	}
	if sample.Populated != nil {
		result.populated = *sample.Populated
	}
	if sample.MemberCount != nil {
		result.members = *sample.MemberCount
	}
	return result
}
