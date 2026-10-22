package benchmark

import (
	"errors"
	"strings"
	"testing"
)

func TestParseFaultKernelDaemonReceiptAcceptsStrictAnonymousLine(t *testing.T) {
	payload := []byte(`{"schema_version":"aegisrt.supervisor.startup-receipt/v1","listen_address":"127.0.0.1:43127","runtime_namespace":"rt-test","state_loaded":true,"recovery_performed":true,"requeued_count":1,"failed_count":0,"canceled_count":0}` + "\n")
	receipt, err := parseFaultKernelDaemonReceipt(payload)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ListenAddress != "127.0.0.1:43127" || receipt.RuntimeNamespace != "rt-test" {
		t.Fatalf("receipt = %+v", receipt)
	}
	want := FaultKernelStartupReceipt{SchemaVersion: 1, StateLoaded: true, RecoveryPerformed: true, RequeuedCount: 1}
	if got := receipt.Evidence(); got != want {
		t.Fatalf("Evidence() = %+v, want %+v", got, want)
	}
}

func TestParseFaultKernelDaemonReceiptRejectsMalformedOrSensitiveInput(t *testing.T) {
	valid := `{"schema_version":"aegisrt.supervisor.startup-receipt/v1","listen_address":"127.0.0.1:43127","runtime_namespace":"rt-test","state_loaded":false,"recovery_performed":false,"requeued_count":0,"failed_count":0,"canceled_count":0}`
	cases := map[string][]byte{
		"missing newline": []byte(valid),
		"extra line":      []byte(valid + "\n{}\n"),
		"second value":    []byte(valid + "{}\n"),
		"carriage return": []byte(valid + "\r\n"),
		"leading space":   []byte(" " + valid + "\n"),
		"unknown field":   []byte(strings.Replace(valid, `"canceled_count":0`, `"canceled_count":0,"token":"secret-value"`, 1) + "\n"),
		"remote address":  []byte(strings.Replace(valid, "127.0.0.1:43127", "198.51.100.77:43127", 1) + "\n"),
		"bad recovery":    []byte(strings.Replace(valid, `"requeued_count":0`, `"requeued_count":1`, 1) + "\n"),
		"oversized":       []byte(strings.Repeat("x", faultKernelMaximumStartupReceiptBytes+1)),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parseFaultKernelDaemonReceipt(payload)
			if !errors.Is(err, ErrFaultKernelUnavailable) {
				t.Fatalf("error = %v, want ErrFaultKernelUnavailable", err)
			}
			for _, secret := range []string{"secret-value", "198.51.100.77", "43127"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaked receipt content: %v", err)
				}
			}
		})
	}
}
