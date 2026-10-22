package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"aegisrt/internal/benchmark"
	"aegisrt/internal/faultfixture"
)

type commandKind string

const (
	commandScheduler      commandKind = "scheduler"
	commandContext        commandKind = "context"
	commandFault          commandKind = "fault"
	commandFaultKernel    commandKind = "fault-kernel"
	commandResourceKernel commandKind = "resource-kernel"
	commandCommunication  commandKind = "communication"

	hiddenFaultFixtureCommand    = "fault-agent-fixture"
	hiddenFaultChildCommand      = faultfixture.HiddenChildCommand
	hiddenResourceFixtureCommand = "resource-fixture"
)

type cliOptions struct {
	kind               commandKind
	output             string
	seeds              []uint32
	repetitions        int
	profile            string
	schedulerPlan      benchmark.SchedulerPlan
	faultKernelPlan    benchmark.FaultKernelPlan
	resourceKernelPlan benchmark.ResourceKernelPlan
	communicationPlan  benchmark.CommunicationPlan
}

const usageText = `usage:
  aegisbench scheduler --profile smoke --output <existing-parent> --seeds <comma-list> --repetitions <n>
  aegisbench scheduler --profile formal --output <existing-parent>
  aegisbench context   --output <existing-parent> --seeds <comma-list> --repetitions <n>
  aegisbench fault     --output <existing-parent> --seeds <comma-list> --repetitions <n>
  aegisbench fault-kernel --profile smoke --output <existing-parent> --seeds <comma-list> --repetitions <n>
  aegisbench fault-kernel --profile formal --output <existing-parent>
  aegisbench resource-kernel --profile smoke --output <existing-parent>
  aegisbench communication --profile smoke --output <existing-parent>

Collects deterministic scheduler, ContextVM, or fault-containment evidence on
Windows, or kernel evidence in a dedicated openEuler service. Scheduler offers
a fixed formal S1/S2/S3 profile; other functional smoke commands are not
complete PLAN.md runs.`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), terminationSignals()...)
	defer stop()
	if exitCode, handled := runHiddenCommand(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); handled {
		stop()
		os.Exit(exitCode)
	}
	options, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	var summary benchmark.RunSummary
	switch options.kind {
	case commandCommunication:
		summary, err = benchmark.CollectCommunication(ctx, benchmark.CommunicationOptions{
			OutputDir: options.output, SourceSHA256: communicationSourceSHA256, Plan: options.communicationPlan,
		})
	case commandScheduler:
		summary, err = benchmark.CollectScheduler(ctx, benchmark.CollectOptions{
			OutputDir: options.output,
			Plan:      options.schedulerPlan,
		})
	case commandContext:
		summary, err = benchmark.CollectContext(ctx, benchmark.ContextCollectOptions{
			OutputDir: options.output,
			Plan:      benchmark.ContextPlan{Seeds: options.seeds, Repetitions: options.repetitions},
		})
	case commandFault:
		var executable string
		executable, err = os.Executable()
		if err == nil {
			summary, err = benchmark.CollectFault(ctx, benchmark.FaultCollectOptions{
				OutputDir: options.output,
				Plan:      benchmark.FaultPlan{Seeds: options.seeds, Repetitions: options.repetitions},
				Agent:     faultAgentCommand(executable),
			})
		}
	case commandFaultKernel:
		summary, err = benchmark.CollectFaultKernel(ctx, benchmark.FaultKernelCollectOptions{
			OutputDir: options.output,
			Plan:      options.faultKernelPlan,
		})
	case commandResourceKernel:
		summary, err = benchmark.CollectResourceKernel(ctx, benchmark.ResourceKernelCollectOptions{
			OutputDir: options.output, Plan: options.resourceKernelPlan,
		})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, collectionErrorMessage(err))
		os.Exit(1)
	}
	fmt.Println(evidencePublishedMessage(options.kind, summary.RunID))
}

func terminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}

func runHiddenCommand(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	switch args[0] {
	case hiddenFaultFixtureCommand:
		return faultfixture.Run(ctx, args[1:], stdin, stdout, stderr), true
	case hiddenFaultChildCommand:
		return faultfixture.RunChild(ctx, args[1:]), true
	case hiddenResourceFixtureCommand:
		return benchmark.RunResourceKernelFixture(ctx, args[1:], stderr), true
	default:
		return 0, false
	}
}

func faultAgentCommand(executable string) benchmark.FaultAgentCommand {
	return benchmark.FaultAgentCommand{
		Entrypoint: executable,
		ArgsPrefix: []string{hiddenFaultFixtureCommand},
	}
}

func parseArgs(args []string) (cliOptions, error) {
	if len(args) == 0 {
		return cliOptions{}, fmt.Errorf("%s", usageText)
	}
	kind := commandKind(args[0])
	if kind == commandCommunication {
		return parseCommunicationOptions(args[1:])
	}
	if kind != commandScheduler && kind != commandContext && kind != commandFault && kind != commandFaultKernel && kind != commandResourceKernel {
		return cliOptions{}, fmt.Errorf("unknown evidence command %q\n%s", args[0], usageText)
	}
	set := flag.NewFlagSet(string(kind), flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	set.Usage = func() { fmt.Fprintln(set.Output(), usageText) }
	var output, seedList, profile string
	var repetitions int
	set.StringVar(&output, "output", "", "existing parent for an anonymous run directory")
	set.StringVar(&seedList, "seeds", "", "comma-separated uint32 seeds")
	set.IntVar(&repetitions, "repetitions", 0, "measured repetitions per seed")
	if kind == commandScheduler || kind == commandFaultKernel || kind == commandResourceKernel {
		set.StringVar(&profile, "profile", "", "evidence profile: smoke or formal")
	}
	if err := set.Parse(args[1:]); err != nil {
		return cliOptions{}, err
	}
	if set.NArg() != 0 || strings.TrimSpace(output) == "" {
		return cliOptions{}, fmt.Errorf("output is required and positional arguments are not allowed")
	}
	if kind == commandFaultKernel {
		return parseFaultKernelOptions(set, output, profile, seedList, repetitions)
	}
	if kind == commandResourceKernel {
		visited := make(map[string]bool)
		set.Visit(func(item *flag.Flag) { visited[item.Name] = true })
		if profile != benchmark.ResourceKernelProfileSmoke || visited["seeds"] || visited["repetitions"] {
			return cliOptions{}, fmt.Errorf("resource-kernel supports only the fixed smoke profile without overrides")
		}
		plan := benchmark.NewResourceKernelSmokePlan()
		return cliOptions{kind: kind, output: output, profile: profile, resourceKernelPlan: plan}, nil
	}
	if kind == commandScheduler {
		visited := make(map[string]bool)
		set.Visit(func(item *flag.Flag) { visited[item.Name] = true })
		if profile == benchmark.SchedulerProfileFormal {
			if visited["seeds"] || visited["repetitions"] {
				return cliOptions{}, fmt.Errorf("formal scheduler profile does not accept seed or repetition overrides")
			}
			return cliOptions{kind: kind, output: output, profile: profile, schedulerPlan: benchmark.NewSchedulerFormalPlan()}, nil
		}
		if profile != "" && profile != benchmark.SchedulerProfileSmoke {
			return cliOptions{}, fmt.Errorf("scheduler profile must be smoke or formal")
		}
	}
	seeds, err := parseSeeds(seedList)
	if err != nil {
		return cliOptions{}, err
	}
	if repetitions <= 0 || repetitions > 1000 {
		return cliOptions{}, fmt.Errorf("repetitions must be between 1 and 1000")
	}
	if kind == commandScheduler {
		plan := benchmark.SchedulerPlan{Profile: benchmark.SchedulerProfileSmoke, Seeds: seeds, Repetitions: repetitions}
		return cliOptions{kind: kind, output: output, profile: benchmark.SchedulerProfileSmoke, schedulerPlan: plan, seeds: seeds, repetitions: repetitions}, nil
	}
	if kind == commandFault {
		if err := benchmark.ValidateFaultPlan(benchmark.FaultPlan{Seeds: seeds, Repetitions: repetitions}); err != nil {
			return cliOptions{}, err
		}
	}
	return cliOptions{kind: kind, output: output, seeds: seeds, repetitions: repetitions}, nil
}

func parseFaultKernelOptions(
	set *flag.FlagSet,
	output, profile, seedList string,
	repetitions int,
) (cliOptions, error) {
	visited := make(map[string]bool)
	set.Visit(func(item *flag.Flag) { visited[item.Name] = true })
	var plan benchmark.FaultKernelPlan
	var err error
	switch profile {
	case benchmark.FaultKernelProfileSmoke:
		seeds, parseErr := parseSeeds(seedList)
		if parseErr != nil {
			return cliOptions{}, parseErr
		}
		plan, err = benchmark.NewFaultKernelSmokePlan(
			seeds, repetitions, benchmark.FaultKernelDefaultTrialTimeoutMS,
		)
	case benchmark.FaultKernelProfileFormal:
		if visited["seeds"] || visited["repetitions"] {
			return cliOptions{}, fmt.Errorf("formal fault-kernel profile does not accept seed or repetition overrides")
		}
		plan, err = benchmark.NewFaultKernelFormalPlan(benchmark.FaultKernelDefaultTrialTimeoutMS)
	default:
		return cliOptions{}, fmt.Errorf("fault-kernel profile must be smoke or formal")
	}
	if err != nil {
		return cliOptions{}, err
	}
	return cliOptions{kind: commandFaultKernel, output: output, profile: profile, faultKernelPlan: plan}, nil
}

func evidencePublishedMessage(kind commandKind, runID string) string {
	return fmt.Sprintf("%s evidence published: %s", kind, runID)
}

func collectionErrorMessage(err error) string {
	if errors.Is(err, context.Canceled) {
		return "evidence collection canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "evidence collection timed out"
	}
	return "evidence collection failed"
}

func parseSeeds(value string) ([]uint32, error) {
	parts := strings.Split(value, ",")
	if strings.TrimSpace(value) == "" || len(parts) > 128 {
		return nil, fmt.Errorf("seeds must contain between 1 and 128 values")
	}
	seeds := make([]uint32, 0, len(parts))
	seen := make(map[uint32]struct{}, len(parts))
	for _, part := range parts {
		parsed, err := strconv.ParseUint(strings.TrimSpace(part), 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid seed %q", part)
		}
		seed := uint32(parsed)
		if _, exists := seen[seed]; exists {
			return nil, fmt.Errorf("duplicate seed %d", seed)
		}
		seen[seed] = struct{}{}
		seeds = append(seeds, seed)
	}
	return seeds, nil
}
