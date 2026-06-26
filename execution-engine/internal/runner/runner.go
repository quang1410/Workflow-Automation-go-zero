package runner

import (
	"context"
	"fmt"

	goredis "github.com/redis/go-redis/v9"
)

type StepDef struct {
	ID     string         `json:"id"`
	Type   string         `json:"type"`
	Config map[string]any `json:"config"`
}

type StepRunner interface {
	Run(ctx context.Context, input map[string]any) (map[string]any, error)
}

// RunnerFactory holds shared dependencies (API keys, Redis) needed by runners
// that call external services. Pass a stream channel so ai_task can publish
// token events for real-time SSE streaming.
type RunnerFactory struct {
	AnthropicAPIKey string
	OpenAIAPIKey    string
	Rdb             *goredis.Client
}

// New creates a StepRunner for the given step definition.
// streamChan is the Redis pub/sub channel name used by ai_task to stream tokens;
// pass an empty string to disable token streaming.
func (f *RunnerFactory) New(step StepDef, streamChan string) (StepRunner, error) {
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
	case "ai_task":
		return newAITaskRunner(step.Config, f.AnthropicAPIKey, f.OpenAIAPIKey, f.Rdb, streamChan, step.ID)
	default:
		return nil, fmt.Errorf("unknown step type: %s", step.Type)
	}
}

// NewStepRunner is a convenience wrapper for non-AI steps (no API keys needed).
func NewStepRunner(step StepDef) (StepRunner, error) {
	return (&RunnerFactory{}).New(step, "")
}
