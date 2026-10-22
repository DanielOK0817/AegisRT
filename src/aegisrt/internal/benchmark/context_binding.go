package benchmark

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
)

func validateContextTracesAgainstWorkload(
	ctx context.Context,
	traces []ContextTrialTrace,
	workloadJSON []byte,
) error {
	if ctx == nil {
		return fmt.Errorf("context is required")
	}
	var bundle contextWorkloadBundle
	if err := json.Unmarshal(workloadJSON, &bundle); err != nil {
		return fmt.Errorf("decode canonical context workload: %w", err)
	}
	if bundle.Version != contextWorkloadVersion || bundle.ContentGenerator != contextContentGenerator || len(bundle.Cases) == 0 {
		return fmt.Errorf("canonical context workload has unsupported identity")
	}
	workloads := make(map[string]contextCaseWorkload, len(bundle.Cases))
	for _, workload := range bundle.Cases {
		key := contextWorkloadKey(workload.CaseID, workload.Seed)
		if _, exists := workloads[key]; exists {
			return fmt.Errorf("canonical context workload duplicates %q", key)
		}
		workloads[key] = workload
	}
	wantHash := hashBytes(workloadJSON)
	for index, trace := range traces {
		if err := ctx.Err(); err != nil {
			return err
		}
		if trace.WorkloadSHA256 != wantHash {
			return fmt.Errorf("context trace %d references a different workload", index)
		}
		workload, exists := workloads[contextWorkloadKey(trace.CaseID, trace.Seed)]
		if !exists {
			return fmt.Errorf("context trace %d has no canonical workload", index)
		}
		expected, err := runContextTrial(ctx, workload, trace.Treatment)
		if err != nil {
			return fmt.Errorf("replay context trace %d: %w", index, err)
		}
		if trace.ManifestOperations != expected.manifestOperations || trace.ManifestConflicts != expected.manifestConflicts ||
			!reflect.DeepEqual(trace.Stores, expected.stores) || !reflect.DeepEqual(trace.Dispatches, expected.dispatches) ||
			!reflect.DeepEqual(trace.ACLProbes, expected.aclProbes) {
			return fmt.Errorf("context trace %d does not match canonical workload replay", index)
		}
	}
	return nil
}

func contextWorkloadKey(caseID string, seed uint32) string {
	return fmt.Sprintf("%s/%d", caseID, seed)
}
