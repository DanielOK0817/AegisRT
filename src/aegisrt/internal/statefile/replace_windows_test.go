//go:build windows

package statefile

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func holdDestinationWithoutDeleteSharing(t *testing.T, path string) syscall.Handle {
	t.Helper()
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	return handle
}

func TestSaveRetriesTransientWindowsDestinationSharing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.state")
	store, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save([]byte("old")); err != nil {
		t.Fatal(err)
	}
	handle := holdDestinationWithoutDeleteSharing(t, path)
	defer syscall.CloseHandle(handle)
	done := make(chan error, 1)
	go func() { done <- store.Save([]byte("new")) }()
	select {
	case err := <-done:
		t.Fatalf("Save stopped before transient reader released: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if err := syscall.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("replacement did not finish")
	}
	payload, err := store.Load()
	if err != nil || string(payload) != "new" {
		t.Fatalf("payload=%q err=%v", payload, err)
	}
}

func TestSavePersistentWindowsSharingFailurePreservesOldFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.state")
	store, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save([]byte("old")); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	handle := holdDestinationWithoutDeleteSharing(t, path)
	defer syscall.CloseHandle(handle)
	start := time.Now()
	if err := store.Save([]byte("new")); err == nil {
		t.Fatal("locked replacement unexpectedly succeeded")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("retry was not bounded")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("failed replacement changed old checkpoint")
	}
}
