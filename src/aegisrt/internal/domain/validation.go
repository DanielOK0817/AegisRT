package domain

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	maxIdentifierLength = 128
	maxNameLength       = 256
)

func validateIdentifier(field, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must not contain surrounding whitespace", field)
	}
	if len(value) > maxIdentifierLength {
		return fmt.Errorf("%s must not exceed %d bytes", field, maxIdentifierLength)
	}
	for index, char := range value {
		valid := char >= 'a' && char <= 'z' ||
			char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' ||
			(index > 0 && (char == '-' || char == '_' || char == '.' || char == ':'))
		if !valid {
			return fmt.Errorf("%s contains invalid character %q", field, char)
		}
	}
	return nil
}

func validateText(field, value string, maxRunes int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must not contain surrounding whitespace", field)
	}
	if utf8.RuneCountInString(value) > maxRunes {
		return fmt.Errorf("%s must not exceed %d characters", field, maxRunes)
	}
	return nil
}

func validateUniqueIdentifiers(field string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := validateIdentifier(field, value); err != nil {
			return err
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s contains duplicate value %q", field, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}
