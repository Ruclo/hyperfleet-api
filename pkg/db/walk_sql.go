package db

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/openshift-hyperfleet/hyperfleet-api/pkg/errors"
	"github.com/yaacov/tree-search-language/v6/pkg/tsl"
)

// WalkToSQL walks the TSL tree (read-only) and emits a SQL WHERE fragment
// with positional bind values. All transformations (field mapping, JSONB
// accessors, label/condition subqueries, table prefixes, CAST wrapping)
// happen during emission — the tree is never modified.
func WalkToSQL(node *tsl.TSLNode) (string, []any, *errors.ServiceError) {
	return walkNode(node)
}

// --- walk functions ---

func walkNode(n *tsl.TSLNode) (string, []any, *errors.ServiceError) {
	if n == nil {
		return "", nil, nil
	}

	switch n.Type() {
	case tsl.KindIdentifier:
		return walkIdentifier(n)
	case tsl.KindStringLiteral:
		return walkStringLiteral(n)
	case tsl.KindNumericLiteral:
		return walkNumericLiteral(n)
	case tsl.KindBooleanLiteral:
		return walkBooleanLiteral(n)
	case tsl.KindTimestampLiteral:
		return walkTimestampLiteral(n)
	case tsl.KindNullLiteral:
		return walkNullLiteral()
	case tsl.KindBinaryExpr:
		return walkBinaryExpr(n)
	case tsl.KindUnaryExpr:
		return walkUnaryExpr(n)
	case tsl.KindArrayLiteral:
		return walkArrayLiteral(n)
	default:
		return "", nil, errors.BadRequest("unsupported node type in search query")
	}
}

func walkIdentifier(n *tsl.TSLNode) (string, []any, *errors.ServiceError) {
	name, _ := n.AsString()
	col, err := resolveColumn(name)
	if err != nil {
		return "", nil, err
	}
	return col, nil, nil
}

func walkStringLiteral(n *tsl.TSLNode) (string, []any, *errors.ServiceError) {
	s, _ := n.AsString()
	return "?", []any{s}, nil
}

func walkNumericLiteral(n *tsl.TSLNode) (string, []any, *errors.ServiceError) {
	f, _ := n.AsFloat64()
	return "?", []any{f}, nil
}

func walkBooleanLiteral(n *tsl.TSLNode) (string, []any, *errors.ServiceError) {
	b, _ := n.AsBool()
	return "?", []any{b}, nil
}

func walkTimestampLiteral(n *tsl.TSLNode) (string, []any, *errors.ServiceError) {
	v := n.Value()
	if t, ok := v.(time.Time); ok {
		return "?", []any{t.Format(time.RFC3339Nano)}, nil
	}
	return "?", []any{v}, nil
}

func walkNullLiteral() (string, []any, *errors.ServiceError) {
	return "NULL", nil, nil
}

func walkUnaryExpr(n *tsl.TSLNode) (string, []any, *errors.ServiceError) {
	op, _ := n.AsExprOp()
	childSQL, childArgs, err := walkNode(op.Right)
	if err != nil {
		return "", nil, err
	}
	switch op.Operator {
	case tsl.OpNot:
		return fmt.Sprintf("NOT (%s)", childSQL), childArgs, nil
	case tsl.OpUMinus:
		return fmt.Sprintf("(-%s)", childSQL), childArgs, nil
	default:
		return "", nil, errors.BadRequest(
			"unsupported operator '%s' in search query", op.Operator)
	}
}

func walkBinaryExpr(n *tsl.TSLNode) (string, []any, *errors.ServiceError) {
	op, _ := n.AsExprOp()

	switch op.Operator {
	case tsl.OpAnd, tsl.OpOr:
		return walkLogical(op)
	case tsl.OpIn:
		return walkIn(op)
	case tsl.OpBetween:
		return walkBetween(op)
	case tsl.OpIs:
		return walkIsNull(op)
	case tsl.OpLike, tsl.OpILike:
		return walkStringMatch(op)
	default:
		return walkComparison(op)
	}
}

func walkLogical(op tsl.TSLExpressionOp) (string, []any, *errors.ServiceError) {
	leftSQL, leftArgs, err := walkNode(op.Left)
	if err != nil {
		return "", nil, err
	}
	rightSQL, rightArgs, err := walkNode(op.Right)
	if err != nil {
		return "", nil, err
	}

	keyword := "AND"
	if op.Operator == tsl.OpOr {
		keyword = "OR"
	}

	return fmt.Sprintf("(%s) %s (%s)", leftSQL, keyword, rightSQL),
		append(leftArgs, rightArgs...), nil
}

func walkComparison(op tsl.TSLExpressionOp) (string, []any, *errors.ServiceError) {
	leftSQL, leftArgs, err := walkNode(op.Left)
	if err != nil {
		return "", nil, err
	}
	rightSQL, rightArgs, err := walkNode(op.Right)
	if err != nil {
		return "", nil, err
	}

	sqlOp, ok := comparisonOperators[op.Operator]
	if !ok {
		return "", nil, errors.BadRequest("unsupported comparison operator")
	}

	if strings.HasPrefix(leftSQL, "spec->") && len(rightArgs) > 0 {
		if _, isNum := rightArgs[0].(float64); isNum {
			leftSQL = fmt.Sprintf("CAST(%s AS numeric)", leftSQL)
		}
	}
	if strings.HasPrefix(rightSQL, "spec->") && len(leftArgs) > 0 {
		if _, isNum := leftArgs[0].(float64); isNum {
			rightSQL = fmt.Sprintf("CAST(%s AS numeric)", rightSQL)
		}
	}

	if validateErr := validateConditionComparison(leftSQL, rightArgs); validateErr != nil {
		return "", nil, validateErr
	}

	return fmt.Sprintf("%s %s %s", leftSQL, sqlOp, rightSQL),
		append(leftArgs, rightArgs...), nil
}

func walkIn(op tsl.TSLExpressionOp) (string, []any, *errors.ServiceError) {
	leftSQL, leftArgs, err := walkNode(op.Left)
	if err != nil {
		return "", nil, err
	}

	arr, ok := op.Right.AsArray()
	if !ok {
		return "", nil, errors.BadRequest("expected array on right side of IN")
	}

	placeholders := make([]string, len(arr.Values))
	var rightArgs []any
	for i, v := range arr.Values {
		s, a, walkErr := walkNode(v)
		if walkErr != nil {
			return "", nil, walkErr
		}
		placeholders[i] = s
		rightArgs = append(rightArgs, a...)
	}

	return fmt.Sprintf("%s IN (%s)", leftSQL, strings.Join(placeholders, ", ")),
		append(leftArgs, rightArgs...), nil
}

func walkBetween(op tsl.TSLExpressionOp) (string, []any, *errors.ServiceError) {
	leftSQL, leftArgs, err := walkNode(op.Left)
	if err != nil {
		return "", nil, err
	}

	arr, ok := op.Right.AsArray()
	if !ok || len(arr.Values) != 2 {
		return "", nil, errors.BadRequest("BETWEEN requires exactly 2 values")
	}

	lowSQL, lowArgs, err := walkNode(arr.Values[0])
	if err != nil {
		return "", nil, err
	}
	highSQL, highArgs, err := walkNode(arr.Values[1])
	if err != nil {
		return "", nil, err
	}

	var args []any
	args = append(args, leftArgs...)
	args = append(args, lowArgs...)
	args = append(args, highArgs...)
	return fmt.Sprintf("%s BETWEEN %s AND %s", leftSQL, lowSQL, highSQL), args, nil
}

func walkIsNull(op tsl.TSLExpressionOp) (string, []any, *errors.ServiceError) {
	leftSQL, leftArgs, err := walkNode(op.Left)
	if err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("%s IS NULL", leftSQL), leftArgs, nil
}

func walkStringMatch(op tsl.TSLExpressionOp) (string, []any, *errors.ServiceError) {
	leftSQL, leftArgs, err := walkNode(op.Left)
	if err != nil {
		return "", nil, err
	}
	rightSQL, rightArgs, err := walkNode(op.Right)
	if err != nil {
		return "", nil, err
	}

	keyword := "LIKE"
	if op.Operator == tsl.OpILike {
		keyword = "ILIKE"
	}

	return fmt.Sprintf("%s %s %s", leftSQL, keyword, rightSQL),
		append(leftArgs, rightArgs...), nil
}

func walkArrayLiteral(n *tsl.TSLNode) (string, []any, *errors.ServiceError) {
	arr, ok := n.AsArray()
	if !ok {
		return "", nil, errors.BadRequest("expected array")
	}

	placeholders := make([]string, len(arr.Values))
	var args []any
	for i, v := range arr.Values {
		s, a, err := walkNode(v)
		if err != nil {
			return "", nil, err
		}
		placeholders[i] = s
		args = append(args, a...)
	}

	return fmt.Sprintf("(%s)", strings.Join(placeholders, ", ")), args, nil
}

// --- resolve functions ---

func resolveColumn(name string) (string, *errors.ServiceError) {
	trimmed := strings.TrimSpace(name)

	if startsWithLabels(trimmed) {
		return resolveLabelColumn(trimmed)
	}

	if startsWithConditions(trimmed) {
		return resolveConditionColumn(trimmed)
	}

	return resolveField(trimmed)
}

func resolveLabelColumn(name string) (string, *errors.ServiceError) {
	key, _ := strings.CutPrefix(name, "labels.")
	if key == "" {
		return "", errors.BadRequest("label key cannot be empty")
	}
	return fmt.Sprintf(
		"(SELECT value FROM resource_labels"+
			" WHERE resource_labels.resource_id = resources.id"+
			" AND resource_labels.key = '%s')",
		key,
	), nil
}

func resolveConditionColumn(name string) (string, *errors.ServiceError) {
	parts := strings.Split(name, ".")
	if len(parts) < 3 || len(parts) > 4 ||
		parts[0] != "status" || parts[1] != "conditions" {
		return "", errors.BadRequest("invalid condition field path: %s", name)
	}

	conditionType := parts[2]
	if !conditionTypePattern.MatchString(conditionType) {
		return "", errors.BadRequest(
			"condition type '%s' is invalid: must be PascalCase"+
				" (e.g., Reconciled, Available)", conditionType,
		)
	}

	subfield := "status"
	if len(parts) == 4 {
		subfield = parts[3]
		if !conditionTimeSubfields[subfield] && !conditionIntSubfields[subfield] {
			return "", errors.BadRequest(
				"condition subfield '%s' is not supported;"+
					" use last_updated_time, last_transition_time,"+
					" or observed_generation",
				subfield,
			)
		}
	}

	return fmt.Sprintf(
		"(SELECT resource_conditions.%s FROM resource_conditions"+
			" WHERE resource_conditions.resource_id = resources.id"+
			" AND resource_conditions.type = '%s')",
		subfield, conditionType,
	), nil
}

func resolveField(name string) (string, *errors.ServiceError) {
	field, err := getField(name)
	if err != nil {
		return "", err
	}

	if !strings.Contains(field, "->") && !strings.Contains(field, ".") {
		field = fmt.Sprintf("resources.%s", field)
	}

	return field, nil
}

// --- validation ---

func validateConditionComparison(
	leftSQL string, rightArgs []any,
) *errors.ServiceError {
	if !strings.HasPrefix(leftSQL, "(SELECT resource_conditions.") ||
		len(rightArgs) == 0 {
		return nil
	}

	if strings.Contains(leftSQL, "resource_conditions.status") {
		if s, ok := rightArgs[0].(string); ok && !validConditionStatuses[s] {
			return errors.BadRequest(
				"condition status '%s' is invalid:"+
					" must be True, False, or Unknown", s,
			)
		}
	}

	if strings.Contains(leftSQL, "resource_conditions.last_updated_time") ||
		strings.Contains(leftSQL, "resource_conditions.last_transition_time") {
		if s, ok := rightArgs[0].(string); ok {
			if _, parseErr := time.Parse(time.RFC3339, s); parseErr != nil {
				return errors.BadRequest(
					"invalid timestamp for condition subfield:" +
						" expected RFC3339 format" +
						" (e.g., 2026-01-01T00:00:00Z)",
				)
			}
		}
	}

	if strings.Contains(leftSQL, "resource_conditions.observed_generation") {
		if f, ok := rightArgs[0].(float64); ok {
			if f != math.Trunc(f) {
				return errors.BadRequest(
					"expected integer value for condition"+
						" subfield observed_generation, got %v", f,
				)
			}
			if f < math.MinInt32 || f > math.MaxInt32 {
				return errors.BadRequest(
					"value %v is out of 32-bit integer range"+
						" for condition subfield observed_generation", f,
				)
			}
		}
	}

	return nil
}
