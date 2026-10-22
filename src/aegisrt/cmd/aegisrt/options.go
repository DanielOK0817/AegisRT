package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"runtime"
	"strings"
	"time"
)

const (
	maximumWorkers       = 256
	minimumPollInterval  = 10 * time.Millisecond
	maximumEventCapacity = 1_000_000
)

type daemonOptions struct {
	listenAddress      string
	showVersion        bool
	cpuMilli           int64
	memoryMiB          int64
	llmSlots           int64
	tokenBudget        int64
	agentsFile         string
	authTokenFile      string
	stateFile          string
	ephemeral          bool
	migrateLegacyState bool
	agentRoot          string
	maxWorkers         int
	pollInterval       time.Duration
	schedulerPolicy    string
	eventCapacity      int
	contextCounter     string
	contextCompression string
	cgroupRoot         string
	pidsMax            int64
	cpuPeriod          int64
	shutdownTimeout    time.Duration
	runtimeNamespace   string
	startupReceiptFD   int
}

func parseOptions(args []string, stderr io.Writer) (daemonOptions, error) {
	flags := flag.NewFlagSet("aegisrt", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var options daemonOptions
	flags.StringVar(&options.listenAddress, "listen", "127.0.0.1:8080", "HTTP listen address")
	flags.BoolVar(&options.showVersion, "version", false, "print version and exit")
	flags.Int64Var(&options.cpuMilli, "cpu-milli", 4000, "runtime CPU capacity in millicores")
	flags.Int64Var(&options.memoryMiB, "memory-mib", 8192, "runtime memory capacity in MiB")
	flags.Int64Var(&options.llmSlots, "llm-slots", 4, "maximum concurrent LLM calls")
	flags.Int64Var(&options.tokenBudget, "token-budget", 100000, "admission token budget")
	flags.StringVar(&options.agentsFile, "agents-file", "", "local immutable Agent registry JSON")
	flags.StringVar(&options.authTokenFile, "auth-token-file", "", "restricted API bearer-token file")
	flags.StringVar(&options.stateFile, "state-file", "", "durable Runtime state file")
	flags.BoolVar(&options.ephemeral, "ephemeral", false, "run without durable Runtime state")
	flags.BoolVar(&options.migrateLegacyState, "migrate-legacy-state", false,
		"explicitly migrate a v1 Runtime and create empty Context heads")
	flags.StringVar(&options.agentRoot, "agent-root", "", "optional root containing Agent executables")
	flags.IntVar(&options.maxWorkers, "max-workers", 4, "maximum concurrent Agent executions")
	flags.DurationVar(&options.pollInterval, "poll-interval", 250*time.Millisecond, "scheduler idle poll interval")
	flags.StringVar(&options.schedulerPolicy, "scheduler-policy", "dag-drcfs",
		"scheduler policy: fifo, priority, dag-drcfs, or dag-drcfs-time")
	flags.IntVar(&options.eventCapacity, "event-capacity", 4096, "retained in-memory runtime events")
	flags.StringVar(&options.contextCounter, "context-counter", "whitespace-v1", "local budget counter: whitespace-v1 or unicode-segment-v1")
	flags.StringVar(&options.contextCompression, "context-compression", "none-v1", "local cold-page compression: none-v1 or deterministic-extractive-v1")
	flags.StringVar(&options.cgroupRoot, "cgroup-root", "",
		"optional prepared cgroup v2 root, or auto for a delegated systemd service")
	flags.Int64Var(&options.pidsMax, "pids-max", 256, "per-Agent process limit when cgroups are enabled")
	flags.Int64Var(&options.cpuPeriod, "cpu-period-micros", 100_000, "cgroup CPU quota period")
	flags.DurationVar(&options.shutdownTimeout, "shutdown-timeout", 10*time.Second, "graceful shutdown limit")
	flags.IntVar(&options.startupReceiptFD, "startup-receipt-fd", startupReceiptDisabledFD,
		"optional inherited descriptor for one local supervisor startup receipt")
	if err := flags.Parse(args); err != nil {
		return daemonOptions{}, err
	}
	if flags.NArg() != 0 {
		return daemonOptions{}, fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	if err := validateStartupReceiptFD(options.startupReceiptFD); err != nil {
		return daemonOptions{}, err
	}
	if options.showVersion {
		return options, nil
	}
	if err := options.validate(); err != nil {
		return daemonOptions{}, err
	}
	return options, nil
}

func (o daemonOptions) validate() error {
	if _, _, err := localContextComponents(o); err != nil {
		return err
	}
	if err := validateStartupReceiptFD(o.startupReceiptFD); err != nil {
		return err
	}
	if err := validateLoopbackAddress(o.listenAddress); err != nil {
		return err
	}
	if strings.TrimSpace(o.agentsFile) == "" || strings.TrimSpace(o.agentsFile) != o.agentsFile {
		return fmt.Errorf("agents-file is required")
	}
	if strings.TrimSpace(o.authTokenFile) == "" || strings.TrimSpace(o.authTokenFile) != o.authTokenFile {
		return fmt.Errorf("auth-token-file is required")
	}
	if o.stateFile != "" && strings.TrimSpace(o.stateFile) != o.stateFile {
		return fmt.Errorf("state-file must not have surrounding whitespace")
	}
	if (o.stateFile == "") == !o.ephemeral {
		return fmt.Errorf("exactly one of state-file or ephemeral is required")
	}
	if o.migrateLegacyState && o.ephemeral {
		return fmt.Errorf("migrate-legacy-state requires durable state-file mode")
	}
	if o.maxWorkers <= 0 || o.maxWorkers > maximumWorkers {
		return fmt.Errorf("max-workers must be between 1 and %d", maximumWorkers)
	}
	if o.pollInterval < minimumPollInterval {
		return fmt.Errorf("poll-interval must be at least %s", minimumPollInterval)
	}
	if !supportedSchedulerPolicy(o.schedulerPolicy) {
		return fmt.Errorf("scheduler-policy must be one of: fifo, priority, dag-drcfs, dag-drcfs-time")
	}
	if o.eventCapacity <= 0 || o.eventCapacity > maximumEventCapacity {
		return fmt.Errorf("event-capacity must be between 1 and %d", maximumEventCapacity)
	}
	if o.pidsMax < 0 || o.cpuPeriod < 0 || o.shutdownTimeout <= 0 {
		return fmt.Errorf("pids-max and cpu-period must be non-negative; shutdown-timeout must be positive")
	}
	if o.cgroupRoot != "" && runtime.GOOS != "linux" {
		return fmt.Errorf("cgroup-root is supported only on Linux")
	}
	return nil
}

func supportedSchedulerPolicy(name string) bool {
	switch name {
	case "fifo", "priority", "dag-drcfs", "dag-drcfs-time":
		return true
	default:
		return false
	}
}

func validateLoopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", address, err)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("listen address must use a loopback host")
	}
	return nil
}
