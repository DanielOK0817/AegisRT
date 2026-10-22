package resourcecontrol

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPSICollector(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "cpu"), "some avg10=1.25 avg60=2.50 avg300=3.75 total=123\n")
	writeFixture(t, filepath.Join(root, "memory"), pressureFixture())
	writeFixture(t, filepath.Join(root, "io"), pressureFixture())
	collector, err := NewPSICollector(root)
	if err != nil {
		t.Fatal(err)
	}
	pressure, err := collector.Collect()
	if err != nil {
		t.Fatal(err)
	}
	if pressure.CPU.Some == nil || pressure.CPU.Some.Avg10 != 1.25 || pressure.CPU.Some.Total != 123 {
		t.Fatalf("unexpected CPU pressure: %+v", pressure.CPU)
	}
	if pressure.CPU.Full != nil {
		t.Fatalf("CPU full pressure should be absent: %+v", pressure.CPU.Full)
	}
	if pressure.Memory.Full == nil || pressure.Memory.Full.Total != 456 {
		t.Fatalf("unexpected memory pressure: %+v", pressure.Memory)
	}
}

func TestPSICollectorRejectsMalformedData(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "cpu"), "some avg10=NaN avg60=0.00 avg300=0.00 total=0\n")
	writeFixture(t, filepath.Join(root, "memory"), pressureFixture())
	writeFixture(t, filepath.Join(root, "io"), pressureFixture())
	collector, err := NewPSICollector(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := collector.Collect(); err == nil {
		t.Fatal("Collect accepted malformed pressure data")
	}
}

func TestParsePressureLineRejectsInvalidShapes(t *testing.T) {
	for _, input := range []string{
		"",
		"other avg10=0 avg60=0 avg300=0 total=0",
		"some avg10=0 avg60=0 avg300=0",
		"some avg10=0 avg60=0 avg300=0 total=-1",
		"some avg10=0 avg10=0 avg300=0 total=0",
	} {
		if _, _, err := parsePressureLine(input); err == nil {
			t.Fatalf("parsePressureLine(%q) unexpectedly succeeded", input)
		}
	}
}

func TestNewPSICollectorRequiresRoot(t *testing.T) {
	if _, err := NewPSICollector(" "); err == nil {
		t.Fatal("NewPSICollector accepted an empty root")
	}
}

func pressureFixture() string {
	return "some avg10=0.10 avg60=0.20 avg300=0.30 total=123\n" +
		"full avg10=0.01 avg60=0.02 avg300=0.03 total=456\n"
}

func TestPSICollectorReportsMissingFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "cpu"), []byte(pressureFixture()), 0o644); err != nil {
		t.Fatal(err)
	}
	collector, err := NewPSICollector(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := collector.Collect(); err == nil {
		t.Fatal("Collect succeeded without memory and io files")
	}
}
