# Stage 9 — OpenTelemetry + Jaeger: Distributed Tracing

> **Mục tiêu**: Quan sát toàn bộ đường đi của một request từ HTTP webhook → RabbitMQ → gRPC → từng step. Jaeger UI hiển thị trace tree với timing từng node để debug slow executions.

---

## Vấn đề của Stage 8

Khi một execution chạy chậm hoặc fail, ta không biết step nào gây ra. Logs từ 3 terminal khác nhau khó liên kết với nhau. Không có cách xem full request lifecycle.

---

## Giải pháp: Distributed Tracing với OTel + Jaeger

```
[Jaeger UI] http://localhost:16686
       ↑
       │ OTLP gRPC (port 4317)
       │
  ┌────┴────┐              ┌──────────────────┐
  │api-gateway│            │ execution-engine  │
  │           │            │                   │
  │ webhook   │──gRPC─────►│ RunExecution span │
  │  span     │            │   step.delay span │
  │           │            │   step.http span  │
  │ worker    │            └──────────────────┘
  │  span     │
  └─────┬─────┘
        │ AMQP headers
  ┌─────▼─────┐
  │ RabbitMQ  │
  └───────────┘
```

Một trace duy nhất kết nối tất cả các spans từ 2 services.

---

## Khái niệm cốt lõi

| Khái niệm | Giải thích |
|---|---|
| **Trace** | Toàn bộ "hành trình" của một request — từ webhook đến hết các steps |
| **Span** | Một đơn vị công việc trong trace (HTTP handler, gRPC call, một step) |
| **Parent/Child span** | Span con kế thừa trace ID từ span cha — tạo ra cây trace |
| **Context propagation** | Truyền trace ID qua network boundaries (HTTP headers, gRPC metadata, AMQP headers) |
| **W3C TraceContext** | Standard format: `traceparent: 00-<traceId>-<spanId>-01` |
| **OTLP** | OpenTelemetry Protocol — chuẩn export trace data |
| **Jaeger** | Distributed tracing backend với UI để xem traces |

---

## Trace flow đầy đủ

```
POST /webhooks/1
  │
  ├─ Go-Zero tự động tạo span: "GET /webhooks/:workflowId"
  │  (từ Telemetry config trong gateway-api.yaml)
  │
  ├─ publisher.Publish(ctx, msg)
  │   └─ inject traceparent vào AMQP headers:
  │      Headers["traceparent"] = "00-abc123-def456-01"
  │
  └─ return {"executionId": 1}  ← < 50ms

[RabbitMQ queue]
  │ message với traceparent header

Worker goroutine (api-gateway):
  ├─ extract traceparent từ AMQP headers
  ├─ tạo span: "worker.dispatch" (child của webhook span)
  │   attributes: execution.id=1, workflow.id=1
  │
  └─ engineClient.RunExecution(ctx, req)
      └─ otelgrpc tự động inject span vào gRPC metadata

execution-engine gRPC server:
  ├─ otelgrpc tự động extract span từ gRPC metadata
  ├─ tạo span: "execution.run" (child của worker span)
  │   attributes: execution.id=1, workflow.id=1
  │
  ├─ step "delay":
  │   └─ span: "step.delay"  (2001ms)
  │       attributes: step.id="s1", step.type="delay", step.duration_ms=2001
  │
  └─ step "http_request":
      └─ span: "step.http_request"  (145ms)
          attributes: step.id="s2", step.type="http_request"
```

**Jaeger UI** hiển thị timeline:
```
webhook [──────────────────────────── 2200ms ──────────────────]
  worker.dispatch [──────────────────── 2180ms ──────────────]
    execution.run [──────────────── 2160ms ──────────────]
      step.delay  [─────── 2001ms ────────]
      step.http   [── 145ms ──]
```

---

## Context propagation qua RabbitMQ

RabbitMQ không natively support W3C TraceContext. Giải pháp: inject/extract vào AMQP message headers thủ công.

**File**: [api-gateway/internal/queue/publisher.go](../api-gateway/internal/queue/publisher.go)

```go
// AMQPHeaderCarrier adapts amqp.Table sang OTel TextMapCarrier
type AMQPHeaderCarrier amqp.Table

func (c AMQPHeaderCarrier) Get(key string) string { ... }
func (c AMQPHeaderCarrier) Set(key, val string)   { amqp.Table(c)[key] = val }
func (c AMQPHeaderCarrier) Keys() []string        { ... }

// Trong Publish():
headers := amqp.Table{}
otel.GetTextMapPropagator().Inject(ctx, AMQPHeaderCarrier(headers))
// → headers["traceparent"] = "00-abc...-def...-01"
```

**File**: [api-gateway/internal/worker/executionworker.go](../api-gateway/internal/worker/executionworker.go)

```go
// Trong handle():
ctx = otel.GetTextMapPropagator().Extract(ctx, queue.AMQPHeaderCarrier(d.Headers))
ctx, span := otel.Tracer("api-gateway").Start(ctx, "worker.dispatch")
defer span.End()
```

---

## Context propagation qua gRPC

gRPC tự động propagate trace context thông qua `otelgrpc` stats handler. Không cần code thủ công.

**api-gateway** (client side) — [svc/servicecontext.go](../api-gateway/internal/svc/servicecontext.go):

```go
conn, err := grpc.NewClient(c.Engine.Addr,
    grpc.WithTransportCredentials(insecure.NewCredentials()),
    grpc.WithStatsHandler(otelgrpc.NewClientHandler()), // inject span vào gRPC metadata
)
```

**execution-engine** (server side) — [main.go](../execution-engine/main.go):

```go
grpcServer := grpc.NewServer(
    grpc.StatsHandler(otelgrpc.NewServerHandler()), // extract span từ gRPC metadata
)
```

`otelgrpc` là implementation của `grpc.StatsHandler` — hook vào gRPC's stats system để inject/extract trace context tự động.

---

## OTel setup trong execution-engine

**File**: [execution-engine/main.go](../execution-engine/main.go)

```go
func initTracer(ctx context.Context, endpoint string) (func(context.Context) error, error) {
    if endpoint == "" {
        return noop, nil  // disable tracing gracefully if Jaeger not configured
    }

    exp, _ := otlptracegrpc.New(ctx,
        otlptracegrpc.WithInsecure(),   // plaintext (dev only)
        otlptracegrpc.WithEndpoint(endpoint), // "localhost:4317" or "jaeger:4317"
    )

    res, _ := resource.New(ctx,
        resource.WithAttributes(attribute.String("service.name", "execution-engine")),
    )

    tp := sdktrace.NewTracerProvider(
        sdktrace.WithBatcher(exp),          // async export
        sdktrace.WithResource(res),
        sdktrace.WithSampler(sdktrace.AlwaysSample()), // 100% sampling (dev)
    )

    otel.SetTracerProvider(tp)
    otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
        propagation.TraceContext{},  // W3C TraceContext
        propagation.Baggage{},
    ))

    return tp.Shutdown, nil  // flush spans on graceful shutdown
}
```

**api-gateway** dùng Go-Zero's built-in OTel — chỉ cần thêm vào YAML:

```yaml
Telemetry:
  Name: api-gateway
  Endpoint: localhost:4317  # host:port, no http://
  Sampler: 1.0
  Batcher: otlpgrpc
```

---

## Spans trong execution-engine

**File**: [execution-engine/internal/server/server.go](../execution-engine/internal/server/server.go)

```go
var tracer = otel.Tracer("execution-engine")

func (s *Server) RunExecution(ctx context.Context, req *pb.RunExecutionReq) (*pb.RunExecutionResp, error) {
    ctx, span := tracer.Start(ctx, "execution.run",
        trace.WithAttributes(
            attribute.Int64("execution.id", req.ExecutionId),
            attribute.Int64("workflow.id", req.WorkflowId),
        ))
    defer span.End()
    // ...
    span.SetAttributes(attribute.String("execution.status", status))
    if status == "failed" {
        span.SetStatus(codes.Error, errMsg)
    }
}

// Trong runSteps(), mỗi step tạo span riêng:
for _, stepDef := range steps {
    stepCtx, stepSpan := tracer.Start(stepCtx, "step."+stepDef.Type,
        trace.WithAttributes(
            attribute.String("step.id", stepDef.ID),
            attribute.String("step.type", stepDef.Type),
        ))
    // ... run step ...
    stepSpan.SetAttributes(attribute.Int64("step.duration_ms", durationMs))
    if failed { stepSpan.SetStatus(codes.Error, stepErrMsg) }
    stepSpan.End()
}
```

---

## Chạy và xem trace

### Local

```bash
# Start Jaeger
docker-compose up -d jaeger

# Start services (3 terminal)
make engine   # terminal 1
make run      # terminal 2

# Trigger
TOKEN=$(curl -s -X POST http://localhost:8080/realms/workflow-app/protocol/openid-connect/token \
  -d "grant_type=password&client_id=workflow-client&username=alice&password=alice123" \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['access_token'])")

curl -X POST http://localhost:8888/webhooks/1 \
  -H "Content-Type: application/json" \
  -d '{"data":"test"}'

# Mở Jaeger UI
open http://localhost:16686
# → Service: api-gateway → Find Traces → chọn trace
```

### Full Docker

```bash
make up
make logs
# Sau khi up: http://localhost:16686
```

---

## Jaeger UI — đọc trace

1. Vào `http://localhost:16686`
2. Chọn **Service**: `api-gateway`
3. Click **Find Traces**
4. Click vào một trace để xem chi tiết

```
Trace: POST /webhooks/1           2.3s
├── worker.dispatch               2.28s
│   └── execution.run             2.26s
│       ├── step.delay            2001ms  ← dễ thấy bottleneck
│       └── step.http_request     145ms
```

Màu đỏ = span có error. Hover = xem attributes.

---

## Thay đổi các files

| File | Thay đổi |
|---|---|
| [docker-compose.yml](../docker-compose.yml) | Thêm `jaeger` service (ports 16686, 4317, 4318) |
| [api-gateway/etc/gateway-api.yaml](../api-gateway/etc/gateway-api.yaml) | Thêm `Telemetry` section |
| [api-gateway/etc/gateway-api.docker.yaml](../api-gateway/etc/gateway-api.docker.yaml) | Thêm `Telemetry` với `jaeger:4317` |
| [api-gateway/internal/queue/publisher.go](../api-gateway/internal/queue/publisher.go) | Thêm `AMQPHeaderCarrier`, inject trace trong `Publish()` |
| [api-gateway/internal/worker/executionworker.go](../api-gateway/internal/worker/executionworker.go) | Extract trace, tạo `worker.dispatch` span |
| [api-gateway/internal/svc/servicecontext.go](../api-gateway/internal/svc/servicecontext.go) | Thêm `otelgrpc.NewClientHandler()` vào gRPC client |
| [execution-engine/internal/config/config.go](../execution-engine/internal/config/config.go) | Thêm `JaegerEndpoint` field |
| [execution-engine/etc/engine.yaml](../execution-engine/etc/engine.yaml) | Thêm `JaegerEndpoint: "localhost:4317"` |
| [execution-engine/etc/engine.docker.yaml](../execution-engine/etc/engine.docker.yaml) | Thêm `JaegerEndpoint: "jaeger:4317"` |
| [execution-engine/main.go](../execution-engine/main.go) | `initTracer()` + `otelgrpc.NewServerHandler()` |
| [execution-engine/internal/server/server.go](../execution-engine/internal/server/server.go) | Spans cho `execution.run` và `step.*` |

---

## Tóm tắt kiến thức đã học

| Khái niệm | Áp dụng |
|---|---|
| `otel.Tracer("name")` | Tạo tracer cho một service/component |
| `tracer.Start(ctx, "span.name")` | Bắt đầu span, trả về ctx mới chứa span |
| `span.SetAttributes(...)` | Tag span với metadata (execution.id, step.type,...) |
| `span.SetStatus(codes.Error, msg)` | Mark span là lỗi — hiện màu đỏ trong Jaeger |
| `span.RecordError(err)` | Ghi error event vào span timeline |
| `span.End()` | Kết thúc span — flush timing |
| `propagation.TraceContext{}` | W3C standard — encode trace ID vào `traceparent` header |
| `otel.GetTextMapPropagator().Inject/Extract` | Propagate trace qua carrier tùy chỉnh (AMQP) |
| `otelgrpc.NewClientHandler()` | Tự động inject trace vào gRPC metadata (client) |
| `otelgrpc.NewServerHandler()` | Tự động extract trace từ gRPC metadata (server) |
| `sdktrace.WithBatcher(exp)` | Async export — batch spans trước khi gửi Jaeger |
| `sdktrace.AlwaysSample()` | 100% sampling — dùng cho dev, giảm xuống 0.1 cho prod |
| `tp.Shutdown(ctx)` | Flush tất cả pending spans trước khi process exit |

---

## Cái gì chưa làm (Stage 10+)

- Stage 10: AI step type — `ai_task` với Anthropic SDK
- GORM query tracing với `otelgorm` plugin
- RabbitMQ message tracing với `otelrabbit`
- Sampling strategy — giảm xuống 1-10% cho production load
- Trace-based alerting — alert khi p99 latency > threshold
