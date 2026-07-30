package db

import (
	"testing"

	. "github.com/onsi/gomega"
	"github.com/yaacov/tree-search-language/v6/pkg/tsl"
)

func parseAndWalk(t *testing.T, search string) (string, []any) {
	t.Helper()
	RegisterTestingT(t)
	node, err := tsl.ParseTSL(search)
	Expect(err).ToNot(HaveOccurred())
	sql, args, svcErr := WalkToSQL(node)
	Expect(svcErr).To(BeNil())
	return sql, args
}

func TestWalkToSQL_BasicComparisons(t *testing.T) {
	tests := []struct {
		name         string
		search       string
		expectedSQL  string
		expectedArgs []any
	}{
		{
			name:         "string equality",
			search:       "name = 'foo'",
			expectedSQL:  "resources.name = ?",
			expectedArgs: []any{"foo"},
		},
		{
			name:         "string not equal",
			search:       "name != 'foo'",
			expectedSQL:  "resources.name != ?",
			expectedArgs: []any{"foo"},
		},
		{
			name:         "numeric greater than",
			search:       "generation > 1",
			expectedSQL:  "resources.generation > ?",
			expectedArgs: []any{float64(1)},
		},
		{
			name:         "AND",
			search:       "name = 'foo' and kind = 'cluster'",
			expectedSQL:  "(resources.name = ?) AND (resources.kind = ?)",
			expectedArgs: []any{"foo", "cluster"},
		},
		{
			name:         "OR",
			search:       "name = 'foo' or name = 'bar'",
			expectedSQL:  "(resources.name = ?) OR (resources.name = ?)",
			expectedArgs: []any{"foo", "bar"},
		},
		{
			name:         "IN",
			search:       "created_by in ['alice', 'bob']",
			expectedSQL:  "resources.created_by IN (?, ?)",
			expectedArgs: []any{"alice", "bob"},
		},
		{
			name:         "NOT",
			search:       "not (name = 'foo')",
			expectedSQL:  "NOT (resources.name = ?)",
			expectedArgs: []any{"foo"},
		},
		{
			name:         "less than",
			search:       "generation < 10",
			expectedSQL:  "resources.generation < ?",
			expectedArgs: []any{float64(10)},
		},
		{
			name:         "less than or equal",
			search:       "generation <= 5",
			expectedSQL:  "resources.generation <= ?",
			expectedArgs: []any{float64(5)},
		},
		{
			name:         "greater than or equal",
			search:       "generation >= 1",
			expectedSQL:  "resources.generation >= ?",
			expectedArgs: []any{float64(1)},
		},
		{
			name:         "double NOT",
			search:       "not (not (name = 'foo'))",
			expectedSQL:  "NOT (NOT (resources.name = ?))",
			expectedArgs: []any{"foo"},
		},
		{
			name:         "negative number",
			search:       "generation > -1",
			expectedSQL:  "resources.generation > (-?)",
			expectedArgs: []any{float64(1)},
		},
		{
			name:         "spec with negative number",
			search:       "spec.replicas > -5",
			expectedSQL:  "CAST(spec->>'replicas' AS numeric) > (-?)",
			expectedArgs: []any{float64(5)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args := parseAndWalk(t, tt.search)
			Expect(sql).To(Equal(tt.expectedSQL))
			Expect(args).To(Equal(tt.expectedArgs))
		})
	}
}

func TestWalkToSQL_SpecJSONB(t *testing.T) {
	tests := []struct {
		name         string
		search       string
		expectedSQL  string
		expectedArgs []any
	}{
		{
			name:         "single level",
			search:       "spec.region = 'us-east'",
			expectedSQL:  "spec->>'region' = ?",
			expectedArgs: []any{"us-east"},
		},
		{
			name:         "nested",
			search:       "spec.release.channel = 'stable'",
			expectedSQL:  "spec->'release'->>'channel' = ?",
			expectedArgs: []any{"stable"},
		},
		{
			name:         "deep nested",
			search:       "spec.release.config.zone = 'a'",
			expectedSQL:  "spec->'release'->'config'->>'zone' = ?",
			expectedArgs: []any{"a"},
		},
		{
			name:         "numeric CAST",
			search:       "spec.replicas > 9",
			expectedSQL:  "CAST(spec->>'replicas' AS numeric) > ?",
			expectedArgs: []any{float64(9)},
		},
		{
			name:         "nested numeric CAST",
			search:       "spec.release.version > 3",
			expectedSQL:  "CAST(spec->'release'->>'version' AS numeric) > ?",
			expectedArgs: []any{float64(3)},
		},
		{
			name:         "string comparison no CAST",
			search:       "spec.channel = 'dev'",
			expectedSQL:  "spec->>'channel' = ?",
			expectedArgs: []any{"dev"},
		},
		{
			name:         "reverse CAST (number on left)",
			search:       "9 < spec.replicas",
			expectedSQL:  "? < CAST(spec->>'replicas' AS numeric)",
			expectedArgs: []any{float64(9)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args := parseAndWalk(t, tt.search)
			Expect(sql).To(Equal(tt.expectedSQL))
			Expect(args).To(Equal(tt.expectedArgs))
		})
	}
}

func TestWalkToSQL_Properties(t *testing.T) {
	sql, args := parseAndWalk(t, "properties.owner = 'team-a'")
	Expect(sql).To(Equal("properties ->> 'owner' = ?"))
	Expect(args).To(Equal([]any{"team-a"}))
}

func TestWalkToSQL_Labels(t *testing.T) {
	tests := []struct {
		name         string
		search       string
		expectedSQL  string
		expectedArgs []any
	}{
		{
			name:   "label equality",
			search: "labels.env = 'prod'",
			expectedSQL: "(SELECT value FROM resource_labels " +
				"WHERE resource_labels.resource_id = resources.id " +
				"AND resource_labels.key = 'env') = ?",
			expectedArgs: []any{"prod"},
		},
		{
			name:   "label not equal",
			search: "labels.env != 'staging'",
			expectedSQL: "(SELECT value FROM resource_labels " +
				"WHERE resource_labels.resource_id = resources.id " +
				"AND resource_labels.key = 'env') != ?",
			expectedArgs: []any{"staging"},
		},
		{
			name:   "label IN",
			search: "labels.env in ['prod', 'staging']",
			expectedSQL: "(SELECT value FROM resource_labels " +
				"WHERE resource_labels.resource_id = resources.id " +
				"AND resource_labels.key = 'env') IN (?, ?)",
			expectedArgs: []any{"prod", "staging"},
		},
		{
			name:   "label combined with regular field",
			search: "labels.env = 'prod' and name = 'foo'",
			expectedSQL: "((SELECT value FROM resource_labels " +
				"WHERE resource_labels.resource_id = resources.id " +
				"AND resource_labels.key = 'env') = ?) AND (resources.name = ?)",
			expectedArgs: []any{"prod", "foo"},
		},
		{
			name:   "label with OR (now supported)",
			search: "labels.env = 'prod' or name = 'foo'",
			expectedSQL: "((SELECT value FROM resource_labels " +
				"WHERE resource_labels.resource_id = resources.id " +
				"AND resource_labels.key = 'env') = ?) OR (resources.name = ?)",
			expectedArgs: []any{"prod", "foo"},
		},
		{
			name:   "multiple labels AND",
			search: "labels.env = 'prod' and labels.region = 'us-east'",
			expectedSQL: "((SELECT value FROM resource_labels" +
				" WHERE resource_labels.resource_id = resources.id" +
				" AND resource_labels.key = 'env') = ?) AND " +
				"((SELECT value FROM resource_labels" +
				" WHERE resource_labels.resource_id = resources.id" +
				" AND resource_labels.key = 'region') = ?)",
			expectedArgs: []any{"prod", "us-east"},
		},
		{
			name:   "label with NOT (now supported)",
			search: "not (labels.env = 'prod')",
			expectedSQL: "NOT ((SELECT value FROM resource_labels " +
				"WHERE resource_labels.resource_id = resources.id " +
				"AND resource_labels.key = 'env') = ?)",
			expectedArgs: []any{"prod"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args := parseAndWalk(t, tt.search)
			Expect(sql).To(Equal(tt.expectedSQL))
			Expect(args).To(Equal(tt.expectedArgs))
		})
	}
}

func TestWalkToSQL_Conditions(t *testing.T) {
	tests := []struct {
		name         string
		search       string
		expectedSQL  string
		expectedArgs []any
	}{
		{
			name:   "condition status",
			search: "status.conditions.Reconciled = 'True'",
			expectedSQL: "(SELECT resource_conditions.status FROM resource_conditions" +
				" WHERE resource_conditions.resource_id = resources.id AND resource_conditions.type = 'Reconciled') = ?",
			expectedArgs: []any{"True"},
		},
		{
			name:   "condition subfield timestamp",
			search: "status.conditions.Reconciled.last_updated_time < '2026-03-06T00:00:00Z'",
			expectedSQL: "(SELECT resource_conditions.last_updated_time FROM resource_conditions" +
				" WHERE resource_conditions.resource_id = resources.id AND resource_conditions.type = 'Reconciled') < ?",
			expectedArgs: []any{"2026-03-06T00:00:00Z"},
		},
		{
			name:   "condition subfield observed_generation",
			search: "status.conditions.Reconciled.observed_generation < 5",
			expectedSQL: "(SELECT resource_conditions.observed_generation FROM resource_conditions" +
				" WHERE resource_conditions.resource_id = resources.id AND resource_conditions.type = 'Reconciled') < ?",
			expectedArgs: []any{float64(5)},
		},
		{
			name:   "condition status not equal",
			search: "status.conditions.Reconciled != 'True'",
			expectedSQL: "(SELECT resource_conditions.status" +
				" FROM resource_conditions" +
				" WHERE resource_conditions.resource_id = resources.id" +
				" AND resource_conditions.type = 'Reconciled') != ?",
			expectedArgs: []any{"True"},
		},
		{
			name:   "condition last_transition_time",
			search: "status.conditions.Available.last_transition_time < '2026-01-01T00:00:00Z'",
			expectedSQL: "(SELECT resource_conditions.last_transition_time" +
				" FROM resource_conditions" +
				" WHERE resource_conditions.resource_id = resources.id" +
				" AND resource_conditions.type = 'Available') < ?",
			expectedArgs: []any{"2026-01-01T00:00:00Z"},
		},
		{
			name:   "condition observed_generation greater than",
			search: "status.conditions.Reconciled.observed_generation > 5",
			expectedSQL: "(SELECT resource_conditions.observed_generation" +
				" FROM resource_conditions" +
				" WHERE resource_conditions.resource_id = resources.id" +
				" AND resource_conditions.type = 'Reconciled') > ?",
			expectedArgs: []any{float64(5)},
		},
		{
			name:   "condition observed_generation not equal",
			search: "status.conditions.Reconciled.observed_generation != 0",
			expectedSQL: "(SELECT resource_conditions.observed_generation" +
				" FROM resource_conditions" +
				" WHERE resource_conditions.resource_id = resources.id" +
				" AND resource_conditions.type = 'Reconciled') != ?",
			expectedArgs: []any{float64(0)},
		},
		{
			name:   "condition with NOT (now supported)",
			search: "not (status.conditions.Reconciled = 'True')",
			expectedSQL: "NOT ((SELECT resource_conditions.status FROM resource_conditions" +
				" WHERE resource_conditions.resource_id = resources.id AND resource_conditions.type = 'Reconciled') = ?)",
			expectedArgs: []any{"True"},
		},
		{
			name:   "condition with OR (now supported)",
			search: "status.conditions.Reconciled = 'True' or name = 'foo'",
			expectedSQL: "((SELECT resource_conditions.status FROM resource_conditions" +
				" WHERE resource_conditions.resource_id = resources.id" +
				" AND resource_conditions.type = 'Reconciled') = ?)" +
				" OR (resources.name = ?)",
			expectedArgs: []any{"True", "foo"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args := parseAndWalk(t, tt.search)
			Expect(sql).To(Equal(tt.expectedSQL))
			Expect(args).To(Equal(tt.expectedArgs))
		})
	}
}

func TestWalkToSQL_ConditionValidation(t *testing.T) {
	tests := []struct {
		name          string
		search        string
		errorContains string
	}{
		{
			name:          "invalid condition status",
			search:        "status.conditions.Reconciled = 'Invalid'",
			errorContains: "condition status 'Invalid' is invalid",
		},
		{
			name:          "lowercase condition type",
			search:        "status.conditions.ready = 'True'",
			errorContains: "must be PascalCase",
		},
		{
			name:          "unsupported subfield",
			search:        "status.conditions.Reconciled.unknown_field < '2026-01-01T00:00:00Z'",
			errorContains: "not supported",
		},
		{
			name:          "invalid timestamp format",
			search:        "status.conditions.Reconciled.last_updated_time < 'not-a-timestamp'",
			errorContains: "expected RFC3339 format",
		},
		{
			name:          "float for integer subfield",
			search:        "status.conditions.Reconciled.observed_generation < 3.5",
			errorContains: "expected integer value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			RegisterTestingT(t)
			node, err := tsl.ParseTSL(tt.search)
			Expect(err).ToNot(HaveOccurred())
			_, _, svcErr := WalkToSQL(node)
			Expect(svcErr).ToNot(BeNil())
			Expect(svcErr.Error()).To(ContainSubstring(tt.errorContains))
		})
	}
}

func TestWalkToSQL_FieldValidation(t *testing.T) {
	tests := []struct {
		name          string
		search        string
		errorContains string
	}{
		{
			name:          "invalid field",
			search:        "invalid_field = 'foo'",
			errorContains: "not a valid field name",
		},
		{
			name:          "spec uppercase key",
			search:        "spec.ReleaseImage = 'x'",
			errorContains: "invalid",
		},
		{
			name:          "sum operator rejected",
			search:        "sum(generation) > 0",
			errorContains: "unsupported operator",
		},
		{
			name:          "len operator rejected",
			search:        "len(name) > 0",
			errorContains: "unsupported operator",
		},
		{
			name:          "empty label key",
			search:        "labels. = 'x'",
			errorContains: "label key cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			RegisterTestingT(t)
			node, err := tsl.ParseTSL(tt.search)
			Expect(err).ToNot(HaveOccurred())
			_, _, svcErr := WalkToSQL(node)
			Expect(svcErr).ToNot(BeNil())
			Expect(svcErr.Error()).To(ContainSubstring(tt.errorContains))
		})
	}
}

func TestWalkToSQL_LIKE(t *testing.T) {
	tests := []struct {
		name         string
		search       string
		expectedSQL  string
		expectedArgs []any
	}{
		{
			name:         "LIKE",
			search:       "name like 'prod%'",
			expectedSQL:  "resources.name LIKE ?",
			expectedArgs: []any{"prod%"},
		},
		{
			name:         "ILIKE",
			search:       "name ilike 'PROD%'",
			expectedSQL:  "resources.name ILIKE ?",
			expectedArgs: []any{"PROD%"},
		},
		{
			name:         "LIKE on spec field",
			search:       "spec.region like 'us%'",
			expectedSQL:  "spec->>'region' LIKE ?",
			expectedArgs: []any{"us%"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args := parseAndWalk(t, tt.search)
			Expect(sql).To(Equal(tt.expectedSQL))
			Expect(args).To(Equal(tt.expectedArgs))
		})
	}
}

func TestWalkToSQL_BETWEEN(t *testing.T) {
	sql, args := parseAndWalk(t, "generation between 1 and 10")
	Expect(sql).To(Equal("resources.generation BETWEEN ? AND ?"))
	Expect(args).To(Equal([]any{float64(1), float64(10)}))
}

func TestWalkToSQL_IsNull(t *testing.T) {
	tests := []struct {
		name        string
		search      string
		expectedSQL string
	}{
		{
			name:        "name is null",
			search:      "name is null",
			expectedSQL: "resources.name IS NULL",
		},
		{
			name:        "deleted_time is null",
			search:      "deleted_time is null",
			expectedSQL: "resources.deleted_time IS NULL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args := parseAndWalk(t, tt.search)
			Expect(sql).To(Equal(tt.expectedSQL))
			Expect(args).To(BeEmpty())
		})
	}
}

func TestWalkToSQL_TwoPartField(t *testing.T) {
	RegisterTestingT(t)
	node, err := tsl.ParseTSL("status.phase = 'Active'")
	Expect(err).ToNot(HaveOccurred())
	_, _, svcErr := WalkToSQL(node)
	Expect(svcErr).ToNot(BeNil())
	Expect(svcErr.Error()).To(ContainSubstring("not a valid field name"))
}

func TestWalkToSQL_Combined(t *testing.T) {
	tests := []struct {
		name         string
		search       string
		expectedSQL  string
		expectedArgs []any
	}{
		{
			name:   "spec + label + condition",
			search: "spec.region = 'us-east' and labels.env = 'prod' and status.conditions.Reconciled = 'True'",
			expectedSQL: "((spec->>'region' = ?) AND " +
				"((SELECT value FROM resource_labels" +
				" WHERE resource_labels.resource_id = resources.id" +
				" AND resource_labels.key = 'env') = ?)) AND " +
				"((SELECT resource_conditions.status FROM resource_conditions" +
				" WHERE resource_conditions.resource_id = resources.id" +
				" AND resource_conditions.type = 'Reconciled') = ?)",
			expectedArgs: []any{"us-east", "prod", "True"},
		},
		{
			name:         "spec numeric + non-spec numeric",
			search:       "spec.replicas > 9 and generation > 1",
			expectedSQL:  "(CAST(spec->>'replicas' AS numeric) > ?) AND (resources.generation > ?)",
			expectedArgs: []any{float64(9), float64(1)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args := parseAndWalk(t, tt.search)
			Expect(sql).To(Equal(tt.expectedSQL))
			Expect(args).To(Equal(tt.expectedArgs))
		})
	}
}
