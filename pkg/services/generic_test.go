package services

import (
	"context"
	"testing"

	"github.com/openshift-hyperfleet/hyperfleet-api/pkg/dao"
	"github.com/openshift-hyperfleet/hyperfleet-api/pkg/db"
	dbmocks "github.com/openshift-hyperfleet/hyperfleet-api/pkg/db/mocks"

	"github.com/onsi/gomega/types"
	"github.com/yaacov/tree-search-language/v6/pkg/tsl"

	"github.com/openshift-hyperfleet/hyperfleet-api/pkg/api"
	"github.com/openshift-hyperfleet/hyperfleet-api/pkg/errors"

	. "github.com/onsi/gomega"
)

func TestSQLTranslation(t *testing.T) {
	RegisterTestingT(t)
	var dbFactory db.SessionFactory = dbmocks.NewMockSessionFactory()
	defer dbFactory.Close() //nolint:errcheck

	g := dao.NewGenericDao(dbFactory)
	genericService := sqlGenericService{genericDao: g}

	// ill-formatted search should be rejected
	errorTests := []map[string]interface{}{
		{
			"search": "= = =",
			"error":  errors.CodeBadRequest + ": Failed to parse search query: = = =",
		},
	}
	for _, test := range errorTests {
		var list []api.Resource
		search := test["search"].(string)
		errorMsg := test["error"].(string)
		listCtx, model, serviceErr := genericService.newListContext(
			context.Background(), &ListArguments{Search: search}, &list,
		)
		Expect(serviceErr).ToNot(HaveOccurred())
		d := g.GetInstanceDao(context.Background(), model)
		_, serviceErr = genericService.buildSearch(listCtx, &d)
		Expect(serviceErr).To(HaveOccurred())
		Expect(serviceErr.Type).To(Equal(errors.ErrorTypeBadRequest))
		Expect(serviceErr.Error()).To(Equal(errorMsg))
	}

	// tests for sql parsing — now uses WalkToSQL directly
	// Note: WalkToSQL always prefixes bare columns with "resources."
	sqlTests := []map[string]interface{}{
		{
			"search": "created_by in ['ooo.openshift']",
			"sql":    "resources.created_by IN (?)",
			"values": ConsistOf("ooo.openshift"),
		},
		{
			"search": "spec.region = 'us-east-1'",
			"sql":    "spec->>'region' = ?",
			"values": ConsistOf("us-east-1"),
		},
		{
			"search": "spec.release.version = '2'",
			"sql":    "spec->'release'->>'version' = ?",
			"values": ConsistOf("2"),
		},
		{
			"search": "spec.release.notes.url = 'https://example.com'",
			"sql":    "spec->'release'->'notes'->>'url' = ?",
			"values": ConsistOf("https://example.com"),
		},
		{
			"search": "spec.replicas > 9",
			"sql":    "CAST(spec->>'replicas' AS numeric) > ?",
			"values": ConsistOf(float64(9)),
		},
		{
			"search": "spec.release.version > 9",
			"sql":    "CAST(spec->'release'->>'version' AS numeric) > ?",
			"values": ConsistOf(float64(9)),
		},
		{
			"search": "spec.release.config.replicas > 9",
			"sql":    "CAST(spec->'release'->'config'->>'replicas' AS numeric) > ?",
			"values": ConsistOf(float64(9)),
		},
		{
			"search": "id = 'cls-123'",
			"sql":    "resources.id = ?",
			"values": ConsistOf("cls-123"),
		},
		{
			"search": "properties.owner = 'team_a'",
			"sql":    "properties ->> 'owner' = ?",
			"values": ConsistOf("team_a"),
		},
	}
	for _, test := range sqlTests {
		search := test["search"].(string)
		sqlReal := test["sql"].(string)
		valuesReal := test["values"].(types.GomegaMatcher)

		sql, values, serviceErr := db.WalkToSQL(mustParseTSL(t, search))
		Expect(serviceErr).ToNot(HaveOccurred(), "WalkToSQL failed for: %s", search)
		Expect(sql).To(Equal(sqlReal), "SQL mismatch for: %s", search)
		Expect(values).To(valuesReal, "values mismatch for: %s", search)
	}
}

func mustParseTSL(t *testing.T, search string) *tsl.TSLNode {
	t.Helper()
	node, err := tsl.ParseTSL(search)
	if err != nil {
		t.Fatalf("ParseTSL(%q) failed: %v", search, err)
	}
	return node
}
