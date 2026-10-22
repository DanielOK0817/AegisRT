package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"

	"aegisrt/internal/runtimecore"
)

const (
	startupReceiptSchemaV1     = "aegisrt.supervisor.startup-receipt/v1"
	startupReceiptDisabledFD   = -1
	minimumStartupReceiptFD    = 3
	maximumStartupReceiptFD    = 1024
	maximumStartupReceiptBytes = 2048
)

// startupReceipt is a one-shot local supervisor handshake. It deliberately
// contains no credentials, paths, task identifiers, process identifiers, or
// raw errors. The listen address is transient and must not be persisted as
// benchmark evidence.
type startupReceipt struct {
	SchemaVersion     string `json:"schema_version"`
	ListenAddress     string `json:"listen_address"`
	RuntimeNamespace  string `json:"runtime_namespace"`
	StateLoaded       bool   `json:"state_loaded"`
	RecoveryPerformed bool   `json:"recovery_performed"`
	RequeuedCount     int    `json:"requeued_count"`
	FailedCount       int    `json:"failed_count"`
	CanceledCount     int    `json:"canceled_count"`
}

func newStartupReceipt(address, namespace string, report runtimecore.RecoveryReport) startupReceipt {
	return startupReceipt{
		SchemaVersion:     startupReceiptSchemaV1,
		ListenAddress:     address,
		RuntimeNamespace:  namespace,
		StateLoaded:       report.Loaded,
		RecoveryPerformed: report.Recovered,
		RequeuedCount:     len(report.RequeuedTaskIDs),
		FailedCount:       len(report.FailedTaskIDs),
		CanceledCount:     len(report.CanceledTaskIDs),
	}
}

func writeStartupReceipt(writer io.Writer, receipt startupReceipt) error {
	if writer == nil {
		return errors.New("startup receipt writer is required")
	}
	if err := receipt.validate(); err != nil {
		return err
	}
	payload, err := marshalStartupReceipt(receipt)
	if err != nil {
		return err
	}
	for len(payload) > 0 {
		count, writeErr := writer.Write(payload)
		if count < 0 || count > len(payload) {
			return errors.New("startup receipt writer returned an invalid count")
		}
		payload = payload[count:]
		if writeErr != nil {
			return fmt.Errorf("write startup receipt: %w", writeErr)
		}
		if count == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func marshalStartupReceipt(receipt startupReceipt) ([]byte, error) {
	payload, err := json.Marshal(receipt)
	if err != nil {
		return nil, errors.New("encode startup receipt")
	}
	if len(payload)+1 > maximumStartupReceiptBytes || bytes.ContainsAny(payload, "\r\n") {
		return nil, errors.New("encoded startup receipt is invalid")
	}
	return append(payload, '\n'), nil
}

func emitStartupReceipt(fd int, receipt startupReceipt) error {
	if fd == startupReceiptDisabledFD {
		return nil
	}
	if err := validateStartupReceiptFD(fd); err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "aegisrt-startup-receipt")
	if file == nil {
		return errors.New("open startup receipt descriptor")
	}
	writeErr := writeStartupReceipt(file, receipt)
	return errors.Join(writeErr, file.Close())
}

func validateStartupReceiptFD(fd int) error {
	if fd == startupReceiptDisabledFD {
		return nil
	}
	if fd < minimumStartupReceiptFD || fd > maximumStartupReceiptFD {
		return fmt.Errorf("startup-receipt-fd must be between %d and %d",
			minimumStartupReceiptFD, maximumStartupReceiptFD)
	}
	return nil
}

func (receipt startupReceipt) validate() error {
	if receipt.SchemaVersion != startupReceiptSchemaV1 {
		return errors.New("startup receipt schema is invalid")
	}
	if err := validateStartupReceiptAddress(receipt.ListenAddress); err != nil {
		return err
	}
	if !validStartupReceiptNamespace(receipt.RuntimeNamespace) {
		return errors.New("startup receipt namespace is invalid")
	}
	if receipt.RequeuedCount < 0 || receipt.FailedCount < 0 || receipt.CanceledCount < 0 {
		return errors.New("startup receipt counts must be non-negative")
	}
	changed := receipt.RequeuedCount > 0 || receipt.FailedCount > 0 || receipt.CanceledCount > 0
	if receipt.RecoveryPerformed && !receipt.StateLoaded || changed && !receipt.RecoveryPerformed {
		return errors.New("startup receipt recovery state is inconsistent")
	}
	return nil
}

func validateStartupReceiptAddress(address string) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("startup receipt address must be a numeric loopback endpoint")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("startup receipt address must be a numeric loopback endpoint")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 {
		return errors.New("startup receipt address must contain an allocated port")
	}
	return nil
}

func validStartupReceiptNamespace(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 128 {
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
