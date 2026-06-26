package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	anthropicopt "github.com/anthropics/anthropic-sdk-go/option"
	oai "github.com/openai/openai-go"
	oaiopt "github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	goredis "github.com/redis/go-redis/v9"
)

type aiTaskRunner struct {
	provider     string
	model        string
	promptTmpl   string
	maxTokens    int64
	anthropicKey string
	openaiKey    string
	rdb          *goredis.Client
	streamChan   string
	stepID       string
}

type aiTokenEvent struct {
	Type     string `json:"type"`
	StepID   string `json:"stepId"`
	Token    string `json:"token"`
	Finished bool   `json:"finished"`
}

func newAITaskRunner(cfg map[string]any, anthropicKey, openaiKey string, rdb *goredis.Client, streamChan, stepID string) (*aiTaskRunner, error) {
	provider, _ := cfg["provider"].(string)
	if provider == "" {
		provider = "anthropic"
	}
	model, _ := cfg["model"].(string)
	if model == "" {
		if provider == "openai" {
			model = "gpt-4o"
		} else {
			model = "claude-sonnet-4-6"
		}
	}
	prompt, _ := cfg["prompt"].(string)
	if prompt == "" {
		return nil, fmt.Errorf("ai_task: missing prompt")
	}
	var maxTokens int64 = 500
	if v, ok := cfg["maxTokens"]; ok {
		switch n := v.(type) {
		case float64:
			maxTokens = int64(n)
		case int:
			maxTokens = int64(n)
		case int64:
			maxTokens = n
		}
	}
	return &aiTaskRunner{
		provider:     provider,
		model:        model,
		promptTmpl:   prompt,
		maxTokens:    maxTokens,
		anthropicKey: anthropicKey,
		openaiKey:    openaiKey,
		rdb:          rdb,
		streamChan:   streamChan,
		stepID:       stepID,
	}, nil
}

func (r *aiTaskRunner) Run(ctx context.Context, input map[string]any) (map[string]any, error) {
	prompt, err := renderPrompt(r.promptTmpl, input)
	if err != nil {
		return nil, fmt.Errorf("ai_task: render prompt: %w", err)
	}

	switch r.provider {
	case "openai":
		return r.runOpenAI(ctx, prompt)
	default:
		return r.runAnthropic(ctx, prompt)
	}
}

func (r *aiTaskRunner) runAnthropic(ctx context.Context, prompt string) (map[string]any, error) {
	client := anthropic.NewClient(anthropicopt.WithAPIKey(r.anthropicKey))

	stream := client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(r.model),
		MaxTokens: r.maxTokens,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})

	var sb strings.Builder
	for stream.Next() {
		event := stream.Current()
		delta := event.AsContentBlockDelta()
		if delta.Delta.Type == "text_delta" && delta.Delta.Text != "" {
			sb.WriteString(delta.Delta.Text)
			r.publishToken(ctx, delta.Delta.Text, false)
		}
	}
	if err := stream.Err(); err != nil {
		return nil, fmt.Errorf("anthropic stream: %w", err)
	}

	r.publishToken(ctx, "", true)
	return map[string]any{"output": sb.String()}, nil
}

func (r *aiTaskRunner) runOpenAI(ctx context.Context, prompt string) (map[string]any, error) {
	client := oai.NewClient(oaiopt.WithAPIKey(r.openaiKey))

	stream := client.Chat.Completions.NewStreaming(ctx, oai.ChatCompletionNewParams{
		Model:               oai.ChatModel(r.model),
		MaxCompletionTokens: param.NewOpt(r.maxTokens),
		Messages: []oai.ChatCompletionMessageParamUnion{
			oai.UserMessage(prompt),
		},
	})

	var sb strings.Builder
	for stream.Next() {
		chunk := stream.Current()
		if len(chunk.Choices) > 0 {
			token := chunk.Choices[0].Delta.Content
			if token != "" {
				sb.WriteString(token)
				r.publishToken(ctx, token, false)
			}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, fmt.Errorf("openai stream: %w", err)
	}

	r.publishToken(ctx, "", true)
	return map[string]any{"output": sb.String()}, nil
}

// publishToken sends a token event to the Redis pub/sub channel for real-time SSE streaming.
// finished=true signals the end of the AI response.
func (r *aiTaskRunner) publishToken(ctx context.Context, token string, finished bool) {
	if r.rdb == nil || r.streamChan == "" {
		return
	}
	data, _ := json.Marshal(aiTokenEvent{
		Type:     "ai_token",
		StepID:   r.stepID,
		Token:    token,
		Finished: finished,
	})
	r.rdb.Publish(ctx, r.streamChan, string(data))
}

func renderPrompt(tmpl string, input map[string]any) (string, error) {
	t, err := template.New("prompt").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, input); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	return buf.String(), nil
}
