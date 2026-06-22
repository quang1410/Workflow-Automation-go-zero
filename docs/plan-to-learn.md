# Plan: Workflow Automation Engine — Go + Go-Zero Learning Project

## Context

Xây dựng một hệ thống workflow automation đơn giản tương tự n8n/Activepieces để học Go và các công nghệ liên quan. User tạo workflow gồm trigger + các steps, hệ thống tự động chạy khi trigger được kích hoạt. Project chia làm 10 giai đoạn tăng dần độ khó. Workspace hiện trống hoàn toàn.

---

## Tổng quan hệ thống

### Concept cốt lõi

```
Workflow = Trigger + [Step1 → Step2 → Step3 ...]

Trigger types:
  - webhook  : POST /webhooks/{workflowId} → chạy workflow
  - schedule : Cron expression (mỗi 5 phút, mỗi ngày 8h,...)
  - manual   : Gọi API để chạy thủ công

Step types (built-in):
  - http_request : Gọi HTTP API ngoài
  - transform    : Biến đổi data (template expression)
  - delay        : Chờ N giây
  - condition    : If/else rẽ nhánh
  - send_email   : Giả lập gửi email (log)
  - ai_task      : Gọi Claude/OpenAI
```

### Workflow definition (JSON lưu trong DB)

```json
{
  "id": "wf_abc123",
  "name": "Notify on new user",
  "trigger": { "type": "webhook" },
  "steps": [
    {
      "id": "step1",
      "type": "http_request",
      "config": { "url": "https://api.example.com/notify", "method": "POST" }
    },
    {
      "id": "step2",
      "type": "send_email",
      "config": { "to": "admin@example.com", "subject": "New user: {{step1.body.name}}" }
    }
  ]
}
```

### Kiến trúc

```
Client
  |
API Gateway (Go-Zero HTTP)
  |
  +─── Workflow API (CRUD workflows)
  |
  +─── Webhook Trigger (/webhooks/{id})
  |
  +─── Execution API (xem log)
  |
Execution Engine (Go-Zero RPC)
  |
  +─── Step Runner (goroutine pool)
  |
PostgreSQL (workflows, executions, step_logs)
Redis     (cache workflow definitions, pub/sub trigger)
RabbitMQ  (async execution queue)
Scheduler (cron trigger service)
Jaeger    (trace từng execution)
```

---

## Cấu trúc thư mục

```
go-workflow/
├── api-gateway/               # Go-Zero HTTP API
│   ├── gateway.api
│   ├── etc/gateway.yaml
│   └── internal/
│       ├── handler/
│       ├── logic/
│       ├── middleware/        # Auth middleware
│       └── svc/
├── execution-engine/          # Go-Zero RPC — chạy workflow steps
│   ├── engine.proto
│   └── internal/
│       ├── runner/            # Step runners
│       └── logic/
├── scheduler-service/         # Cron trigger service
│   └── main.go
├── playground/                # Bài tập Go cơ bản
├── docker-compose.yml
├── Makefile
└── go.work
```

---

## Giai đoạn 1: Go cơ bản + Go-Zero API (Tuần 1)

### Mục tiêu học
- Struct, Interface, Pointer, Error handling
- Goroutine, Channel, Context
- Go-Zero HTTP service cơ bản

### Việc cần làm

1. **Khởi tạo project**
   ```bash
   mkdir go-workflow && cd go-workflow
   go work init
   goctl api new api-gateway
   go work use api-gateway
   ```

2. **Viết `gateway.api`** — 3 endpoints không cần DB:
   ```
   GET  /health             → {"status":"ok","version":"1.0.0"}
   GET  /workflows          → [] (hardcoded empty)
   POST /workflows/trigger  → {"message":"triggered"}
   ```

3. **Generate + implement handlers** với hardcoded data

4. **Playground**: Bài tập Go cơ bản
   - Goroutine fan-out: Chạy 5 "step" song song, collect results bằng channel
   - Context cancellation: Timeout một "step" nếu chạy quá 3 giây
   - Custom error type: `StepError{StepID, Reason}`
   - Interface: `StepRunner` interface với `Run(ctx, input) (output, error)`

### Files cần tạo
- `api-gateway/gateway.api`
- `api-gateway/internal/handler/*.go`
- `api-gateway/internal/logic/*.go`
- `playground/step_runner_demo.go`

---

## Giai đoạn 2: PostgreSQL + GORM (Tuần 2)

### Mục tiêu học
- GORM CRUD, AutoMigrate, JSONB columns
- Transaction
- Index, Foreign Key

### GORM Models

```go
type Workflow struct {
    gorm.Model
    Name        string          `gorm:"not null"`
    UserID      uint
    TriggerType string          `gorm:"not null"` // webhook, schedule, manual
    TriggerConfig datatypes.JSON
    Steps       datatypes.JSON  // []StepDef lưu dạng JSONB
    IsActive    bool            `gorm:"default:true"`
}

type Execution struct {
    gorm.Model
    WorkflowID uint
    Workflow   Workflow
    Status     string          `gorm:"default:running"` // running, success, failed
    StartedAt  time.Time
    FinishedAt *time.Time
    TriggerPayload datatypes.JSON
}

type StepLog struct {
    gorm.Model
    ExecutionID uint
    StepID      string
    StepType    string
    Status      string          // success, failed, skipped
    Input       datatypes.JSON
    Output      datatypes.JSON
    Error       string
    DurationMs  int64
}
```

### Việc cần làm

1. Thêm PostgreSQL vào `docker-compose.yml`
2. Init GORM trong `ServiceContext` với `AutoMigrate`
3. Implement Workflow CRUD APIs:
   - `POST /workflows` → tạo workflow
   - `GET /workflows` → list (filter by user)
   - `GET /workflows/:id` → chi tiết
   - `PUT /workflows/:id` → update
   - `DELETE /workflows/:id` → soft delete
4. Implement Execution APIs:
   - `GET /workflows/:id/executions` → list executions
   - `GET /executions/:id` → chi tiết + step logs
5. **Transaction**: Khi tạo execution, insert Execution + StepLogs trong 1 transaction

### Files cần tạo/sửa
- `docker-compose.yml`
- `api-gateway/model/*.go`
- `api-gateway/internal/svc/servicecontext.go`
- `api-gateway/internal/logic/*.go`

---

## Giai đoạn 3: Execution Engine — Chạy Workflow (Tuần 3)

### Mục tiêu học
- Interface pattern cho step runners
- Goroutine pool (worker pool pattern)
- Context propagation + timeout per step

### Step Runner Interface

```go
type StepRunner interface {
    Run(ctx context.Context, input map[string]any) (map[string]any, error)
}

// Implement cho từng loại step:
type HTTPRequestRunner struct{}
type TransformRunner struct{}
type DelayRunner struct{}
type ConditionRunner struct{}
type SendEmailRunner struct{}
```

### Execution flow

```
Trigger nhận request
      ↓
Tạo Execution record (status: running)
      ↓
Lấy Workflow definition
      ↓
Duyệt steps theo thứ tự:
  For each step:
    ctx, cancel = context.WithTimeout(ctx, stepTimeout)
    output, err = runner.Run(ctx, previousStepOutput)
    Insert StepLog (status, duration, output/error)
    Nếu error + không có fallback → dừng, mark Execution failed
      ↓
Mark Execution success/failed
```

### Việc cần làm

1. Viết `StepRunner` interface + factory `NewStepRunner(stepType)`
2. Implement `HTTPRequestRunner` (dùng `net/http`)
3. Implement `TransformRunner` (template với `text/template`)
4. Implement `DelayRunner` (`time.Sleep` với context cancel)
5. Implement `ConditionRunner` (evaluate expression → chọn nhánh)
6. Implement `SendEmailRunner` (chỉ log, không gửi thật)
7. Wire vào `POST /webhooks/:workflowId` endpoint

### Files cần tạo
- `api-gateway/internal/engine/runner.go` (interface + factory)
- `api-gateway/internal/engine/http_runner.go`
- `api-gateway/internal/engine/transform_runner.go`
- `api-gateway/internal/engine/delay_runner.go`
- `api-gateway/internal/engine/condition_runner.go`
- `api-gateway/internal/engine/email_runner.go`

---

## Giai đoạn 4: Redis Cache + Pub/Sub (3-4 ngày)

### Mục tiêu học
- Cache-aside pattern
- Redis Pub/Sub cho real-time events
- TTL management

### Cache

```
GET /workflows/:id
      ↓
Check Redis "workflow:{id}"
   Hit  → Unmarshal + Return
   Miss → Query PostgreSQL → Marshal → Setex(key, json, 300s) → Return

Khi update/delete workflow → Del "workflow:{id}"
```

### Pub/Sub — Real-time execution updates

```
Execution bắt đầu → Publish "executions:{workflowId}" với event
                       {"type":"started","executionId":1}
Mỗi step xong    → Publish "executions:{workflowId}" với step result
Execution kết thúc → Publish "executions:{workflowId}" với final status
```

Client poll `GET /executions/:id/stream` (Server-Sent Events) subscribe Redis channel.

### Việc cần làm

1. Thêm Redis vào `docker-compose.yml`
2. Cache workflow definitions
3. Invalidate cache khi update workflow
4. Implement SSE endpoint `/executions/:id/stream` → subscribe Redis channel
5. Publisher trong execution engine sau mỗi step

### Files cần tạo/sửa
- `api-gateway/internal/logic/getworkflowlogic.go` (cache)
- `api-gateway/internal/handler/executionstreamhandler.go` (SSE)
- `api-gateway/internal/engine/runner.go` (publish events)

---

## Giai đoạn 5: Keycloak + JWT Auth (Tuần 5)

### Mục tiêu học
- OAuth2 + OpenID Connect
- JWT validation với JWKS
- Role-based: `admin` có thể xem tất cả workflow, `user` chỉ xem của mình

### Setup

```yaml
keycloak:
  image: quay.io/keycloak/keycloak:24.0
  environment:
    KEYCLOAK_ADMIN: admin
    KEYCLOAK_ADMIN_PASSWORD: admin
  ports: ["8080:8080"]
  command: start-dev
```

Realm: `workflow-app`, Roles: `admin`, `user`

### Auth flow

```
POST /auth/login (username, password)
      ↓ proxy tới Keycloak
{ access_token, refresh_token }
      ↓
Header: Authorization: Bearer <token>
      ↓
JWT Middleware: validate token bằng Keycloak JWKS endpoint
      ↓
Extract userId + roles từ claims
      ↓
Inject vào context → logic layer dùng để filter data
```

### Việc cần làm

1. Thêm Keycloak vào `docker-compose.yml`
2. `authmiddleware.go`: validate JWT, fetch JWKS từ Keycloak
3. `rolemiddleware.go`: check role từ claims
4. Update tất cả workflow/execution endpoints → require auth
5. Logic filter: `user` chỉ thấy workflow của mình (WHERE user_id = ?)
6. `POST /auth/login` proxy tới Keycloak

### Files cần tạo/sửa
- `api-gateway/internal/middleware/authmiddleware.go`
- `api-gateway/internal/middleware/rolemiddleware.go`
- `api-gateway/internal/handler/authhandler.go`

---

## Giai đoạn 6: RabbitMQ — Async Execution Queue (Tuần 6)

### Mục tiêu học
- Producer/Consumer pattern
- Topic exchange, routing keys
- Dead Letter Queue + retry

### Tại sao cần RabbitMQ?

Trước đó, webhook trigger chạy execution **đồng bộ** trong HTTP request → timeout nếu workflow chạy lâu.
Sau khi thêm RabbitMQ: webhook chỉ enqueue message, trả về ngay. Execution chạy **bất đồng bộ**.

### Flow

```
POST /webhooks/{workflowId}
      ↓ (nhanh)
Tạo Execution record (status: pending)
Publish to RabbitMQ:
  exchange: "workflows" (topic)
  routing key: "execution.run"
  payload: { executionId, workflowId, triggerPayload }
      ↓
Return { executionId, status: "pending" }

Worker (goroutine pool) consume từ queue
      ↓
Chạy execution engine
      ↓
Update Execution status
      ↓
Publish kết quả lên Redis pub/sub
```

### Schedule trigger flow

```
scheduler-service (cron)
      ↓ mỗi phút, lấy workflows có triggerType=schedule
      ↓ Check cron expression match
      ↓ Publish to RabbitMQ "execution.run"
```

### Việc cần làm

1. Thêm RabbitMQ vào `docker-compose.yml`
2. Tách execution logic ra khỏi HTTP handler → worker pool
3. Webhook handler chỉ publish message, không chạy trực tiếp
4. Worker: Consume + chạy execution engine
5. Dead Letter Queue cho failed executions (retry 3 lần)
6. Tạo `scheduler-service/` với cron trigger

### Files cần tạo
- `api-gateway/internal/queue/publisher.go`
- `api-gateway/internal/worker/executionworker.go` (consumer + goroutine pool)
- `scheduler-service/main.go`
- `scheduler-service/cron/scheduler.go`

---

## Giai đoạn 7: gRPC — Tách Execution Engine thành RPC Service (Tuần 7)

### Mục tiêu học
- Protobuf schema
- gRPC unary + server streaming
- Go-Zero RPC + etcd service discovery

### Tách execution engine ra microservice riêng

```bash
goctl rpc new execution-engine
```

### engine.proto

```proto
syntax = "proto3";
package engine;

service ExecutionEngine {
  rpc RunExecution(RunExecutionReq) returns (RunExecutionResp);
  rpc GetExecutionStatus(GetStatusReq) returns (stream ExecutionEvent);
}

message RunExecutionReq {
  int64 execution_id  = 1;
  int64 workflow_id   = 2;
  bytes trigger_payload = 3;
}
message RunExecutionResp { string status = 1; }
message ExecutionEvent {
  string event_type = 1; // step_started, step_done, execution_done
  int64  step_index = 2;
  bytes  data       = 3;
}
```

### Flow sau khi tách

```
RabbitMQ Worker (trong api-gateway)
      ↓ consume message
      ↓ gọi ExecutionEngine RPC
execution-engine service chạy steps
      ↓ server streaming: push events về worker
Worker nhận events → publish tới Redis pub/sub
```

### Việc cần làm

1. Generate execution-engine với goctl
2. Di chuyển step runners vào execution-engine
3. Implement `RunExecution` RPC + server streaming `GetExecutionStatus`
4. Update api-gateway worker để gọi RPC thay vì trực tiếp
5. Thêm etcd vào docker-compose
6. Update `go.work`

### Files cần tạo
- `execution-engine/engine.proto` + generated code
- `execution-engine/internal/runner/*.go`
- `execution-engine/internal/logic/runexecutionlogic.go`

---

## Giai đoạn 8: Docker + Containerization (3 ngày)

### Mục tiêu học
- Multi-stage Dockerfile
- docker-compose health checks
- Env var management

### Dockerfile pattern

```dockerfile
FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o service .

FROM alpine:3.19
WORKDIR /app
COPY --from=builder /app/service .
COPY etc/ etc/
EXPOSE 8888
HEALTHCHECK --interval=10s CMD wget -qO- http://localhost:8888/health || exit 1
CMD ["./service"]
```

### docker-compose.yml services

`postgres`, `redis`, `rabbitmq`, `keycloak`, `etcd`, `jaeger`, `api-gateway`, `execution-engine`, `scheduler-service`

### Việc cần làm

1. Dockerfile cho `api-gateway`, `execution-engine`, `scheduler-service`
2. Health checks + depends_on đúng thứ tự
3. Finalize `docker-compose.yml`
4. Makefile với `make up`, `make down`, `make logs`, `make gen-api`, `make gen-rpc`

---

## Giai đoạn 9: OpenTelemetry + Jaeger — Distributed Tracing (3-4 ngày)

### Mục tiêu học
- Trace + Span
- Context propagation qua gRPC
- Jaeger UI để debug slow executions

### Tracing trong workflow engine

```
POST /webhooks/{workflowId}  → root span "webhook.trigger"
      ↓ (W3C trace context propagated qua RabbitMQ headers)
execution-engine RunExecution → child span "execution.run"
      ↓
  Step 1 HTTPRequest       → child span "step.http_request" (với URL, duration)
  Step 2 Transform         → child span "step.transform"
  Step 3 SendEmail         → child span "step.send_email"
      ↓
Jaeger UI: xem toàn bộ trace, tìm step nào chậm
```

### Setup

```yaml
jaeger:
  image: jaegertracing/all-in-one:1.56
  ports:
    - "16686:16686"
    - "4317:4317"
```

Go-Zero config:

```yaml
Telemetry:
  Name: api-gateway
  Endpoint: http://jaeger:4317
  Sampler: 1.0
  Batcher: otlpgrpc
```

### Việc cần làm

1. Thêm Jaeger vào `docker-compose.yml`
2. Thêm Telemetry config vào tất cả service yaml
3. Instrument từng step runner với span:
   ```go
   ctx, span := otel.Tracer("engine").Start(ctx, "step.http_request")
   defer span.End()
   span.SetAttributes(attribute.String("url", config.URL))
   ```
4. Propagate trace context qua RabbitMQ message headers
5. Instrument GORM queries với `otelgorm`

---

## Giai đoạn 10: AI Step Type — Anthropic + OpenAI (3-4 ngày)

### Mục tiêu học
- Anthropic SDK + OpenAI SDK cho Go
- Streaming responses
- AI như một step trong workflow

### Built-in step type mới: `ai_task`

```json
{
  "id": "step3",
  "type": "ai_task",
  "config": {
    "provider": "anthropic",
    "model": "claude-sonnet-4-6",
    "prompt": "Summarize this data: {{step2.output}}",
    "maxTokens": 500
  }
}
```

### Ví dụ workflow dùng AI

```
Trigger: webhook (nhận data từ form)
      ↓
Step 1: http_request (GET thêm context từ API ngoài)
      ↓
Step 2: ai_task (Claude summarize data)
      ↓
Step 3: send_email (gửi summary)
```

### AI Runner

```go
type AITaskRunner struct {
    anthropicClient *anthropic.Client
    openaiClient    *openai.Client
}

func (r *AITaskRunner) Run(ctx context.Context, input map[string]any) (map[string]any, error) {
    prompt := renderTemplate(config.Prompt, input)
    msg, _ := r.anthropicClient.Messages.New(ctx, anthropic.MessageNewParams{
        Model:     anthropic.ModelClaudeSonnet4_6,
        MaxTokens: int64(config.MaxTokens),
        Messages:  []anthropic.MessageParam{...},
    })
    return map[string]any{"output": msg.Content[0].Text}, nil
}
```

### Streaming execution logs

Khi step `ai_task` chạy streaming → push từng token lên Redis pub/sub → client xem real-time qua SSE.

### Việc cần làm

1. Thêm `anthropic-sdk-go` và `openai-go` dependencies
2. Implement `AITaskRunner` với provider switch (anthropic/openai)
3. Register `ai_task` trong step runner factory
4. Streaming: forward tokens tới Redis pub/sub
5. Test: tạo workflow summarize text với Claude

### Files cần tạo
- `execution-engine/internal/runner/ai_runner.go`
- Config: `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` trong docker-compose env

---

## Tools & Dependencies

| Tool/Lib | Giai đoạn | Mục đích |
|----------|-----------|----------|
| `go-zero` | 1 | Framework HTTP + RPC |
| `goctl` | 1 | Code generator |
| `gorm` + `datatypes` | 2 | PostgreSQL ORM + JSONB |
| `go-zero redis` | 4 | Cache + Pub/Sub |
| `golang-jwt/jwt/v5` | 5 | JWT validation |
| `amqp091-go` | 6 | RabbitMQ |
| `robfig/cron/v3` | 6 | Cron scheduler |
| `protoc` + `protoc-gen-go` | 7 | gRPC codegen |
| `go.opentelemetry.io/otel` | 9 | Distributed tracing |
| `anthropic-sdk-go` | 10 | Claude AI |
| `openai-go` | 10 | OpenAI |

---

## Verification

| Giai đoạn | Kiểm tra |
|-----------|----------|
| 1 | `curl /health` → 200; goroutine demo chạy không deadlock |
| 2 | CRUD workflow/execution hoạt động, JSONB lưu đúng |
| 3 | POST webhook → steps chạy tuần tự → step_logs có đủ input/output |
| 4 | Request thứ 2 lấy cache; SSE stream nhận events real-time |
| 5 | `/workflows` không token → 401; user A không thấy workflow của user B |
| 6 | Webhook trả về ngay (< 50ms); execution chạy async; schedule trigger hoạt động |
| 7 | execution-engine chạy như RPC service riêng; etcd discovery hoạt động |
| 8 | `docker-compose up` chạy toàn bộ hệ thống |
| 9 | Jaeger UI hiển thị trace từ webhook → từng step; slow step được highlight |
| 10 | Workflow có `ai_task` → Claude trả về text; streaming tokens qua SSE |

---

## Makefile

```makefile
.PHONY: up down logs gen-api gen-rpc

up:
	docker-compose up -d

down:
	docker-compose down -v

gen-api:
	cd api-gateway && goctl api go -api gateway.api -dir .

gen-engine-rpc:
	cd execution-engine && goctl rpc protoc engine.proto --go_out=. --go-grpc_out=. --zrpc_out=.

test-webhook:
	curl -X POST http://localhost:8888/webhooks/1 -H "Content-Type: application/json" -d '{"user":"test"}'
```

---

## Thứ tự implement mỗi session

1. Viết `.api` / `.proto` definition
2. `goctl` generate code
3. Implement logic + step runners
4. Test với curl
5. Commit
