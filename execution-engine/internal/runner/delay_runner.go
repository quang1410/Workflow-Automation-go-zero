package runner

import (
	"context"
	"fmt"
	"time"
)

type delayRunner struct {
	duration time.Duration
}

func newDelayRunner(cfg map[string]any) (*delayRunner, error) {
	seconds := 1.0
	if s, ok := cfg["seconds"].(float64); ok {
		seconds = s
	}
	return &delayRunner{duration: time.Duration(seconds * float64(time.Second))}, nil
}

func (r *delayRunner) Run(ctx context.Context, input map[string]any) (map[string]any, error) {
	select {
	case <-time.After(r.duration):
		return input, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("delay interrupted: %w", ctx.Err())
	}
}
