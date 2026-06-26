# Stage 7 — gRPC: Tách Execution Engine thành RPC Service

> **Mục tiêu**: Tách logic chạy workflow ra thành service riêng (`execution-engine`). API gateway worker không còn chạy step runners trực tiếp — thay vào đó gọi gRPC để ủy quyền toàn bộ execution sang microservice độc lập.

---

## Vấn đề của Stage 6

Ở Stage 6, worker trong api-gateway vẫn chạy `engine.RunWorkflow(...)` trực tiếp. Logic chạy steps (http_request, delay, transform...) nằm trong cùng binary với HTTP server:

```
api-gateway binary:
  ├── HTTP handlers
  ├── RabbitMQ worker
  └── engine.RunWorkflow (step runners)   ← tất cả trong 1 binary
```

Nếu execution engine cần scale riêng (thêm CPU/memory), ta phải scale toàn bộ api-gateway.

---

## Giải pháp: gRPC microservice

```
api-gateway binary:                    execution-engine binary:
  ├── HTTP handlers                      ├── gRPC server (:9090)
  ├── RabbitMQ worker                    ├── step runners
  │   └── engineClient.RunExecution() ──→│   └── RunWorkflow logic
  └── (không còn step runners)           └── DB + Redis access
```

Worker chỉ dispatch job, execution-engine xử lý toàn bộ step logic.

---

## Protobuf Schema

**File**: [execution-engine/pb/engine.proto](../execution-engine/pb/engine.proto)

```proto
syntax = "proto3";
package engine;
option go_package = "execution-engine/pb";

service ExecutionEngine {
  // RunExecution: blocking call, trả về khi workflow finish
  rpc RunExecution(RunExecutionReq) returns (RunExecutionResp);

  // StreamExecution: server streaming — push event về caller sau mỗi step
  rpc StreamExecution(RunExecutionReq) returns (stream ExecutionEvent);
}

message RunExecutionReq {
  int64  execution_id    = 1;
  int64  workflow_id     = 2;
  string trigger_payload = 3;
}

message RunExecutionResp {
  string status = 1; // "success" | "failed"
  string error  = 2;
}

message ExecutionEvent {
  string event_type  = 1; // "step_done" | "finished"
  string step_id     = 2;
  string status      = 3;
  int64  duration_ms = 4;
  string error       = 5;
}
```

**Regenerate code:**
```bash
make gen-rpc
```

---

## Cấu trúc execution-engine

```
execution-engine/
├── engine.proto               ← source of truth
├── pb/
│   ├── engine.proto           ← proto file
│   ├── engine.pb.go           ← generated (message types)
│   └── engine_grpc.pb.go      ← generated (server/client stubs)
├── etc/engine.yaml
├── main.go
└── internal/
    ├── config/config.go
    ├── model/models.go        ← GORM models (duped từ api-gateway, không import chéo)
    ├── runner/                ← step runners (chuyển từ api-gateway/internal/engine)
    │   ├── runner.go          ← StepDef, StepRunner interface, factory
    │   ├── http_runner.go
    │   ├── transform_runner.go
    │   ├── delay_runner.go
    │   ├── condition_runner.go
    │   └── email_runner.go
    └── server/
        └── server.go          ← gRPC server implementation
```

**Tại sao duplicate models thay vì import chéo?**

`execution-engine` và `api-gateway` là 2 module riêng. Nếu engine import api-gateway và api-gateway import engine → **circular dependency**. Cách chuẩn trong microservices: mỗi service tự định nghĩa schema của mình từ cùng một "source of truth" (proto hoặc spec).

---

## gRPC Flow

```
RabbitMQ Worker (api-gateway)
        │
        │ consume ExecutionMessage
        │
        ▼
engineClient.RunExecution(ctx, req)
        │
        │ gRPC call (TCP :9090)
        ▼
execution-engine Server
        │
        ├── db.First(&workflow, req.WorkflowId)
        ├── db.First(&execution, req.ExecutionId)
        ├── Update execution status: pending → running
        │
        ├── For each step:
        │   ├── runner.NewStepRunner(stepDef)
        │   ├── runner.Run(ctx, input)
        │   ├── db.Create(&StepLog)
        │   └── redis.Publish("executions:N", event)
        │
        ├── Update execution status: running → success/failed
        └── Return RunExecutionResp{Status: "success"}
        │
        ▼
Worker nhận resp → d.Ack(false)
```

---

## RPC vs Streaming

Có 2 RPC methods trong proto:

### `RunExecution` — Unary (blocking)

```go
resp, err := engineClient.RunExecution(ctx, &pb.RunExecutionReq{...})
// Blocks until all steps finish
// Worker sử dụng cái này
```

**Ưu điểm**: Đơn giản, ack/nack rõ ràng — nếu engine trả về error thì worker nack → retry.

### `StreamExecution` — Server Streaming

```go
stream, err := engineClient.StreamExecution(ctx, req)
for {
    event, err := stream.Recv() // nhận từng event
    if err == io.EOF { break }
    // process event (step_done, finished)
}
```

**Ưu điểm**: Real-time updates tới caller — có thể relay sang SSE endpoint mà không cần Redis pub/sub. Phức tạp hơn nhưng giảm latency cho real-time UI.

Ở Stage 7, worker dùng `RunExecution` (đơn giản). `StreamExecution` implement sẵn trong server để dùng sau.

---

## Thay đổi api-gateway

### Worker mới

**File**: [api-gateway/internal/worker/executionworker.go](../api-gateway/internal/worker/executionworker.go)

```go
// Trước (Stage 6): worker trực tiếp chạy engine
engine.RunWorkflow(ctx, w.db, w.redis, &execution, &wf)

// Sau (Stage 7): worker gọi gRPC
resp, err := w.engineClient.RunExecution(ctx, &pb.RunExecutionReq{
    ExecutionId:    int64(msg.ExecutionID),
    WorkflowId:     int64(msg.WorkflowID),
    TriggerPayload: msg.TriggerPayload,
})
```

Worker giờ nhẹ hơn — không cần biết gì về step runners.

### ServiceContext

**File**: [api-gateway/internal/svc/servicecontext.go](../api-gateway/internal/svc/servicecontext.go)

```go
conn, err := grpc.NewClient(c.Engine.Addr,
    grpc.WithTransportCredentials(insecure.NewCredentials()))

EngineClient: pb.NewExecutionEngineClient(conn)
```

`insecure.NewCredentials()` — dùng plaintext TCP (không TLS). Trong production dùng mTLS.

### Proto files trong api-gateway

**File**: [api-gateway/internal/enginepb/](../api-gateway/internal/enginepb/)

API gateway có copy riêng của generated code (package `pb`). Cùng proto file, 2 lần chạy protoc với output paths khác nhau:
- `execution-engine/pb/` — server side
- `api-gateway/internal/enginepb/` — client side

---

## Config

**File**: [execution-engine/etc/engine.yaml](../execution-engine/etc/engine.yaml)

```yaml
Port: 9090
DSN: "host=localhost user=workflow password=workflow dbname=workflow_db port=5432 sslmode=disable"
RedisAddr: "localhost:6379"
```

**File**: [api-gateway/etc/gateway-api.yaml](../api-gateway/etc/gateway-api.yaml)

```yaml
Engine:
  Addr: "localhost:9090"
```

---

## Bước 8 — Chạy toàn bộ hệ thống

### Start infrastructure

```bash
make db-up    # PostgreSQL + Redis
make mq-up    # RabbitMQ
make etcd-up  # etcd (sẵn sàng cho Stage 8+ service discovery)
```

### Start services (3 terminal khác nhau)

```bash
# Terminal 1: execution-engine
make engine
# [execution-engine] listening on :9090

# Terminal 2: api-gateway
make run
# Starting server at 0.0.0.0:8888...

# Terminal 3 (optional): scheduler
make scheduler
```

### Test async flow

```bash
# Lấy token
TOKEN=$(curl -s -X POST http://localhost:8080/realms/workflow-app/protocol/openid-connect/token \
  -d "grant_type=password&client_id=workflow-client&username=alice&password=alice123" \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['access_token'])")

# Tạo workflow với delay step
curl -X POST http://localhost:8888/workflows \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"gRPC Test","triggerType":"webhook","steps":"[{\"id\":\"s1\",\"type\":\"delay\",\"config\":{\"seconds\":2}}]"}'

# Trigger → trả về ngay < 50ms
time curl -X POST http://localhost:8888/webhooks/1 \
  -H "Content-Type: application/json" \
  -d '{"data":"test"}'
# {"message":"execution queued","executionId":1}
# real  0m0.045s  ← immediate

# Execution chạy trong execution-engine (delay 2s)
# Poll sau 3s
sleep 3 && curl -H "Authorization: Bearer $TOKEN" http://localhost:8888/executions/1
# {"status":"success", ...}
```

### Quan sát log execution-engine

```
[execution-engine] listening on :9090
RunExecution executionId=1 workflowId=1
  step s1 (delay) → success 2001ms
execution 1 → success
```

---

## etcd — Service Discovery (Preview)

etcd đã được thêm vào docker-compose (port 2379). Hiện tại execution-engine dùng địa chỉ tĩnh (`localhost:9090`). 

Trong một hệ thống production, execution-engine sẽ **register** vào etcd khi khởi động:
```
/services/execution-engine/instance-1 → "192.168.1.10:9090"
/services/execution-engine/instance-2 → "192.168.1.11:9090"
```

API gateway **discover** bằng cách watch key prefix. Go-Zero zrpc tích hợp sẵn etcd discovery. Stage 8+ sẽ explore pattern này.

---

## Tóm tắt kiến thức đã học

| Khái niệm | Áp dụng |
|---|---|
| Protobuf schema | `engine.proto` — typed contract giữa services |
| `protoc` codegen | Generate message types + client/server stubs |
| gRPC unary RPC | `RunExecution` — request/response như HTTP POST |
| gRPC server streaming | `StreamExecution` — server push nhiều messages |
| `grpc.NewClient` | Tạo connection pool tới gRPC server |
| `insecure.NewCredentials()` | Plaintext transport (dev only) |
| `pb.UnimplementedExecutionEngineServer` | Embed để forward-compatible khi thêm RPC methods |
| Cross-module proto copy | Mỗi service có generated code riêng — tránh circular import |
| etcd | Distributed key-value store cho service registry |

---

## Cái gì chưa làm (Stage 8+)

- TLS cho gRPC connection (production)
- etcd service discovery — dynamic resolution thay vì static address
- Stage 8: Docker containerization — Dockerfile cho cả 3 services, `docker-compose up` chạy toàn bộ
