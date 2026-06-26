package engine

import (
	"context"
	"fmt"
)

type sendEmailRunner struct {
	to      string
	subject string
	body    string
}

func newSendEmailRunner(cfg map[string]any) (*sendEmailRunner, error) {
	to, _ := cfg["to"].(string)
	subject, _ := cfg["subject"].(string)
	body, _ := cfg["body"].(string)
	if to == "" {
		return nil, fmt.Errorf("send_email: missing to")
	}
	return &sendEmailRunner{to: to, subject: subject, body: body}, nil
}

func (r *sendEmailRunner) Run(ctx context.Context, input map[string]any) (map[string]any, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Stage 3: log only — real SMTP in a later stage.
	fmt.Printf("[EMAIL] to=%s subject=%q body=%q\n", r.to, r.subject, r.body)

	return map[string]any{
		"sent": true,
		"to":   r.to,
	}, nil
}
