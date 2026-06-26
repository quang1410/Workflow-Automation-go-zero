# Stage 10 — AI Step Type: Anthropic + OpenAI

> **Mục tiêu**: Thêm bước `ai_task` vào workflow engine. Khi workflow chạy đến bước này, nó sẽ gọi Claude (Anthropic) hoặc GPT (OpenAI), stream từng token về Redis pub/sub, và client xem real-time qua SSE đã có từ Stage 4.

---

## Vấn đề của Stage 9

Sau 9 stage, hệ thống đã có đủ hạ tầng (async queue, gRPC, distributed tracing), nhưng tất cả các step chỉ là fixed logic (HTTP call, transform, delay…). Chưa có khả năng **suy luận** hay **tổng hợp** dữ liệu bằng AI.

---

## Giải pháp: `ai_task` step type

```
Workflow JSON:
{
  "steps": [
    { "id": "step1", "type": "http_request", "config": { "url": "..." } },
    {
      "id": "step2",
      "type": "ai_task",
      "config": {
        "provider": "anthropic",
        "model": "claude-sonnet-4-6",
        "prompt": "Summarize this: {{.result}}",
        "maxTokens": 500
      }
    },
    { "id": "step3", "type": "send_email", "config": { ... } }
  ]
}
```

Khi execution engine gặp bước `ai_task`:
1. Render prompt template với output của step trước (`input` map)
2. Gọi Anthropic hoặc OpenAI **streaming API**
3. Publish từng token lên Redis channel `executions:{workflowId}`
4. Client đang watch SSE endpoint sẽ nhận `ai_token` events real-time

---

## Kiến trúc mới

```
execution-engine server.go
  └── runSteps()
        └── factory.New(stepDef, channel)   ← NEW: factory thay vì NewStepRunner()
              └── ai_task → AITaskRunner
                    ├── renderPrompt(template, input)
                    ├── Anthropic/OpenAI streaming API
                    │     └── publishToken() → Redis "executions:{workflowId}"
                    └── return {"output": "<full response>"}
```

---

## Khái niệm học được

### RunnerFactory pattern

Trước đây, `NewStepRunner(step)` là hàm package-level không nhận external dependencies. Nhưng `ai_task` cần API keys và Redis client. Giải pháp: tạo `RunnerFactory` struct để **inject dependencies** vào runner:

```go
type RunnerFactory struct {
    AnthropicAPIKey string
    OpenAIAPIKey    string
    Rdb             *goredis.Client
}

func (f *RunnerFactory) New(step StepDef, streamChan string) (StepRunner, error) {
    switch step.Type {
    case "ai_task":
        return newAITaskRunner(step.Config, f.AnthropicAPIKey, f.OpenAIAPIKey, f.Rdb, streamChan, step.ID)
    // ...
    }
}
```

Factory được tạo một lần trong `main.go` và inject vào `Server`, tránh global state.

### Anthropic SDK Streaming

```go
stream := client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
    Model:     anthropic.Model("claude-sonnet-4-6"),
    MaxTokens: 500,
    Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(prompt))},
})

for stream.Next() {
    event := stream.Current()                          // MessageStreamEventUnion
    delta := event.AsContentBlockDelta()               // ContentBlockDeltaEvent
    if delta.Delta.Type == "text_delta" {
        token := delta.Delta.Text                      // incremental text chunk
        rdb.Publish(ctx, channel, tokenEvent)
    }
}
err := stream.Err()   // kiểm tra lỗi sau khi stream kết thúc
```

Mỗi lần `stream.Next()` trả về một SSE event từ API. `AsContentBlockDelta()` cast về đúng loại event để lấy text delta.

### OpenAI SDK Streaming

```go
stream := client.Chat.Completions.NewStreaming(ctx, oai.ChatCompletionNewParams{
    Model:               oai.ChatModel("gpt-4o"),
    MaxCompletionTokens: param.NewOpt(int64(500)),
    Messages: []oai.ChatCompletionMessageParamUnion{
        oai.UserMessage(prompt),
    },
})

for stream.Next() {
    chunk := stream.Current()                          // ChatCompletionChunk
    if len(chunk.Choices) > 0 {
        token := chunk.Choices[0].Delta.Content        // text delta
    }
}
```

OpenAI trả về `ChatCompletionChunk` thay vì `Message`. Mỗi chunk có `Choices[0].Delta.Content` là phần text mới nhất.

### Template rendering cho prompt

Prompt hỗ trợ Go `text/template` syntax. Output của step trước được pass vào template dưới dạng `map[string]any`:

```
"Tóm tắt kết quả: {{.result}}"     ← .result là key trong output của step trước
"URL: {{.url}}, Status: {{.status}}" ← nhiều fields
```

---

## Token streaming flow

```
execution-engine (runAnthropic):
  for each token chunk:
    publishToken(ctx, chunk, false)
          ↓
    rdb.Publish("executions:1", {
      "type": "ai_token",
      "stepId": "step2",
      "token": "Đây ",
      "finished": false
    })

api-gateway SSE handler (GET /executions/1/stream):
  subscribed to "executions:1"
  receives ai_token events → forwards to client as SSE:
    data: {"type":"ai_token","stepId":"step2","token":"Đây ","finished":false}

  khi stream kết thúc:
    data: {"type":"ai_token","stepId":"step2","token":"","finished":true}
  sau đó:
    data: {"type":"step_done","stepId":"step2","status":"success","durationMs":1234}
```

---

## Files thay đổi

| File | Thay đổi |
|------|---------|
| `execution-engine/internal/runner/ai_runner.go` | **Mới** — AITaskRunner với Anthropic + OpenAI streaming |
| `execution-engine/internal/runner/runner.go` | Thêm `RunnerFactory`, đăng ký `ai_task` |
| `execution-engine/internal/config/config.go` | Thêm `AnthropicAPIKey`, `OpenAIAPIKey` |
| `execution-engine/internal/server/server.go` | Server nhận `RunnerFactory`, dùng factory trong `runSteps` |
| `execution-engine/main.go` | Tạo factory từ config + env vars, inject vào Server |
| `execution-engine/etc/engine.yaml` | Thêm API key placeholders |
| `execution-engine/etc/engine.docker.yaml` | Thêm API key placeholders |
| `docker-compose.yml` | Inject `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` từ host env |
| `execution-engine/go.mod` | Thêm `anthropic-sdk-go`, `openai-go` |

---

## Setup & Test

### 1. Cài đặt API key

```bash
# Với local run:
# Sửa execution-engine/etc/engine.yaml:
AnthropicAPIKey: "sk-ant-api03-..."

# Hoặc dùng env var:
export ANTHROPIC_API_KEY="sk-ant-api03-..."
```

### 2. Chạy local

```bash
# Terminal 1: infrastructure
docker-compose up postgres redis rabbitmq jaeger -d

# Terminal 2: execution-engine
export ANTHROPIC_API_KEY="sk-ant-..."
cd execution-engine && go run main.go -f etc/engine.yaml

# Terminal 3: api-gateway
cd api-gateway && go run gateway.go -f etc/gateway-api.yaml
```

### 3. Tạo workflow có `ai_task`

```bash
# Đăng nhập
TOKEN=$(curl -s -X POST http://localhost:8888/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"alice","password":"alice123"}' | jq -r .accessToken)

# Tạo workflow: HTTP fetch → AI summarize → done
curl -X POST http://localhost:8888/workflows \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "AI Summarizer",
    "triggerType": "webhook",
    "steps": [
      {
        "id": "step1",
        "type": "transform",
        "config": {
          "template": "Go là ngôn ngữ lập trình mạnh mẽ cho backend với goroutine và channel.",
          "outputKey": "text"
        }
      },
      {
        "id": "step2",
        "type": "ai_task",
        "config": {
          "provider": "anthropic",
          "model": "claude-haiku-4-5-20251001",
          "prompt": "Tóm tắt trong 1 câu: {{.text}}",
          "maxTokens": 200
        }
      }
    ]
  }'
# → {"id": 1, ...}
```

### 4. Trigger và xem streaming

```bash
# Terminal A: Subscribe SSE stream TRƯỚC khi trigger
curl -N http://localhost:8888/executions/stream/1 \
  -H "Authorization: Bearer $TOKEN"

# Terminal B: Trigger webhook
curl -X POST http://localhost:8888/webhooks/1 \
  -H "Content-Type: application/json" \
  -d '{"source":"test"}'
```

**Kết quả terminal A (SSE events):**
```
data: {"type":"step_done","stepId":"step1","status":"success","durationMs":0}

data: {"type":"ai_token","stepId":"step2","token":"Go ","finished":false}
data: {"type":"ai_token","stepId":"step2","token":"là một ngôn ngữ","finished":false}
data: {"type":"ai_token","stepId":"step2","token":" lập trình hiệu năng cao.","finished":false}
data: {"type":"ai_token","stepId":"step2","token":"","finished":true}

data: {"type":"step_done","stepId":"step2","status":"success","durationMs":876}
data: {"type":"finished","executionId":1,"status":"success"}
```

### 5. Xem kết quả execution

```bash
curl http://localhost:8888/executions/1 \
  -H "Authorization: Bearer $TOKEN"
# step_logs[1].output = {"output": "Go là một ngôn ngữ lập trình hiệu năng cao."}
```

---

## Dùng OpenAI thay thế

```json
{
  "id": "step2",
  "type": "ai_task",
  "config": {
    "provider": "openai",
    "model": "gpt-4o-mini",
    "prompt": "Summarize in one sentence: {{.text}}",
    "maxTokens": 200
  }
}
```

```bash
export OPENAI_API_KEY="sk-..."
```

---

## Docker Compose

```bash
ANTHROPIC_API_KEY=sk-ant-... docker-compose up --build
```

Biến môi trường `ANTHROPIC_API_KEY` từ host được inject vào container `execution-engine` tự động qua `${ANTHROPIC_API_KEY:-}` trong docker-compose.yml.

---

## Verification checklist

- [ ] `POST /webhooks/{id}` với workflow có `ai_task` → execution tạo ra
- [ ] SSE stream nhận `ai_token` events real-time trong khi AI đang generate
- [ ] `GET /executions/{id}` → `step_logs` có `output.output` chứa full AI response
- [ ] Template `{{.fieldName}}` render đúng từ output step trước
- [ ] Khi `ANTHROPIC_API_KEY` trống → step failed với error rõ ràng, không crash server
- [ ] Jaeger UI hiển thị span `step.ai_task` với duration chính xác
