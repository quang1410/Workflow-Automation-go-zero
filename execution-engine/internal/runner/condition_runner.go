package runner

import (
	"context"
	"fmt"
	"strconv"
)

type conditionRunner struct {
	field    string
	operator string
	value    string
}

func newConditionRunner(cfg map[string]any) (*conditionRunner, error) {
	field, _ := cfg["field"].(string)
	operator, _ := cfg["operator"].(string)
	value, _ := cfg["value"].(string)
	if field == "" || operator == "" {
		return nil, fmt.Errorf("condition: missing field or operator")
	}
	return &conditionRunner{field: field, operator: operator, value: value}, nil
}

func (r *conditionRunner) Run(ctx context.Context, input map[string]any) (map[string]any, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	actual := fmt.Sprintf("%v", input[r.field])

	var result bool
	switch r.operator {
	case "eq", "==":
		result = actual == r.value
	case "neq", "!=":
		result = actual != r.value
	case "gt", ">":
		a, _ := strconv.ParseFloat(actual, 64)
		b, _ := strconv.ParseFloat(r.value, 64)
		result = a > b
	case "lt", "<":
		a, _ := strconv.ParseFloat(actual, 64)
		b, _ := strconv.ParseFloat(r.value, 64)
		result = a < b
	default:
		return nil, fmt.Errorf("condition: unknown operator %q", r.operator)
	}

	return map[string]any{
		"condition": result,
		"input":     input,
	}, nil
}
