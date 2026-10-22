package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"aegisrt/internal/controlplane"
	"aegisrt/internal/domain"
	"aegisrt/internal/resourcecontrol"
	"aegisrt/internal/runtimecore"
	"aegisrt/internal/scheduler"
	"aegisrt/internal/statefile"
)

type runtimeIdentity struct {
	FormatVersion    int                   `json:"format_version"`
	Policy           string                `json:"policy"`
	Capacity         domain.ResourceVector `json:"capacity"`
	Agents           []domain.AgentSpec    `json:"agents"`
	MaxWorkers       int                   `json:"max_workers"`
	CgroupRoot       string                `json:"cgroup_root,omitempty"`
	RuntimeNamespace string                `json:"runtime_namespace,omitempty"`
	PIDsMax          int64                 `json:"pids_max"`
	CPUPeriodMicros  int64                 `json:"cpu_period_micros"`
}

func buildRuntime(
	options daemonOptions,
	policy scheduler.Policy,
	capacity domain.ResourceVector,
	agents []domain.AgentSpec,
) (*runtimecore.Runtime, runtimecore.RecoveryReport, *statefile.Store, error) {
	if options.ephemeral {
		kernel, err := runtimecore.New(policy, capacity)
		return kernel, runtimecore.RecoveryReport{}, nil, err
	}
	store, err := statefile.OpenExclusive(options.stateFile)
	if err != nil {
		return nil, runtimecore.RecoveryReport{}, nil, fmt.Errorf("open Runtime state store: %w", err)
	}
	fingerprint, err := runtimeConfigFingerprint(policy.Name(), capacity, agents, options)
	if err != nil {
		return nil, runtimecore.RecoveryReport{}, nil, errors.Join(err, store.Close())
	}
	recoveryHook, err := daemonRecoveryHook(options)
	if err != nil {
		return nil, runtimecore.RecoveryReport{}, nil, errors.Join(err, store.Close())
	}
	kernel, report, err := runtimecore.NewPersistentWithOptions(
		policy, capacity, fingerprint, store, time.Now().UTC(), runtimecore.PersistentOptions{
			AllowLegacyV1Migration: options.migrateLegacyState,
			RecoveryHook:           recoveryHook,
		},
	)
	if err != nil {
		return nil, runtimecore.RecoveryReport{}, nil,
			errors.Join(fmt.Errorf("restore Runtime state: %w", err), store.Close())
	}
	if err := ensureRecoveryNamespaceEmpty(options); err != nil {
		return nil, runtimecore.RecoveryReport{}, nil,
			errors.Join(fmt.Errorf("validate Runtime cgroup namespace: %w", err), store.Close())
	}
	return kernel, report, store, nil
}

func ensureRecoveryNamespaceEmpty(options daemonOptions) error {
	if options.cgroupRoot == "" {
		return nil
	}
	driver, err := resourcecontrol.NewCgroupV2Driver(options.cgroupRoot, options.runtimeNamespace)
	if err != nil {
		return err
	}
	return driver.EnsureNamespaceEmpty()
}

func daemonRecoveryHook(options daemonOptions) (runtimecore.RecoveryHook, error) {
	if options.cgroupRoot == "" {
		return func(string, int) error {
			return fmt.Errorf("active-attempt recovery requires cgroup isolation")
		}, nil
	}
	driver, err := resourcecontrol.NewCgroupV2Driver(options.cgroupRoot, options.runtimeNamespace)
	if err != nil {
		return nil, fmt.Errorf("create recovery cgroup driver: %w", err)
	}
	return func(taskID string, attemptNumber int) error {
		return driver.Destroy(controlplane.AttemptID(taskID, attemptNumber))
	}, nil
}

func runtimeConfigFingerprint(
	policy string,
	capacity domain.ResourceVector,
	agents []domain.AgentSpec,
	options daemonOptions,
) (string, error) {
	ordered := make([]domain.AgentSpec, len(agents))
	for index, agent := range agents {
		ordered[index] = cloneIdentityAgent(agent)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	cgroupRoot := ""
	if options.cgroupRoot != "" {
		if options.runtimeNamespace == "" {
			return "", errors.New("runtime namespace is required with cgroup isolation")
		}
		var err error
		cgroupRoot, err = filepath.Abs(options.cgroupRoot)
		if err != nil {
			return "", fmt.Errorf("resolve cgroup root identity: %w", err)
		}
		cgroupRoot = filepath.Clean(cgroupRoot)
	}
	payload, err := json.Marshal(runtimeIdentity{
		FormatVersion: 1, Policy: policy, Capacity: capacity.Clone(), Agents: ordered,
		MaxWorkers: options.maxWorkers, CgroupRoot: cgroupRoot,
		RuntimeNamespace: options.runtimeNamespace,
		PIDsMax:          options.pidsMax, CPUPeriodMicros: options.cpuPeriod,
	})
	if err != nil {
		return "", fmt.Errorf("encode Runtime configuration identity: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func withRuntimeNamespace(options daemonOptions) (daemonOptions, error) {
	if options.cgroupRoot == "" || options.runtimeNamespace != "" {
		return options, nil
	}
	var identity []byte
	if options.ephemeral {
		identity = make([]byte, 16)
		if _, err := rand.Read(identity); err != nil {
			return daemonOptions{}, fmt.Errorf("generate ephemeral namespace: %w", err)
		}
	} else {
		namespace, err := resourcecontrol.RuntimeNamespaceForStateFile(options.stateFile)
		if err != nil {
			return daemonOptions{}, fmt.Errorf("resolve state-file identity: %w", err)
		}
		options.runtimeNamespace = namespace
		return options, nil
	}
	digest := sha256.Sum256(identity)
	options.runtimeNamespace = "rt-" + hex.EncodeToString(digest[:12])
	return options, nil
}

func cloneIdentityAgent(agent domain.AgentSpec) domain.AgentSpec {
	result := agent
	result.Args = append([]string(nil), agent.Args...)
	result.Capabilities = append([]string(nil), agent.Capabilities...)
	result.Resources = agent.Resources.Clone()
	return result
}
