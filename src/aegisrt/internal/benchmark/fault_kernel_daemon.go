package benchmark

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"aegisrt/internal/resourcecontrol"
)

const (
	faultKernelDaemonReceiptSchema        = "aegisrt.supervisor.startup-receipt/v1"
	faultKernelMaximumStartupReceiptBytes = 2048
	faultKernelDefaultStartupTimeout      = 10 * time.Second
	faultKernelMaximumStartupTimeout      = 30 * time.Second
)

type faultKernelDaemonConfig struct {
	BenchmarkExecutable string
	Arguments           []string
	Driver              *resourcecontrol.CgroupV2Driver
	AttemptID           string
	UseAttemptManager   bool
	StartupTimeout      time.Duration
}

type faultKernelDaemonReceipt struct {
	SchemaVersion     string `json:"schema_version"`
	ListenAddress     string `json:"listen_address"`
	RuntimeNamespace  string `json:"runtime_namespace"`
	StateLoaded       bool   `json:"state_loaded"`
	RecoveryPerformed bool   `json:"recovery_performed"`
	RequeuedCount     int    `json:"requeued_count"`
	FailedCount       int    `json:"failed_count"`
	CanceledCount     int    `json:"canceled_count"`
}

func (r faultKernelDaemonReceipt) Evidence() FaultKernelStartupReceipt {
	return FaultKernelStartupReceipt{
		SchemaVersion:     1,
		StateLoaded:       r.StateLoaded,
		RecoveryPerformed: r.RecoveryPerformed,
		RequeuedCount:     r.RequeuedCount,
		FailedCount:       r.FailedCount,
		CanceledCount:     r.CanceledCount,
	}
}

type faultKernelDaemonWaitResult struct {
	state *os.ProcessState
	err   error
}

type faultKernelDaemon struct {
	process *os.Process
	done    chan struct{}
	result  faultKernelDaemonWaitResult
	once    sync.Once
}

func parseFaultKernelDaemonReceipt(payload []byte) (faultKernelDaemonReceipt, error) {
	if len(payload) < 2 || len(payload) > faultKernelMaximumStartupReceiptBytes ||
		payload[len(payload)-1] != '\n' || bytes.Count(payload, []byte{'\n'}) != 1 ||
		bytes.Contains(payload, []byte{'\r'}) {
		return faultKernelDaemonReceipt{}, faultKernelDaemonFailure("startup-receipt-framing")
	}
	line := payload[:len(payload)-1]
	if len(bytes.TrimSpace(line)) != len(line) {
		return faultKernelDaemonReceipt{}, faultKernelDaemonFailure("startup-receipt-framing")
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	var receipt faultKernelDaemonReceipt
	if err := decoder.Decode(&receipt); err != nil || decoder.InputOffset() != int64(len(line)) {
		return faultKernelDaemonReceipt{}, faultKernelDaemonFailure("startup-receipt-json")
	}
	if err := validateFaultKernelDaemonReceipt(receipt); err != nil {
		return faultKernelDaemonReceipt{}, err
	}
	return receipt, nil
}

func validateFaultKernelDaemonReceipt(receipt faultKernelDaemonReceipt) error {
	if receipt.SchemaVersion != faultKernelDaemonReceiptSchema ||
		!validFaultKernelDaemonEndpoint(receipt.ListenAddress) ||
		!validFaultKernelDaemonNamespace(receipt.RuntimeNamespace) ||
		receipt.RequeuedCount < 0 || receipt.FailedCount < 0 || receipt.CanceledCount < 0 {
		return faultKernelDaemonFailure("startup-receipt-content")
	}
	changed := receipt.RequeuedCount > 0 || receipt.FailedCount > 0 || receipt.CanceledCount > 0
	if receipt.RecoveryPerformed && !receipt.StateLoaded || changed && !receipt.RecoveryPerformed {
		return faultKernelDaemonFailure("startup-receipt-content")
	}
	return nil
}

func validFaultKernelDaemonEndpoint(value string) bool {
	host, portText, err := net.SplitHostPort(value)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	port, err := strconv.Atoi(portText)
	return err == nil && ip != nil && ip.IsLoopback() && port > 0 && port <= 65535
}

func validFaultKernelDaemonNamespace(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index, char := range value {
		letter := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z'
		digit := char >= '0' && char <= '9'
		if !(letter || digit || index > 0 && (char == '.' || char == '_' || char == '-')) {
			return false
		}
	}
	return true
}

func faultKernelDaemonFailure(stage string) error {
	return fmt.Errorf("%w: %s", ErrFaultKernelUnavailable, stage)
}
