package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"aegisrt/internal/runtimecore"
)

func TestStartupReceiptWritesOneStrictAnonymousJSONLine(t *testing.T) {
	report := runtimecore.RecoveryReport{
		Loaded:          true,
		Recovered:       true,
		RequeuedTaskIDs: []string{"private-requeued-task"},
		FailedTaskIDs:   []string{"private-failed-task"},
		CanceledTaskIDs: []string{"private-canceled-task"},
	}
	receipt := newStartupReceipt("127.0.0.1:43127", "rt-0123456789abcdef01234567", report)

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := writeStartupReceipt(writer, receipt); err != nil {
		_ = writer.Close()
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 4096)
	count, err := reader.Read(payload)
	if err != nil {
		t.Fatal(err)
	}
	payload = payload[:count]

	if bytes.Count(payload, []byte{'\n'}) != 1 || payload[len(payload)-1] != '\n' || bytes.Contains(payload, []byte{'\r'}) {
		t.Fatalf("receipt is not one LF-terminated JSON line: %q", payload)
	}
	for _, forbidden := range []string{
		"private-requeued-task", "private-failed-task", "private-canceled-task",
		"auth_token", "token", "state_file", "error", "path",
	} {
		if bytes.Contains(bytes.ToLower(payload), []byte(strings.ToLower(forbidden))) {
			t.Fatalf("receipt contains forbidden value %q: %s", forbidden, payload)
		}
	}

	var decoded map[string]any
	if err := json.Unmarshal(bytes.TrimSuffix(payload, []byte{'\n'}), &decoded); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{
		"schema_version", "listen_address", "runtime_namespace", "state_loaded",
		"recovery_performed", "requeued_count", "failed_count", "canceled_count",
	}
	if len(decoded) != len(wantKeys) {
		t.Fatalf("receipt fields = %v, want exactly %v", decoded, wantKeys)
	}
	for _, key := range wantKeys {
		if _, exists := decoded[key]; !exists {
			t.Fatalf("receipt is missing %q: %v", key, decoded)
		}
	}
	if decoded["schema_version"] != startupReceiptSchemaV1 || decoded["listen_address"] != "127.0.0.1:43127" ||
		decoded["runtime_namespace"] != "rt-0123456789abcdef01234567" || decoded["state_loaded"] != true ||
		decoded["recovery_performed"] != true || decoded["requeued_count"] != float64(1) ||
		decoded["failed_count"] != float64(1) || decoded["canceled_count"] != float64(1) {
		t.Fatalf("unexpected receipt: %v", decoded)
	}
}

func TestStartupReceiptRejectsUnsafeOrInconsistentValues(t *testing.T) {
	valid := startupReceipt{
		SchemaVersion: startupReceiptSchemaV1,
		ListenAddress: "[::1]:43127", RuntimeNamespace: "rt-0123456789abcdef01234567",
		StateLoaded: true, RecoveryPerformed: true, RequeuedCount: 1,
	}
	tests := []struct {
		name   string
		mutate func(*startupReceipt)
	}{
		{name: "schema", mutate: func(value *startupReceipt) { value.SchemaVersion = "other" }},
		{name: "non-loopback", mutate: func(value *startupReceipt) { value.ListenAddress = "0.0.0.0:43127" }},
		{name: "hostname", mutate: func(value *startupReceipt) { value.ListenAddress = "localhost:43127" }},
		{name: "namespace newline", mutate: func(value *startupReceipt) { value.RuntimeNamespace = "rt-safe\nunsafe" }},
		{name: "negative count", mutate: func(value *startupReceipt) { value.FailedCount = -1 }},
		{name: "recovery without state", mutate: func(value *startupReceipt) { value.StateLoaded = false }},
		{name: "count without recovery", mutate: func(value *startupReceipt) {
			value.RecoveryPerformed = false
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			test.mutate(&value)
			var output bytes.Buffer
			if err := writeStartupReceipt(&output, value); err == nil {
				t.Fatalf("unsafe receipt was written: %s", output.Bytes())
			}
			if output.Len() != 0 {
				t.Fatalf("invalid receipt partially mutated output: %q", output.Bytes())
			}
		})
	}
}

func TestStartupReceiptFDValidationAndDisabledDefault(t *testing.T) {
	options, err := parseOptions([]string{
		"-agents-file", "agents.json", "-auth-token-file", "api.token", "-ephemeral",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if options.startupReceiptFD != startupReceiptDisabledFD {
		t.Fatalf("startup receipt fd = %d, want disabled", options.startupReceiptFD)
	}
	if err := emitStartupReceipt(options.startupReceiptFD, startupReceipt{}); err != nil {
		t.Fatalf("disabled startup receipt attempted validation or output: %v", err)
	}

	for _, value := range []int{minimumStartupReceiptFD, maximumStartupReceiptFD} {
		args := []string{
			"-agents-file", "agents.json", "-auth-token-file", "api.token", "-ephemeral",
			"-startup-receipt-fd", jsonNumber(value),
		}
		parsed, parseErr := parseOptions(args, &bytes.Buffer{})
		if parseErr != nil {
			t.Fatalf("fd %d rejected: %v", value, parseErr)
		}
		if parsed.startupReceiptFD != value {
			t.Fatalf("startup receipt fd = %d, want %d", parsed.startupReceiptFD, value)
		}
	}
	for _, value := range []int{startupReceiptDisabledFD - 1, 0, minimumStartupReceiptFD - 1, maximumStartupReceiptFD + 1} {
		args := []string{
			"-agents-file", "agents.json", "-auth-token-file", "api.token", "-ephemeral",
			"-startup-receipt-fd", jsonNumber(value),
		}
		if _, parseErr := parseOptions(args, &bytes.Buffer{}); parseErr == nil {
			t.Fatalf("unsafe startup receipt fd %d was accepted", value)
		}
	}
}

func jsonNumber(value int) string {
	data, _ := json.Marshal(value)
	return string(data)
}
