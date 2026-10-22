package resourcecontrol

import (
	"bufio"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type PressureLine struct {
	Avg10  float64 `json:"avg10"`
	Avg60  float64 `json:"avg60"`
	Avg300 float64 `json:"avg300"`
	Total  int64   `json:"total_micros"`
}

type PressureSample struct {
	Some *PressureLine `json:"some,omitempty"`
	Full *PressureLine `json:"full,omitempty"`
}

type SystemPressure struct {
	CPU    PressureSample `json:"cpu"`
	Memory PressureSample `json:"memory"`
	IO     PressureSample `json:"io"`
}

// PSICollector reads Linux Pressure Stall Information from /proc/pressure.
type PSICollector struct {
	root string
}

func NewPSICollector(root string) (*PSICollector, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("PSI root is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve PSI root: %w", err)
	}
	return &PSICollector{root: filepath.Clean(absRoot)}, nil
}

func NewHostPSICollector() (*PSICollector, error) {
	return NewPSICollector("/proc/pressure")
}

func (c *PSICollector) Collect() (SystemPressure, error) {
	cpu, err := readPressure(filepath.Join(c.root, "cpu"))
	if err != nil {
		return SystemPressure{}, fmt.Errorf("read cpu pressure: %w", err)
	}
	memory, err := readPressure(filepath.Join(c.root, "memory"))
	if err != nil {
		return SystemPressure{}, fmt.Errorf("read memory pressure: %w", err)
	}
	ioPressure, err := readPressure(filepath.Join(c.root, "io"))
	if err != nil {
		return SystemPressure{}, fmt.Errorf("read io pressure: %w", err)
	}
	return SystemPressure{CPU: cpu, Memory: memory, IO: ioPressure}, nil
}

func readPressure(path string) (PressureSample, error) {
	file, err := os.Open(path)
	if err != nil {
		return PressureSample{}, err
	}
	defer file.Close()
	var result PressureSample
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		kind, line, err := parsePressureLine(scanner.Text())
		if err != nil {
			return PressureSample{}, err
		}
		switch kind {
		case "some":
			if result.Some != nil {
				return PressureSample{}, errors.New("duplicate some pressure line")
			}
			result.Some = &line
		case "full":
			if result.Full != nil {
				return PressureSample{}, errors.New("duplicate full pressure line")
			}
			result.Full = &line
		}
	}
	if err := scanner.Err(); err != nil {
		return PressureSample{}, err
	}
	if result.Some == nil && result.Full == nil {
		return PressureSample{}, errors.New("pressure file has no samples")
	}
	return result, nil
}

func parsePressureLine(input string) (string, PressureLine, error) {
	fields := strings.Fields(input)
	if len(fields) != 5 || (fields[0] != "some" && fields[0] != "full") {
		return "", PressureLine{}, fmt.Errorf("invalid pressure line %q", input)
	}
	values := make(map[string]string, 4)
	for _, field := range fields[1:] {
		name, value, found := strings.Cut(field, "=")
		if !found || name == "" || value == "" {
			return "", PressureLine{}, fmt.Errorf("invalid pressure field %q", field)
		}
		if _, exists := values[name]; exists {
			return "", PressureLine{}, fmt.Errorf("duplicate pressure field %q", name)
		}
		values[name] = value
	}
	for _, required := range []string{"avg10", "avg60", "avg300", "total"} {
		if _, exists := values[required]; !exists {
			return "", PressureLine{}, fmt.Errorf("missing pressure field %q", required)
		}
	}
	avg10, err := parsePressureFloat(values["avg10"])
	if err != nil {
		return "", PressureLine{}, fmt.Errorf("parse avg10: %w", err)
	}
	avg60, err := parsePressureFloat(values["avg60"])
	if err != nil {
		return "", PressureLine{}, fmt.Errorf("parse avg60: %w", err)
	}
	avg300, err := parsePressureFloat(values["avg300"])
	if err != nil {
		return "", PressureLine{}, fmt.Errorf("parse avg300: %w", err)
	}
	total, err := strconv.ParseInt(values["total"], 10, 64)
	if err != nil || total < 0 {
		return "", PressureLine{}, fmt.Errorf("invalid total %q", values["total"])
	}
	return fields[0], PressureLine{Avg10: avg10, Avg60: avg60, Avg300: avg300, Total: total}, nil
}

func parsePressureFloat(input string) (float64, error) {
	value, err := strconv.ParseFloat(input, 64)
	if err != nil || value < 0 || math.IsInf(value, 0) || math.IsNaN(value) {
		return 0, fmt.Errorf("invalid pressure average %q", input)
	}
	return value, nil
}
