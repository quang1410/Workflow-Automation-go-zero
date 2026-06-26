package engine

import (
	"context"
	"fmt"
)

// StepDef mirrors the JSON stored in workflows.steps JSONB column.
type StepDef struct {
	ID     string         `json:"id"`
	Type   string         `json:"type"`
	Config map[string]any `json:"config"`
}

// StepRunner is the interface every step type must implement.
type StepRunner interface {
	Run(ctx context.Context, input map[string]any) (map[string]any, error)
}

// NewStepRunner returns a concrete runner for the given step definition.
func NewStepRunner(step StepDef) (StepRunner, error) {
	switch step.Type {
	case "http_request":
		return newHTTPRequestRunner(step.Config)
	case "transform":
		return newTransformRunner(step.Config)
	case "delay":
		return newDelayRunner(step.Config)
	case "condition":
		return newConditionRunner(step.Config)
	case "send_email":
		return newSendEmailRunner(step.Config)
	default:
		return nil, fmt.Errorf("unknown step type: %s", step.Type)
	}
}
