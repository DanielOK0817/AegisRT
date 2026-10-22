package benchmark

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
)

func (validator workloadSchemaValidator) validateObject(schema, value map[string]any, path string) error {
	properties, _ := schema["properties"].(map[string]any)
	if required, ok := schema["required"].([]any); ok {
		for _, name := range required {
			if _, exists := value[name.(string)]; !exists {
				return fmt.Errorf("%s is missing %s", path, name)
			}
		}
	}
	for name, child := range properties {
		if item, exists := value[name]; exists {
			if err := validator.validateAt(child, item, path+"."+name); err != nil {
				return err
			}
		}
	}
	if allowed, exists := schema["additionalProperties"].(bool); exists && !allowed {
		for name := range value {
			if _, exists := properties[name]; !exists {
				return fmt.Errorf("%s has additional property %s", path, name)
			}
		}
	}
	return nil
}

func (validator workloadSchemaValidator) validateArray(schema map[string]any, value []any, path string) error {
	if minimum, ok := schemaInteger(schema["minItems"]); ok && int64(len(value)) < minimum {
		return fmt.Errorf("%s has too few items", path)
	}
	if maximum, ok := schemaInteger(schema["maxItems"]); ok && int64(len(value)) > maximum {
		return fmt.Errorf("%s has too many items", path)
	}
	prefix, _ := schema["prefixItems"].([]any)
	for index := 0; index < len(prefix) && index < len(value); index++ {
		if err := validator.validateAt(prefix[index], value[index], path+"["+strconv.Itoa(index)+"]"); err != nil {
			return err
		}
	}
	if items, exists := schema["items"]; exists {
		for index := len(prefix); index < len(value); index++ {
			if err := validator.validateAt(items, value[index], path+"["+strconv.Itoa(index)+"]"); err != nil {
				return err
			}
		}
	}
	if unique, _ := schema["uniqueItems"].(bool); unique {
		seen := make(map[string]bool, len(value))
		for _, item := range value {
			payload, _ := json.Marshal(item)
			key := string(payload)
			if seen[key] {
				return fmt.Errorf("%s has duplicate items", path)
			}
			seen[key] = true
		}
	}
	return nil
}

func validateWorkloadSchemaString(schema map[string]any, value, path string) error {
	if minimum, ok := schemaInteger(schema["minLength"]); ok && int64(len([]rune(value))) < minimum {
		return fmt.Errorf("%s is too short", path)
	}
	if maximum, ok := schemaInteger(schema["maxLength"]); ok && int64(len([]rune(value))) > maximum {
		return fmt.Errorf("%s is too long", path)
	}
	if pattern, ok := schema["pattern"].(string); ok {
		expression, err := regexp.Compile(pattern)
		if err != nil || !expression.MatchString(value) {
			return fmt.Errorf("%s does not match %q", path, pattern)
		}
	}
	return nil
}

func validateWorkloadSchemaInteger(schema map[string]any, value json.Number, path string) error {
	parsed, err := value.Int64()
	if err != nil {
		return fmt.Errorf("%s is not an integer", path)
	}
	if minimum, ok := schemaInteger(schema["minimum"]); ok && parsed < minimum {
		return fmt.Errorf("%s is below minimum", path)
	}
	if maximum, ok := schemaInteger(schema["maximum"]); ok && parsed > maximum {
		return fmt.Errorf("%s is above maximum", path)
	}
	return nil
}

func schemaInteger(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	parsed, err := number.Int64()
	return parsed, err == nil
}

func workloadSchemaTypeMatches(kind string, value any) bool {
	switch kind {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		_, err := number.Int64()
		return err == nil
	case "boolean":
		_, ok := value.(bool)
		return ok
	default:
		return false
	}
}
