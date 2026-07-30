package db

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/jinzhu/inflection"
	"github.com/openshift-hyperfleet/hyperfleet-api/pkg/errors"
	"github.com/yaacov/tree-search-language/v6/pkg/tsl"
	"gorm.io/gorm"
)

// jsonbKeyPattern guards keys interpolated into JSONB paths (spec->>'%s', properties->>'%s').
var jsonbKeyPattern = regexp.MustCompile(`^[a-z0-9_]+$`)

func validateJSONBKey(key, fieldType string) *errors.ServiceError {
	if key == "" {
		return errors.BadRequest("%s cannot be empty", fieldType)
	}

	if !jsonbKeyPattern.MatchString(key) {
		return errors.BadRequest(
			"%s '%s' is invalid: must contain only lowercase letters, digits, and underscores", fieldType, key,
		)
	}

	return nil
}

// getField gets the sql field associated with a name.
func getField(name string) (field string, err *errors.ServiceError) {
	trimmedName := strings.Trim(name, " ")

	if strings.HasPrefix(trimmedName, "properties.") {
		key := strings.TrimPrefix(trimmedName, "properties.")
		if validationErr := validateJSONBKey(key, "property key"); validationErr != nil {
			err = validationErr
			return
		}
		field = fmt.Sprintf("properties ->> '%s'", key)
		return
	}

	// Map user-friendly spec.xxx (and nested spec.xxx.yyy...) syntax to JSONB query.
	// v6 gives us the full dotted path directly:
	//   spec.region              → spec->>'region'
	//   spec.release.channel     → spec->'release'->>'channel'
	//   spec.a.b.c              → spec->'a'->'b'->>'c'
	if strings.HasPrefix(trimmedName, "spec.") {
		parts := strings.Split(strings.TrimPrefix(trimmedName, "spec."), ".")
		for _, part := range parts {
			if validationErr := validateJSONBKey(part, "spec field segment"); validationErr != nil {
				err = validationErr
				return
			}
		}

		field = "spec"
		for i, part := range parts {
			if i == len(parts)-1 {
				field += fmt.Sprintf("->>'%s'", part)
			} else {
				field += fmt.Sprintf("->'%s'", part)
			}
		}
		return
	}

	// Check for nested field, e.g., status.phase
	fieldParts := strings.Split(trimmedName, ".")
	if len(fieldParts) > 2 {
		err = errors.BadRequest("%s is not a valid field name", name)
		return
	}

	baseName := fieldParts[0]
	if !searchAllowedFields[baseName] {
		err = errors.BadRequest("%s is not a valid field name", name)
		return
	}

	field = trimmedName
	return
}

var searchAllowedFields = map[string]bool{
	"id":           true,
	"name":         true,
	"kind":         true,
	"created_time": true,
	"updated_time": true,
	"deleted_time": true,
	"created_by":   true,
	"updated_by":   true,
	"deleted_by":   true,
	"generation":   true,
	"href":         true,
	"labels":       true,
	"conditions":   true,
	"owner_id":     true,
	"owner_kind":   true,
}

// Condition type validation pattern: PascalCase condition types (e.g., Reconciled, Available, Progressing)
var conditionTypePattern = regexp.MustCompile(`^[A-Z][a-zA-Z0-9]*$`)

// Condition status validation: must be True, False, or Unknown
var validConditionStatuses = map[string]bool{
	"True":    true,
	"False":   true,
	"Unknown": true,
}

// conditionTimeSubfields are condition subfields that store timestamps and require TIMESTAMPTZ casting.
// Note: created_time is intentionally excluded — it reflects when the condition was first created
// and is not useful for Sentinel polling or staleness queries.
var conditionTimeSubfields = map[string]bool{
	"last_updated_time":    true,
	"last_transition_time": true,
}

// conditionIntSubfields are condition subfields that store integers and require INTEGER casting
var conditionIntSubfields = map[string]bool{
	"observed_generation": true,
}

// comparisonOperators maps TSL operator constants to SQL operator strings
var comparisonOperators = map[tsl.Operator]string{
	tsl.OpEQ: "=",
	tsl.OpNE: "!=",
	tsl.OpLT: "<",
	tsl.OpLE: "<=",
	tsl.OpGT: ">",
	tsl.OpGE: ">=",
}

func startsWithLabels(s string) bool {
	return strings.HasPrefix(s, "labels.")
}

func startsWithConditions(s string) bool {
	return strings.HasPrefix(s, "status.conditions.")
}

// orderAllowedFields defines the whitelist of fields that are allowed to be ordered.
// This prevents SQL injection and restricts invalid order queries.
var orderAllowedFields = map[string]bool{
	"id":           true,
	"name":         true,
	"created_time": true,
	"updated_time": true,
	"deleted_time": true,
	"kind":         true,
	"created_by":   true,
	"updated_by":   true,
	"deleted_by":   true,
	"generation":   true,
	"href":         true,
}

// orderPattern matches valid order syntax: field name (letters, digits, underscore) followed by optional asc/desc.
// This regex rejects SQL injection attempts (semicolons, parentheses, dashes, comments, etc).
var orderPattern = regexp.MustCompile(`^[a-z_][a-z_]*(\s+(asc|desc))?$`)

// ArgsToOrder validates and cleans order arguments against the allowed fields whitelist.
// Returns a cleaned list of order clauses in the format ["field direction", ...]
// Empty or whitespace-only strings are silently skipped.
func ArgsToOrder(args []string) (cleanedOrderList []string, err *errors.ServiceError) {
	for _, val := range args {
		// Accept args with trailing and leading spaces
		trimVal := strings.TrimSpace(val)

		// Skip empty strings silently
		if trimVal == "" {
			continue
		}

		// Check for SQL injection attempts before parsing
		if !orderPattern.MatchString(trimVal) {
			return nil, errors.BadRequest("invalid order format '%s': expected 'field' or 'field asc|desc'", val)
		}

		// Each value should be "<field-name>" or "<field-name> asc|desc"
		splitVal := strings.Fields(trimVal)
		lenVal := len(splitVal)

		var field, direction string

		switch lenVal {
		case 2:
			field = splitVal[0]
			direction = splitVal[1]
			if direction != "asc" && direction != "desc" {
				return nil, errors.BadRequest("invalid sort direction '%s': must be 'asc' or 'desc'", direction)
			}
		case 1:
			field = splitVal[0]
			direction = "asc"
		default:
			return nil, errors.BadRequest("invalid order format '%s': expected 'field' or 'field asc|desc'", val)
		}

		// Validate field against orderAllowedFields
		if !orderAllowedFields[field] {
			return nil, errors.BadRequest("field '%s' is not allowed for ordering", field)
		}

		cleanedValue := fmt.Sprintf("%s %s", field, direction)
		cleanedOrderList = append(cleanedOrderList, cleanedValue)
	}

	return cleanedOrderList, nil
}

func GetTableName(g2 *gorm.DB) string {
	if g2.Statement.Parse(g2.Statement.Model) != nil {
		return "xxx"
	}
	if g2.Statement.Schema != nil {
		return g2.Statement.Schema.Table
	} else {
		name := reflect.TypeOf(g2.Statement.Model).Elem().Name()
		return inflection.Plural(strings.ToLower(name))
	}
}
