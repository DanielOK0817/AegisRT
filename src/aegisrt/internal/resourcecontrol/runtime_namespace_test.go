package resourcecontrol

import (
	"path/filepath"
	"testing"
)

func TestRuntimeNamespaceForStateFileIsStableAcrossPathForms(t *testing.T) {
	relative := filepath.Join("state", "runtime.state")
	absolute, err := filepath.Abs(relative)
	if err != nil {
		t.Fatal(err)
	}
	first, err := RuntimeNamespaceForStateFile(relative)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RuntimeNamespaceForStateFile(absolute)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(first) != len("rt-")+24 {
		t.Fatalf("namespace mismatch %q / %q", first, second)
	}
}
