package experiments_test

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJSONContractFixtures(t *testing.T) {
	paths := []string{
		"schemas/manifest-v1.schema.json",
		"schemas/event-v1.schema.json",
		"schemas/scheduler-task-trace-v1.schema.json",
		"schemas/context-trial-trace-v1.schema.json",
		"schemas/context-trial-trace-v2.schema.json",
		"examples/manifest-v1.not-executed.json",
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
	}

	file, err := os.Open("examples/events-v1.not-executed.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("parse event: %v", err)
		}
		if event.Status != "NOT_EXECUTED" {
			t.Fatalf("example event status = %q", event.Status)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestManifestAcceptsRecomputableEvidenceArtifacts(t *testing.T) {
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
		"scheduler-task-trace-jsonl": false,
		"workload-json":              false,
		"scheduler-input-json":       false,
		"context-trial-trace-jsonl":  false,
		"context-input-json":         false,
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

func TestManifestOpenEulerScopeRequiresDelegatedKernelSignals(t *testing.T) {
	data, err := os.ReadFile("schemas/manifest-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	for _, controller := range []string{"cpu", "memory", "pids"} {
		if !containsSchemaConst(schema, controller) {
			t.Errorf("openEuler manifest constraint does not require %s", controller)
		}
	}
	if !hasSchemaConst(schema, "available") {
		t.Error("openEuler manifest constraint does not require PSI availability")
	}
}

func containsSchemaConst(value any, target string) bool {
	switch typed := value.(type) {
	case map[string]any:
		if contains, exists := typed["contains"].(map[string]any); exists && contains["const"] == target {
			return true
		}
		for _, child := range typed {
			if containsSchemaConst(child, target) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsSchemaConst(child, target) {
				return true
			}
		}
	}
	return false
}

func hasSchemaConst(value any, target string) bool {
	switch typed := value.(type) {
	case map[string]any:
		if typed["const"] == target {
			return true
		}
		for _, child := range typed {
			if hasSchemaConst(child, target) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if hasSchemaConst(child, target) {
				return true
			}
		}
	}
	return false
}

func TestContextCSVUsesUnambiguousRuntimeMetricNames(t *testing.T) {
	header := readCSV(t, "csv/context-v1.header.csv")
	if len(header) != 1 {
		t.Fatalf("context header rows = %d", len(header))
	}
	want := strings.Split(
		"schema_version,run_id,status,evidence_scope,case_id,pair_id,seed,repetition,trial_order,treatment,role,counter_name,context_mode,provider_cache_mode,workload_sha256,page_count,storage_logical_tokens,storage_physical_tokens,storage_reused_tokens,dropped_tokens,summary_pages,dispatch_input_tokens,target_budget_exceedances,elapsed_ms,manifest_operations,manifest_conflicts,timeout_ms,reason_code",
		",",
	)
	if strings.Join(header[0], ",") != strings.Join(want, ",") {
		t.Fatalf("context header does not match the Runtime contract:\n got %v\nwant %v", header[0], want)
	}
	for _, obsolete := range []string{"logical_tokens", "physical_tokens", "reused_tokens", "budget_violations"} {
		for _, column := range header[0] {
			if column == obsolete {
				t.Errorf("ambiguous context column %q remains", obsolete)
			}
		}
	}
}

func TestContextV2CSVAddsTraceableCompressionMetrics(t *testing.T) {
	header := readCSV(t, "csv/context-v2.header.csv")
	if len(header) != 1 {
		t.Fatalf("context v2 header rows = %d", len(header))
	}
	want := strings.Split(
		"schema_version,run_id,status,evidence_scope,case_id,pair_id,seed,repetition,trial_order,treatment,role,counter_name,context_mode,provider_cache_mode,workload_sha256,page_count,storage_logical_tokens,storage_physical_tokens,storage_reused_tokens,dropped_tokens,summary_pages,summarized_source_tokens,summary_tokens,dispatch_input_tokens,target_budget_exceedances,elapsed_ms,manifest_operations,manifest_conflicts,timeout_ms,reason_code",
		",",
	)
	if strings.Join(header[0], ",") != strings.Join(want, ",") {
		t.Fatalf("context v2 header mismatch:\n got %v\nwant %v", header[0], want)
	}
}

func TestContextTraceSchemaIsClosedDraft202012(t *testing.T) {
	for _, path := range []string{"schemas/context-trial-trace-v1.schema.json", "schemas/context-trial-trace-v2.schema.json"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Draft                string `json:"$schema"`
			AdditionalProperties bool   `json:"additionalProperties"`
			Defs                 map[string]struct {
				AdditionalProperties bool `json:"additionalProperties"`
			} `json:"$defs"`
		}
		if err := json.Unmarshal(data, &schema); err != nil {
			t.Fatal(err)
		}
		if schema.Draft != "https://json-schema.org/draft/2020-12/schema" || schema.AdditionalProperties {
			t.Fatalf("context trace root %s is not a closed Draft 2020-12 schema", path)
		}
		for _, name := range []string{"store", "dispatch", "page_outcome", "acl_probe"} {
			definition, exists := schema.Defs[name]
			if !exists || definition.AdditionalProperties {
				t.Errorf("context trace definition %q is missing or open in %s", name, path)
			}
		}
	}
}

func TestCSVExamplesMatchCanonicalHeaders(t *testing.T) {
	examples, err := filepath.Glob("examples/*-v1.not-executed.csv")
	if err != nil {
		t.Fatal(err)
	}
	for _, example := range examples {
		kind := strings.SplitN(filepath.Base(example), "-v1.", 2)[0]
		header := readCSV(t, filepath.Join("csv", kind+"-v1.header.csv"))
		rows := readCSV(t, example)
		if len(header) != 1 || len(rows) != 2 {
			t.Fatalf("unexpected rows for %s: header=%d example=%d", kind, len(header), len(rows))
		}
		if strings.Join(header[0], ",") != strings.Join(rows[0], ",") {
			t.Fatalf("header mismatch for %s", kind)
		}
		if len(rows[1]) != len(rows[0]) {
			t.Fatalf("column mismatch for %s: header=%d row=%d", kind, len(rows[0]), len(rows[1]))
		}
		if rows[1][2] != "NOT_EXECUTED" || rows[1][3] != "not-executed" {
			t.Fatalf("example %s is not marked NOT_EXECUTED", kind)
		}
	}
}

func TestIntegrityContractStatesTamperEvidenceWithoutWORMClaim(t *testing.T) {
	for _, path := range []string{
		"schemas/manifest-v1.schema.json",
		"examples/manifest-v1.not-executed.json",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "raw_data_immutable") {
			t.Fatalf("%s retains an unsupported immutability claim", path)
		}
	}
	data, err := os.ReadFile("examples/manifest-v1.not-executed.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Integrity struct {
			DirectoryPolicy string `json:"directory_policy"`
			TamperEvident   bool   `json:"tamper_evident"`
			WORMStorage     bool   `json:"worm_storage"`
		} `json:"integrity"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Integrity.DirectoryPolicy != "create-only-atomic-publish" ||
		!fixture.Integrity.TamperEvident || fixture.Integrity.WORMStorage {
		t.Fatalf("inaccurate integrity contract: %#v", fixture.Integrity)
	}
}

func readCSV(t *testing.T, path string) [][]string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return rows
}
