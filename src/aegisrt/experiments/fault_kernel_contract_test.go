package experiments_test

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

const faultKernelHeader = "schema_version,run_id,status,evidence_scope,case_id,pair_id,seed,repetition,trial_order,treatment,role,analysis_eligible,workload_sha256,input_sha256,clock_mode,fault_mode,injection_boundary,isolation_mode,daemon_control,attempt_before,attempt_after,daemon_exit_ms,cleanup_ms,retry_start_ms,recovery_ms,retries,daemon_sigkill_observed,orphan_observed_before_restart,checkpoint_loaded,recovery_requeued,restart_ready,target_final_status,survivor_final_status,old_processes_after_cleanup,old_cgroups_after_cleanup,isolation_escape_count,duplicate_artifacts,elapsed_ms,timeout_ms,reason_code"

func TestFaultKernelCSVContractIsIndependentAndExact(t *testing.T) {
	rows := readCSV(t, "csv/fault-kernel-v1.header.csv")
	if len(rows) != 1 {
		t.Fatalf("fault-kernel header rows = %d", len(rows))
	}
	if got := strings.Join(rows[0], ","); got != faultKernelHeader {
		t.Fatalf("fault-kernel header mismatch:\n got %s\nwant %s", got, faultKernelHeader)
	}
	if strings.Join(rows[0], ",") == strings.Join(readCSV(t, "csv/fault-v1.header.csv")[0], ",") {
		t.Fatal("F4 kernel evidence must not reuse the Windows F1/F2/F3 table")
	}
}

func TestFaultKernelSchemasAreClosedDraft202012(t *testing.T) {
	paths := []string{
		"schemas/fault-kernel-input-v1.schema.json",
		"schemas/fault-kernel-workload-v1.schema.json",
		"schemas/fault-kernel-trial-trace-v1.schema.json",
		"schemas/cgroup-sample-v1.schema.json",
		"schemas/component-manifest-v1.schema.json",
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var schema map[string]any
		if err := json.Unmarshal(data, &schema); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
			t.Errorf("%s is not Draft 2020-12", path)
		}
		assertClosedSchemaObjects(t, path, schema, "$")
	}
}

func TestManifestAcceptsFaultKernelEvidenceArtifacts(t *testing.T) {
	data, err := os.ReadFile("schemas/manifest-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties struct {
			Artifacts struct {
				Items struct {
					Properties struct {
						Kind struct {
							Enum []string `json:"enum"`
						} `json:"kind"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"artifacts"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"fault-kernel-csv":                false,
		"fault-kernel-trial-trace-jsonl":  false,
		"fault-kernel-warmup-trace-jsonl": false,
		"cgroup-sample-jsonl":             false,
		"fault-kernel-input-json":         false,
		"component-manifest-json":         false,
	}
	for _, kind := range schema.Properties.Artifacts.Items.Properties.Kind.Enum {
		if _, exists := want[kind]; exists {
			want[kind] = true
		}
	}
	for kind, found := range want {
		if !found {
			t.Errorf("manifest artifact kind %q is missing", kind)
		}
	}
}

func TestManifestAndEventsUseSameFaultKernelKind(t *testing.T) {
	for _, path := range []string{
		"schemas/manifest-v1.schema.json",
		"schemas/event-v1.schema.json",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var schema any
		if err := json.Unmarshal(data, &schema); err != nil {
			t.Fatal(err)
		}
		if !hasSchemaEnumValue(schema, "fault-kernel") {
			t.Errorf("%s does not accept experiment kind fault-kernel", path)
		}
	}
}

func TestFaultKernelComponentsAreStableAndRoleComplete(t *testing.T) {
	data, err := os.ReadFile("schemas/component-manifest-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	if _, exists := properties["run_id"]; exists {
		t.Fatal("component manifest must be content-stable and omit run_id")
	}
	for _, role := range []string{"harness", "runtime-daemon", "agent-fixture"} {
		if !hasSchemaConst(schema, role) {
			t.Errorf("component manifest does not require role %q", role)
		}
	}
}

func TestPlanSeparatesF4KernelFromWindowsFaultSmoke(t *testing.T) {
	data, err := os.ReadFile("PLAN.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, marker := range []string{
		"f4-daemon-sigkill-cgroup-smoke-v1",
		"f4-daemon-sigkill-cgroup-v1",
		"F4-kernel-smoke",
		"openeuler-kernel",
		"must not be merged with the Windows F1/F2/F3 evidence",
	} {
		if !strings.Contains(text, marker) {
			t.Errorf("PLAN.md is missing F4 boundary marker %q", marker)
		}
	}
}

func assertClosedSchemaObjects(t *testing.T, file string, value any, location string) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		if typed["type"] == "object" {
			closed, exists := typed["additionalProperties"].(bool)
			if !exists || closed {
				t.Errorf("%s object at %s is not closed", file, location)
			}
		}
		for key, child := range typed {
			assertClosedSchemaObjects(t, file, child, location+"/"+key)
		}
	case []any:
		for index, child := range typed {
			assertClosedSchemaObjects(t, file, child, location+"/"+strconv.Itoa(index))
		}
	}
}

func hasSchemaEnumValue(value any, target string) bool {
	switch typed := value.(type) {
	case map[string]any:
		if enum, exists := typed["enum"].([]any); exists {
			for _, item := range enum {
				if item == target {
					return true
				}
			}
		}
		for _, child := range typed {
			if hasSchemaEnumValue(child, target) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if hasSchemaEnumValue(child, target) {
				return true
			}
		}
	}
	return false
}
