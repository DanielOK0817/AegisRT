package benchmark

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

func prepareFaultEvidence(
	runID string,
	result FaultResult,
	traces []FaultTrialTrace,
) (FaultResult, []FaultTrialTrace, error) {
	if err := validateRunID(runID); err != nil {
		return FaultResult{}, nil, err
	}
	preparedResult := result
	preparedResult.Rows = append([]FaultRow(nil), result.Rows...)
	preparedTraces := make([]FaultTrialTrace, len(traces))
	for index := range traces {
		preparedTraces[index] = cloneFaultTrialTrace(traces[index])
		if preparedTraces[index].RunID != "" && preparedTraces[index].RunID != runID {
			return FaultResult{}, nil, fmt.Errorf("fault trace %d has conflicting run id", index)
		}
		preparedTraces[index].RunID = runID
	}
	for index := range preparedResult.Rows {
		if preparedResult.Rows[index].RunID != "" && preparedResult.Rows[index].RunID != runID {
			return FaultResult{}, nil, fmt.Errorf("fault row %d has conflicting run id", index)
		}
		preparedResult.Rows[index].RunID = runID
	}
	if err := validateFaultEvidencePairs(preparedResult.Rows, preparedTraces); err != nil {
		return FaultResult{}, nil, err
	}
	return preparedResult, preparedTraces, nil
}

func validateCanonicalFaultInputs(result FaultResult, workloadJSON, inputJSON []byte) error {
	if len(workloadJSON) == 0 || !json.Valid(workloadJSON) || hashBytes(workloadJSON) != result.WorkloadSHA256 {
		return fmt.Errorf("canonical fault workload does not reproduce workload hash")
	}
	if len(inputJSON) == 0 || !json.Valid(inputJSON) || hashBytes(inputJSON) != result.InputSHA256 {
		return fmt.Errorf("canonical fault input does not reproduce input hash")
	}
	for _, forbidden := range [][]byte{
		[]byte(`"entrypoint"`), []byte(`"args_prefix"`), []byte(`"content"`),
		[]byte("deterministic fault fixture result"),
	} {
		if bytes.Contains(workloadJSON, forbidden) || bytes.Contains(inputJSON, forbidden) {
			return fmt.Errorf("canonical fault artifacts contain executable or raw output data")
		}
	}
	return nil
}

func validateFaultTracesAgainstWorkload(
	ctx context.Context,
	traces []FaultTrialTrace,
	workloadJSON []byte,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var workload faultWorkloadBundle
	if err := json.Unmarshal(workloadJSON, &workload); err != nil {
		return fmt.Errorf("decode canonical fault workload: %w", err)
	}
	if workload.Version != faultWorkloadVersion || workload.FixtureProtocol != faultFixtureProtocol {
		return fmt.Errorf("canonical fault workload has unsupported identity")
	}
	cases := make(map[string]faultCaseWorkload, len(workload.Cases))
	for _, item := range workload.Cases {
		key := fmt.Sprintf("%s/%d", item.CaseID, item.Seed)
		if _, exists := cases[key]; exists {
			return fmt.Errorf("canonical fault workload contains duplicate case")
		}
		cases[key] = item
	}
	for index, trace := range traces {
		if err := ctx.Err(); err != nil {
			return err
		}
		item, exists := cases[fmt.Sprintf("%s/%d", trace.CaseID, trace.Seed)]
		if !exists || trace.Target.TaskID != item.Target.TaskID || trace.Target.MaxAttempts != item.Target.MaxAttempts ||
			len(trace.UnaffectedTasks) != 1 || trace.UnaffectedTasks[0].TaskID != item.Unaffected.TaskID ||
			trace.UnaffectedTasks[0].MaxAttempts != item.Unaffected.MaxAttempts || trace.TimeoutMS != item.TrialTimeoutMS {
			return fmt.Errorf("fault trace %d does not match canonical workload", index)
		}
	}
	return nil
}

func marshalFaultTrialTraces(records []FaultTrialTrace) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	for index, record := range records {
		if err := encoder.Encode(record); err != nil {
			return nil, fmt.Errorf("encode fault trial trace %d: %w", index, err)
		}
	}
	return output.Bytes(), nil
}
