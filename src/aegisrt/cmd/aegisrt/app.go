package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"aegisrt/internal/agentconfig"
	"aegisrt/internal/api"
	"aegisrt/internal/apitoken"
	"aegisrt/internal/contextstate"
	"aegisrt/internal/controlplane"
	"aegisrt/internal/domain"
	"aegisrt/internal/eventlog"
	"aegisrt/internal/executor"
	"aegisrt/internal/resourcecontrol"
	"aegisrt/internal/runservice"
	"aegisrt/internal/runtimeapp"
	"aegisrt/internal/runtimecore"
	"aegisrt/internal/statefile"
	"aegisrt/internal/webconsole"
	"aegisrt/internal/worker"
)

type daemonApp struct {
	handler           http.Handler
	service           *runservice.Service
	events            *eventlog.Log
	ready             *atomic.Bool
	kernel            *runtimecore.Runtime
	recovery          runtimecore.RecoveryReport
	stateStore        *statefile.Store
	contextStateStore *statefile.Store
	contexts          *contextstate.Repository
	agentCount        int
	runtimeNamespace  string
}

func buildDaemon(options daemonOptions) (*daemonApp, error) {
	prepared, err := prepareRuntimeOptions(options)
	if err != nil {
		return nil, fmt.Errorf("prepare Runtime options: %w", err)
	}
	options = prepared
	token, err := apitoken.Load(options.authTokenFile, true)
	if err != nil {
		return nil, fmt.Errorf("load API token: %w", err)
	}
	defer clearBytes(token)

	config, err := agentconfig.Load(options.agentsFile, agentconfig.Options{
		AgentRoot: options.agentRoot, StrictPermissions: true,
	})
	if err != nil {
		return nil, fmt.Errorf("load Agent registry: %w", err)
	}
	agents := config.Agents()
	capacity := domain.ResourceVector{
		CPUMilli: options.cpuMilli, MemoryMiB: options.memoryMiB,
		LLMSlots: options.llmSlots, TokenBudget: options.tokenBudget,
	}
	policy, err := buildSchedulingPolicy(options.schedulerPolicy, capacity)
	if err != nil {
		return nil, fmt.Errorf("create scheduling policy: %w", err)
	}
	kernel, recovery, stateStore, err := buildRuntime(options, policy, capacity, agents)
	if err != nil {
		return nil, fmt.Errorf("create runtime kernel: %w", err)
	}
	built := false
	var contextStateStore *statefile.Store
	defer func() {
		if !built {
			if contextStateStore != nil {
				_ = contextStateStore.Close()
			}
			if stateStore != nil {
				_ = stateStore.Close()
			}
		}
	}()

	legacyMigrationPending := kernel.LegacyContextMigrationPending()
	if legacyMigrationPending && !options.migrateLegacyState {
		return nil, fmt.Errorf("%w: restart with -migrate-legacy-state to finish the pending Context migration",
			contextstate.ErrMigrationRequired)
	}
	contexts, builtContextStore, err := buildContextRepository(
		options, kernel.Tasks(), agents, kernel.InstanceID(),
		!recovery.Loaded || legacyMigrationPending,
		legacyMigrationPending && options.migrateLegacyState,
	)
	if err != nil {
		return nil, fmt.Errorf("create Context repository: %w", err)
	}
	contextStateStore = builtContextStore
	if legacyMigrationPending {
		if err := kernel.CompleteLegacyContextMigration(time.Now().UTC()); err != nil {
			return nil, fmt.Errorf("complete legacy Context migration: %w", err)
		}
	}
	isolation, err := buildIsolation(options)
	if err != nil {
		return nil, err
	}
	manager := worker.NewManager(executor.MinimalHostEnvironment(), isolation)
	publisher := &outputPublisherProxy{}
	coordinator, err := controlplane.NewWithOptions(
		kernel, manager, "local-worker", agents,
		controlplane.Options{ContextResolver: contexts, OutputPublisher: publisher},
	)
	if err != nil {
		return nil, fmt.Errorf("create control plane: %w", err)
	}
	control, err := runtimeapp.NewContextControl(coordinator, contexts, agents)
	if err != nil {
		return nil, fmt.Errorf("create Runtime control: %w", err)
	}
	if err := publisher.Bind(control); err != nil {
		return nil, fmt.Errorf("bind Agent output publisher: %w", err)
	}
	if err := coordinator.RecoverOutputs(context.Background()); err != nil {
		return nil, fmt.Errorf("recover committed Agent output: %w", err)
	}
	events, err := eventlog.New(options.eventCapacity)
	if err != nil {
		return nil, fmt.Errorf("create event log: %w", err)
	}
	observer, err := runservice.NewEventObserver(events)
	if err != nil {
		return nil, fmt.Errorf("create event observer: %w", err)
	}
	service, err := runservice.New(coordinator, observer, runservice.Config{
		PollInterval: options.pollInterval, MaxConcurrent: options.maxWorkers,
	})
	if err != nil {
		return nil, fmt.Errorf("create Runtime service: %w", err)
	}
	ready := &atomic.Bool{}
	server := api.NewServer(api.Config{
		Version: version, Agents: agents, Control: control, ContextControl: control,
		Events: events, AuthToken: token, Notify: service.Notify,
		Ready: func() bool {
			return ready.Load() && coordinator.Healthy() && contexts.Healthy() && service.Stats().ObserverErrors == 0
		},
	})
	result := &daemonApp{
		handler: webconsole.New(server.Handler()), service: service, events: events, kernel: kernel,
		ready: ready, recovery: recovery, stateStore: stateStore,
		contextStateStore: contextStateStore, contexts: contexts, agentCount: len(agents),
		runtimeNamespace: options.runtimeNamespace,
	}
	built = true
	return result, nil
}

func (a *daemonApp) close() error {
	if a == nil {
		return nil
	}
	var contextErr, runtimeErr error
	if a.contextStateStore != nil {
		contextErr = a.contextStateStore.Close()
	}
	if a.stateStore != nil {
		runtimeErr = a.stateStore.Close()
	}
	return errors.Join(contextErr, runtimeErr)
}

func buildIsolation(options daemonOptions) (worker.Isolation, error) {
	if options.cgroupRoot == "" {
		return nil, nil
	}
	driver, err := resourcecontrol.NewCgroupV2Driver(options.cgroupRoot, options.runtimeNamespace)
	if err != nil {
		return nil, fmt.Errorf("create cgroup v2 driver: %w", err)
	}
	return worker.CgroupIsolation{
		Driver: driver, PIDsMax: options.pidsMax, CPUPeriodMicros: options.cpuPeriod,
	}, nil
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
