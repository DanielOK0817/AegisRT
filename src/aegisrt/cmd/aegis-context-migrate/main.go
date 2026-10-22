// aegis-context-migrate converts a stopped daemon's legacy Context into a new
// state file. Both source leases are held; existing output files are refused.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"aegisrt/internal/agentconfig"
	"aegisrt/internal/contextstate"
	"aegisrt/internal/contextvm"
	"aegisrt/internal/domain"
	"aegisrt/internal/runtimecore"
	"aegisrt/internal/statefile"
	"aegisrt/internal/taskgraph"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("aegis-context-migrate", flag.ContinueOnError)
	var source, runtimePath, output, agentsPath, agentRoot, bindingsPath string
	flags.StringVar(&source, "source", "", "legacy Context state file")
	flags.StringVar(&runtimePath, "runtime-state", "", "matching stopped Runtime state file")
	flags.StringVar(&output, "output", "", "new Context state file (must not exist)")
	flags.StringVar(&agentsPath, "agents-file", "", "daemon Agent registry")
	flags.StringVar(&agentRoot, "agent-root", "", "daemon Agent root")
	flags.StringVar(&bindingsPath, "ownership", "", "complete authoritative task bindings JSON when replay history was evicted")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if source == "" || runtimePath == "" || output == "" || agentsPath == "" || agentRoot == "" || flags.NArg() != 0 {
		return fmt.Errorf("source, runtime-state, output, agents-file and agent-root are required")
	}
	sourceAbs, _ := filepath.Abs(source)
	runtimeAbs, _ := filepath.Abs(runtimePath)
	outAbs, _ := filepath.Abs(output)
	if outAbs == sourceAbs || outAbs == runtimeAbs {
		return fmt.Errorf("output must differ from both sources")
	}
	if _, err := os.Lstat(outAbs); !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("output must not exist")
	}
	runtimeStore, err := statefile.OpenExclusive(runtimeAbs)
	if err != nil {
		return err
	}
	defer runtimeStore.Close()
	sourceStore, err := statefile.OpenExclusive(sourceAbs)
	if err != nil {
		return err
	}
	defer sourceStore.Close()
	runtimePayload, err := runtimeStore.Load()
	if err != nil {
		return err
	}
	var runtime runtimecore.Checkpoint
	if err := json.Unmarshal(runtimePayload, &runtime); err != nil {
		return err
	}
	if runtime.FormatVersion != 2 {
		return fmt.Errorf("matching Runtime must use checkpoint format 2")
	}
	if _, err := taskgraph.Restore(runtime.Graph); err != nil {
		return err
	}
	registry, err := agentconfig.Load(agentsPath, agentconfig.Options{AgentRoot: agentRoot, StrictPermissions: true})
	if err != nil {
		return err
	}
	limits := contextstate.DefaultLimits()
	fingerprint, err := contextstate.ConfigFingerprint(registry.Agents(), runtime.InstanceID, limits, "whitespace-v1", "none-v1")
	if err != nil {
		return err
	}
	config := contextstate.Config{Fingerprint: fingerprint, RuntimeInstanceID: runtime.InstanceID, Counter: contextvm.WhitespaceCounter{}, Limits: limits}
	payload, err := sourceStore.Load()
	if err != nil {
		return err
	}
	var ownership []contextstate.MigrationBinding
	if bindingsPath != "" {
		raw, err := os.ReadFile(bindingsPath)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &ownership); err != nil {
			return err
		}
		if ownership == nil {
			return fmt.Errorf("ownership must be an array")
		}
	}
	tasks := make([]domain.TaskSpec, 0, len(runtime.Graph.Tasks))
	registered := make(map[string]bool)
	for _, agent := range registry.Agents() {
		registered[agent.ID] = true
	}
	for _, task := range runtime.Graph.Tasks {
		if !registered[task.Spec.AgentID] {
			return fmt.Errorf("Runtime task %q uses an unregistered Agent", task.Spec.ID)
		}
		tasks = append(tasks, task.Spec)
	}
	converted, err := contextstate.MigrateLegacyCheckpoint(config, payload, tasks, ownership, time.Now().UTC())
	if err != nil {
		return err
	}
	destination, err := statefile.OpenExclusive(outAbs)
	if err != nil {
		return err
	}
	defer destination.Close()
	if _, err := os.Lstat(outAbs); !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("output appeared during migration")
	}
	if err := destination.Save(converted); err != nil {
		return err
	}
	if _, err := contextstate.NewPersistent(config, destination, false, time.Now()); err != nil {
		return err
	}
	before := sha256.Sum256(payload)
	after := sha256.Sum256(converted)
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"source_payload_sha256": hex.EncodeToString(before[:]), "output_payload_sha256": hex.EncodeToString(after[:]), "output": outAbs, "validated": true, "source_unchanged": true})
}
