package runner

import (
	"bytes"
	"context"
	"fmt"
	"text/template"
)

type transformRunner struct {
	tmpl      string
	outputKey string
}

func newTransformRunner(cfg map[string]any) (*transformRunner, error) {
	tmpl, _ := cfg["template"].(string)
	if tmpl == "" {
		return nil, fmt.Errorf("transform: missing template")
	}
	key, _ := cfg["outputKey"].(string)
	if key == "" {
		key = "result"
	}
	return &transformRunner{tmpl: tmpl, outputKey: key}, nil
}

func (r *transformRunner) Run(ctx context.Context, input map[string]any) (map[string]any, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	t, err := template.New("transform").Parse(r.tmpl)
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, input); err != nil {
		return nil, fmt.Errorf("execute template: %w", err)
	}

	return map[string]any{r.outputKey: buf.String()}, nil
}
