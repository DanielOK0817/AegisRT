package benchmark

import (
	"bytes"
	"encoding/json"
	"fmt"
)

func prepareContextEvidence(runID string, result ContextResult, traces []ContextTrialTrace) (ContextResult, []ContextTrialTrace, error) {
	if err := validateRunID(runID); err != nil {
		return ContextResult{}, nil, err
	}
	preparedResult := result
	preparedResult.Rows = append([]ContextRow(nil), result.Rows...)
	preparedTraces := make([]ContextTrialTrace, len(traces))
	for index := range traces {
		preparedTraces[index] = cloneContextTrialTrace(traces[index])
		if preparedTraces[index].RunID != "" && preparedTraces[index].RunID != runID {
			return ContextResult{}, nil, fmt.Errorf("context trace %d has conflicting run id", index)
		}
		preparedTraces[index].RunID = runID
	}
	for index := range preparedResult.Rows {
		if preparedResult.Rows[index].RunID != "" && preparedResult.Rows[index].RunID != runID {
			return ContextResult{}, nil, fmt.Errorf("context row %d has conflicting run id", index)
		}
		preparedResult.Rows[index].RunID = runID
	}
	if err := validateContextEvidencePairs(preparedResult.Rows, preparedTraces); err != nil {
		return ContextResult{}, nil, err
	}
	return preparedResult, preparedTraces, nil
}

func validateCanonicalContextInputs(result ContextResult, workloadJSON, inputJSON []byte) error {
	if len(workloadJSON) == 0 || !json.Valid(workloadJSON) || hashBytes(workloadJSON) != result.WorkloadSHA256 {
		return fmt.Errorf("canonical context workload does not reproduce workload hash")
	}
	if len(inputJSON) == 0 || !json.Valid(inputJSON) || hashBytes(inputJSON) != result.InputSHA256 {
		return fmt.Errorf("canonical context input does not reproduce input hash")
	}
	if bytes.Contains(workloadJSON, []byte(`"content":`)) || bytes.Contains(inputJSON, []byte(`"content":`)) {
		return fmt.Errorf("canonical context artifacts contain a raw content field")
	}
	return nil
}

func marshalContextTrialTraces(records []ContextTrialTrace) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	for index, record := range records {
		if err := encoder.Encode(record); err != nil {
			return nil, fmt.Errorf("encode context trial trace %d: %w", index, err)
		}
	}
	return output.Bytes(), nil
}
