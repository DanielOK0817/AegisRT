package benchmark

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type workloadSchemaValidator struct {
	root map[string]any
}

func loadFaultKernelWorkloadSchema(t *testing.T) workloadSchemaValidator {
	t.Helper()
	path := filepath.Join("..", "..", "experiments", "schemas", "fault-kernel-workload-v1.schema.json")
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	value, err := decodeWorkloadSchemaJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	root, ok := value.(map[string]any)
	if !ok {
		t.Fatal("workload schema root is not an object")
	}
	return workloadSchemaValidator{root: root}
}

func workloadSchemaValue(t *testing.T, value any) map[string]any {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeWorkloadSchemaJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	return decoded.(map[string]any)
}

func cloneWorkloadSchemaValue(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	return workloadSchemaValue(t, value)
}

func decodeWorkloadSchemaJSON(payload []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("schema JSON has trailing content")
	}
	return value, nil
}

func firstCell(root map[string]any) map[string]any {
	return arrayField(root, "cells")[0].(map[string]any)
}

func firstTask(root map[string]any, role string) map[string]any {
	return objectField(firstCell(root), role)
}

func objectField(root map[string]any, name string) map[string]any {
	return root[name].(map[string]any)
}

func arrayField(root map[string]any, name string) []any {
	return root[name].([]any)
}

func (validator workloadSchemaValidator) validate(value any) error {
	return validator.validateAt(validator.root, value, "$")
}

func (validator workloadSchemaValidator) validateAt(rawSchema, value any, path string) error {
	if allowed, ok := rawSchema.(bool); ok {
		if allowed {
			return nil
		}
		return fmt.Errorf("%s is rejected by a false schema", path)
	}
	schema, ok := rawSchema.(map[string]any)
	if !ok {
		return fmt.Errorf("%s has a malformed schema", path)
	}
	if reference, ok := schema["$ref"].(string); ok {
		resolved, err := validator.resolve(reference)
		if err != nil {
			return err
		}
		if err := validator.validateAt(resolved, value, path); err != nil {
			return err
		}
	}
	if kind, ok := schema["type"].(string); ok && !workloadSchemaTypeMatches(kind, value) {
		return fmt.Errorf("%s has type %T, want %s", path, value, kind)
	}
	if expected, exists := schema["const"]; exists && !reflect.DeepEqual(value, expected) {
		return fmt.Errorf("%s differs from const", path)
	}
	if values, ok := schema["enum"].([]any); ok {
		matched := false
		for _, expected := range values {
			matched = matched || reflect.DeepEqual(value, expected)
		}
		if !matched {
			return fmt.Errorf("%s is outside enum", path)
		}
	}
	if all, ok := schema["allOf"].([]any); ok {
		for _, child := range all {
			if err := validator.validateAt(child, value, path); err != nil {
				return err
			}
		}
	}
	if condition, exists := schema["if"]; exists {
		branch := schema["else"]
		if validator.validateAt(condition, value, path) == nil {
			branch = schema["then"]
		}
		if branch != nil {
			if err := validator.validateAt(branch, value, path); err != nil {
				return err
			}
		}
	}
	if object, ok := value.(map[string]any); ok {
		if err := validator.validateObject(schema, object, path); err != nil {
			return err
		}
	}
	if array, ok := value.([]any); ok {
		if err := validator.validateArray(schema, array, path); err != nil {
			return err
		}
	}
	if text, ok := value.(string); ok {
		if err := validateWorkloadSchemaString(schema, text, path); err != nil {
			return err
		}
	}
	if number, ok := value.(json.Number); ok {
		if err := validateWorkloadSchemaInteger(schema, number, path); err != nil {
			return err
		}
	}
	return nil
}

func (validator workloadSchemaValidator) resolve(reference string) (any, error) {
	if !strings.HasPrefix(reference, "#/") {
		return nil, fmt.Errorf("unsupported schema reference %q", reference)
	}
	var current any = validator.root
	for _, part := range strings.Split(strings.TrimPrefix(reference, "#/"), "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		object, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid schema reference %q", reference)
		}
		current, ok = object[part]
		if !ok {
			return nil, fmt.Errorf("missing schema reference %q", reference)
		}
	}
	return current, nil
}
