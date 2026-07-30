package db

import (
	"testing"

	. "github.com/onsi/gomega"
)

func TestConditionTypeValidation(t *testing.T) {
	tests := []struct {
		name        string
		condType    string
		expectMatch bool
	}{
		{"Valid - Reconciled", "Reconciled", true},
		{"Valid - Available", "Available", true},
		{"Valid - Progressing", "Progressing", true},
		{"Valid - CustomCondition", "CustomCondition", true},
		{"Valid - With numbers", "Reconciled2", true},
		{"Invalid - lowercase", "ready", false},
		{"Invalid - starts with number", "2Reconciled", false},
		{"Invalid - contains underscore", "Reconciled_State", false},
		{"Invalid - contains hyphen", "Reconciled-State", false},
		{"Invalid - empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			RegisterTestingT(t)
			result := conditionTypePattern.MatchString(tt.condType)
			Expect(result).To(Equal(tt.expectMatch))
		})
	}
}

func TestGetField_SpecMapping(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expected    string
		expectError bool
	}{
		{name: "valid snake_case key", input: "spec.is_default", expected: "spec->>'is_default'"},
		{name: "valid single word key", input: "spec.region", expected: "spec->>'region'"},
		{name: "valid key with digits", input: "spec.release_image_v2", expected: "spec->>'release_image_v2'"},
		{name: "invalid key with uppercase", input: "spec.ReleaseImage", expectError: true},
		{name: "invalid key with hyphens", input: "spec.release-image", expectError: true},
		{name: "empty key", input: "spec.", expectError: true},
		{name: "injection attempt", input: "spec.'; DROP TABLE resources;--", expectError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			RegisterTestingT(t)
			field, err := getField(tt.input)
			if tt.expectError {
				Expect(err).ToNot(BeNil())
			} else {
				Expect(err).To(BeNil())
				Expect(field).To(Equal(tt.expected))
			}
		})
	}
}

func TestGetField_SpecNested(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "1-level", input: "spec.region", expected: "spec->>'region'"},
		{name: "2-level", input: "spec.release.channel", expected: "spec->'release'->>'channel'"},
		{name: "3-level", input: "spec.release.config.zone", expected: "spec->'release'->'config'->>'zone'"},
		{name: "2-level with underscore", input: "spec.release.image_v2", expected: "spec->'release'->>'image_v2'"},
		{name: "trimmed spaces", input: "  spec.region  ", expected: "spec->>'region'"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			RegisterTestingT(t)
			field, err := getField(tt.input)
			Expect(err).To(BeNil())
			Expect(field).To(Equal(tt.expected))
		})
	}
}

func TestConditionStatusValidation(t *testing.T) {
	tests := []struct {
		status      string
		expectValid bool
	}{
		{"True", true}, {"False", true}, {"Unknown", true},
		{"true", false}, {"false", false}, {"unknown", false},
		{"Yes", false}, {"No", false}, {"", false},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			RegisterTestingT(t)
			Expect(validConditionStatuses[tt.status]).To(Equal(tt.expectValid))
		})
	}
}

func TestArgsToOrder(t *testing.T) {
	tests := []struct {
		name          string
		errorContains string
		input         []string
		expected      []string
		expectError   bool
	}{
		{name: "single field with asc", input: []string{"name asc"}, expected: []string{"name asc"}},
		{name: "single field with desc", input: []string{"created_time desc"}, expected: []string{"created_time desc"}},
		{name: "single field defaults to asc", input: []string{"created_time"}, expected: []string{"created_time asc"}},
		{
			name:     "multiple fields",
			input:    []string{"name asc", "created_time desc"},
			expected: []string{"name asc", "created_time desc"},
		},
		{name: "field with extra spaces", input: []string{"  name   asc  "}, expected: []string{"name asc"}},
		{
			name:     "all allowed fields",
			input:    []string{"id", "name", "created_time", "updated_time", "kind"},
			expected: []string{"id asc", "name asc", "created_time asc", "updated_time asc", "kind asc"},
		},
		{
			name: "invalid direction", input: []string{"name ascending"},
			expectError: true, errorContains: "invalid order format",
		},
		{
			name: "SQL injection - semicolon", input: []string{"name; DROP TABLE resources"},
			expectError: true, errorContains: "invalid order format",
		},
		{
			name: "SQL injection - comment", input: []string{"name-- asc"},
			expectError: true, errorContains: "invalid order format",
		},
		{
			name: "uppercase field name", input: []string{"NAME asc"},
			expectError: true, errorContains: "invalid order format",
		},
		{
			name: "uppercase direction", input: []string{"name ASC"},
			expectError: true, errorContains: "invalid order format",
		},
		{name: "empty string skipped", input: []string{""}, expected: nil},
		{name: "whitespace only skipped", input: []string{"   "}, expected: nil},
		{
			name: "field not in whitelist", input: []string{"custom_field asc"},
			expectError: true, errorContains: "not allowed for ordering",
		},
		{name: "empty array", input: []string{}, expected: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			RegisterTestingT(t)
			result, err := ArgsToOrder(tt.input)
			if tt.expectError {
				Expect(err).ToNot(BeNil())
				if tt.errorContains != "" {
					Expect(err.Reason).To(ContainSubstring(tt.errorContains))
				}
			} else {
				Expect(err).To(BeNil())
				Expect(result).To(Equal(tt.expected))
			}
		})
	}
}

func TestArgsToOrder_SecurityValidation(t *testing.T) {
	RegisterTestingT(t)

	injections := []struct {
		name  string
		input string
	}{
		{"union injection", "name UNION SELECT password FROM users"},
		{"comment injection", "name--"},
		{"semicolon terminator", "name; DROP TABLE resources;"},
		{"quote escape", "name' OR '1'='1"},
		{"parentheses", "name) OR (1=1"},
		{"wildcard", "name*"},
		{"backtick", "name`"},
	}

	for _, tt := range injections {
		t.Run(tt.name, func(t *testing.T) {
			RegisterTestingT(t)
			_, err := ArgsToOrder([]string{tt.input})
			Expect(err).ToNot(BeNil())
		})
	}
}
