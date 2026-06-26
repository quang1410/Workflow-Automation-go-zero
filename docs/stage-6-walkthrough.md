# Stage 6 — RabbitMQ: Async Execution Queue

> **Mục tiêu**: Webhook trả về ngay (< 50ms). Execution chạy bất đồng bộ qua RabbitMQ worker pool. `scheduler-service` trigger schedule workflows bằng cron expression.

---

## Vấn đề của Stage 5

Trước đây, `POST /webhooks/:id` chạy workflow **đồng bộ** trong HTTP request:

```
Client → POST /webhooks/1
                ↓ (có thể mất 10-30s nếu workflow phức tạp)
        RunWorkflow(steps...)
                ↓
        Response: {"executionId": 5}
```

Nếu workflow có `delay` step hay `http_request` đến API chậm → HTTP timeout, client không nhận được response.

---

## Giải pháp: Producer/Consumer với RabbitMQ

```
Client → POST /webhooks/1
                ↓ (< 50ms)
        Tạo Execution(status=pending)
        Publish to RabbitMQ
                ↓
        Response: {"executionId": 5, "message": "execution queued"}

                (bất đồng bộ)
Worker 1..5  ← Consume từ "execution.run"
                ↓
        RunWorkflow(steps...)
        Update Execution(status=success/failed)
        Publish to Redis pub/sub
```

---

## Topology RabbitMQ

```
workflows (topic exchange)
    │
    ├── execution.run (queue, durable)
    │   └── x-dead-letter-exchange: workflows.dlx
    │
workflows.dlx (direct exchange)
    │
    └── execution.retry (queue, durable)
        ├── x-message-ttl: 10000 (10s delay)
        └── x-dead-letter-exchange: workflows (→ back to execution.run)
```

**Retry flow:**
1. Worker xử lý thất bại (DB error) → `nack(requeue=false)` → vào DLX
2. DLX → `execution.retry` queue (giữ 10 giây)
3. Sau 10s TTL → `workflows` exchange → `execution.run` queue (retry)
4. Sau `MaxRetries=3` lần → worker `ack` (drop hẳn)

**`x-death` header:** RabbitMQ tự động thêm header này mỗi khi message bị dead-lettered, ghi lại số lần (`count`) và lý do.

---

## Bước 1 — RabbitMQ trong Docker Compose

**File**: [docker-compose.yml](../docker-compose.yml)

```yaml
rabbitmq:
  image: rabbitmq:3.13-management-alpine
  environment:
    RABBITMQ_DEFAULT_USER: workflow
    RABBITMQ_DEFAULT_PASS: workflow
  ports:
    - "5672:5672"    # AMQP protocol
    - "15672:15672"  # Management UI
  healthcheck:
    test: ["CMD", "rabbitmq-diagnostics", "-q", "ping"]
    interval: 10s
    start_period: 30s
```

Management UI: http://localhost:15672 (workflow/workflow)

---

## Bước 2 — Config

**File**: [api-gateway/internal/config/config.go](../api-gateway/internal/config/config.go)

```go
type RabbitMQConfig struct {
    URL         string // amqp://user:pass@host:5672/
    WorkerCount int    // number of consumer goroutines
}
```

**File**: [api-gateway/etc/gateway-api.yaml](../api-gateway/etc/gateway-api.yaml)

```yaml
RabbitMQ:
  URL: "amqp://workflow:workflow@localhost:5672/"
  WorkerCount: 5
```

---

## Bước 3 — Publisher

**File**: [api-gateway/internal/queue/publisher.go](../api-gateway/internal/queue/publisher.go)

```go
type ExecutionMessage struct {
    ExecutionID    uint   `json:"executionId"`
    WorkflowID     uint   `json:"workflowId"`
    TriggerPayload string `json:"triggerPayload"`
}

func (p *Publisher) Publish(ctx context.Context, msg ExecutionMessage) error {
    body, _ := json.Marshal(msg)
    return p.ch.PublishWithContext(ctx, Exchange, RoutingKeyRun, false, false,
        amqp.Publishing{
            ContentType:  "application/json",
            DeliveryMode: amqp.Persistent, // survive RabbitMQ restart
            Body:         body,
        })
}
```

`amqp.Persistent` lưu message xuống disk — nếu RabbitMQ restart, message không mất.

---

## Bước 4 — Worker Pool

**File**: [api-gateway/internal/worker/executionworker.go](../api-gateway/internal/worker/executionworker.go)

```go
func (w *Worker) Start(ctx context.Context) error {
    conn, _ := amqp.Dial(w.amqpURL)
    
    for i := range w.count {
        ch, _ := conn.Channel()
        ch.Qos(1, 0, false) // prefetch 1: worker không nhận job mới cho đến khi ack job hiện tại
        go w.consume(ctx, ch, i)
    }
    
    go func() { <-ctx.Done(); conn.Close() }()
    return nil
}
```

**`Qos(1, 0, false)` — prefetch = 1:**

Không có prefetch: RabbitMQ gửi tất cả pending messages cho worker ngay lập tức → một worker bị block, các message kia bị delay.

Với prefetch=1: mỗi worker chỉ giữ 1 message tại một thời điểm → fair dispatch.

### Handle logic

```go
func (w *Worker) handle(ctx context.Context, d amqp.Delivery, id int) {
    if deathCount(d.Headers) >= MaxRetries {
        d.Ack(false) // đã retry đủ lần → drop
        return
    }
    
    var msg ExecutionMessage
    json.Unmarshal(d.Body, &msg)
    
    // Load workflow + execution từ DB
    // ...
    
    w.db.Model(&execution).Update("status", "running")
    engine.RunWorkflow(ctx, w.db, w.redis, &execution, &wf)
    
    d.Ack(false) // engine tự handle success/fail trong DB
}
```

**Khi nào nack vs ack:**

| Tình huống | Action | Lý do |
|---|---|---|
| `deathCount >= 3` | `Ack` (drop) | Đã retry đủ, không lặp vô hạn |
| Message JSON sai | `Ack` (drop) | Malformed, retry không giúp ích |
| Workflow bị xóa | `Ack` (drop) | Workflow không còn tồn tại |
| DB connection error | `Nack(false, false)` | Transient error → DLX → retry sau 10s |
| `RunWorkflow` lỗi | `Ack` | Engine đã ghi trạng thái vào DB |

---

## Bước 5 — Async Trigger

**File**: [api-gateway/internal/logic/executionlogic.go](../api-gateway/internal/logic/executionlogic.go)

```go
// Trước (Stage 5): đồng bộ
execution := model.Execution{Status: "running", ...}
db.Create(&execution)
engine.RunWorkflow(ctx, db, redis, &execution, &wf)  // block!

// Sau (Stage 6): bất đồng bộ
execution := model.Execution{Status: "pending", ...}
db.Create(&execution)
queue.Publish(ctx, ExecutionMessage{ExecutionID: execution.ID, ...})  // returns immediately
return &TriggerResp{Message: "execution queued", ExecutionId: ...}
```

**Execution status lifecycle:**
```
pending → (worker picks up) → running → success
                                      → failed
```

---

## Bước 6 — Worker khởi động trong gateway.go

**File**: [api-gateway/gateway.go](../api-gateway/gateway.go)

```go
w := worker.New(c.RabbitMQ.URL, ctx.DB, ctx.Redis, c.RabbitMQ.WorkerCount)
workerCtx, cancelWorkers := context.WithCancel(context.Background())
defer cancelWorkers()
w.Start(workerCtx)
```

`context.WithCancel` cho phép graceful shutdown — khi `server.Stop()` được gọi, `cancelWorkers()` signal workers dừng consume và đóng channels.

---

## Bước 7 — Scheduler Service

**Files**: [scheduler-service/](../scheduler-service/)

Service riêng biệt (module `scheduler-service`) trigger schedule workflows.

### Cron check pattern

```go
func (s *Scheduler) checkAndTrigger() {
    // Load tất cả active schedule workflows từ DB
    var workflows []Workflow
    db.Where("trigger_type = ? AND is_active = ?", "schedule", true).Find(&workflows)
    
    now := time.Now().Truncate(time.Minute)
    minuteAgo := now.Add(-time.Minute)
    
    for _, wf := range workflows {
        var cfg TriggerConfig  // {"cron": "*/5 * * * *"}
        json.Unmarshal(wf.TriggerConfig, &cfg)
        
        sched, _ := cron.ParseStandard(cfg.Cron)
        next := sched.Next(minuteAgo)
        
        if next.Before(now.Add(time.Second)) {
            // Cron expression fires trong phút này → trigger
            s.trigger(wf)
        }
    }
}
```

**Logic:** "Next scheduled time sau 1 phút trước < bây giờ + 1s buffer" → cron expression này nên chạy trong phút hiện tại.

### Schedule workflow ví dụ

Khi tạo workflow với `triggerType=schedule`, đặt `triggerConfig`:

```json
{"cron": "*/5 * * * *"}
```

Cron chạy mỗi 5 phút. Scheduler check mỗi phút, phát hiện và publish execution message.

---

## Bước 8 — Test

### Start infrastructure

```bash
make db-up    # PostgreSQL + Redis
make mq-up    # RabbitMQ (Management UI: http://localhost:15672)
```

### Lấy JWT token

```bash
make keycloak-up  # nếu chưa chạy
make keycloak-token
TOKEN=$(curl -s -X POST http://localhost:8080/realms/workflow-app/protocol/openid-connect/token \
  -d "grant_type=password&client_id=workflow-client&username=alice&password=alice123" \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['access_token'])")
```

### Trigger async

```bash
# Tạo workflow
curl -X POST http://localhost:8888/workflows \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"Async Test","triggerType":"webhook","steps":"[{\"id\":\"s1\",\"type\":\"delay\",\"config\":{\"seconds\":3}}]"}'

# Trigger → phải trả về ngay (< 50ms) dù step có delay 3s
time curl -X POST http://localhost:8888/webhooks/1 \
  -H "Content-Type: application/json" \
  -d '{"data":"test"}'
# Response ngay: {"message":"execution queued","executionId":1}
# real  0m0.045s  ← không đợi execution xong

# Poll status sau vài giây
curl -H "Authorization: Bearer $TOKEN" http://localhost:8888/executions/1
# status: "running" → "success"
```

### Kiểm tra retry

Tắt PostgreSQL trong khi worker đang xử lý:

```bash
docker-compose stop postgres
# Trigger một execution
curl -X POST http://localhost:8888/webhooks/1 -d '{}'
# Worker log: "db error loading workflow: ... — retrying"
# Message vào execution.retry (TTL 10s) → quay lại execution.run
docker-compose start postgres
# Worker xử lý thành công sau khi DB back up
```

### Kiểm tra Management UI

http://localhost:15672 → Queues → `execution.run`:
- **Ready**: số message chờ
- **Unacked**: message đang được worker xử lý
- **Total**: tổng

### Test scheduler

```bash
# Tạo workflow loại schedule
curl -X POST http://localhost:8888/workflows \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"Every Minute","triggerType":"schedule","triggerConfig":"{\"cron\":\"* * * * *\"}"}'

# Chạy scheduler service
make scheduler
# [scheduler] started — checking every minute
# [scheduler] triggered workflow 2 → executionId 3
```

---

## Tóm tắt kiến thức đã học

| Khái niệm | Áp dụng |
|---|---|
| Producer/Consumer | Webhook publish → Worker consume |
| Topic exchange | `workflows` exchange, routing key `execution.run` |
| Dead Letter Exchange (DLX) | `nack` → `workflows.dlx` → retry queue |
| `x-message-ttl` | 10s delay trước khi retry |
| `x-death` header | Đếm số lần retry, drop khi >= MaxRetries |
| `Qos(prefetch=1)` | Fair dispatch — worker không nhận job mới cho đến khi ack |
| `amqp.Persistent` | Message survive broker restart |
| Goroutine pool | N workers × 1 channel each |
| Cron parsing | `robfig/cron` parse expression, tính `Next()` time |
| Separate module | `scheduler-service` — binary riêng trong go.work |

---

## Cái gì chưa làm (Stage 7+)

- RabbitMQ connection reconnect (hiện tại nếu connection drop → service phải restart)
- Execution timeout (workflow chạy quá lâu → force fail)
- Stage 7: tách `execution-engine` thành RPC service riêng (gRPC)
