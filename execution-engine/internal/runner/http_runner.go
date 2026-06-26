package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type httpRequestRunner struct {
	url    string
	method string
	body   string
}

func newHTTPRequestRunner(cfg map[string]any) (*httpRequestRunner, error) {
	url, _ := cfg["url"].(string)
	if url == "" {
		return nil, fmt.Errorf("http_request: missing url")
	}
	method, _ := cfg["method"].(string)
	if method == "" {
		method = "GET"
	}
	body, _ := cfg["body"].(string)
	return &httpRequestRunner{url: url, method: method, body: body}, nil
}

func (r *httpRequestRunner) Run(ctx context.Context, input map[string]any) (map[string]any, error) {
	var bodyReader io.Reader
	if r.body != "" {
		bodyReader = strings.NewReader(r.body)
	}

	req, err := http.NewRequestWithContext(ctx, r.method, r.url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if r.body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http %s %s: %w", r.method, r.url, err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)

	var parsed any
	if json.Unmarshal(raw, &parsed) != nil {
		parsed = string(raw)
	}

	return map[string]any{
		"status": resp.StatusCode,
		"body":   parsed,
	}, nil
}
