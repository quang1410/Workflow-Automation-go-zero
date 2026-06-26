# Stage 4 — Redis Cache + Pub/Sub: Walkthrough từng bước

> **Mục tiêu**: Thêm Redis để cache workflow definitions (giảm DB round-trips) và stream real-time execution events về client qua Server-Sent Events.

---

## Tổng quan những gì thay đổi

```
docker-compose.yml            ← thêm redis service
api-gateway/
  internal/config/config.go  ← thêm RedisConf
  etc/gateway-api.yaml        ← thêm Redis.Addr
  internal/svc/servicecontext.go  ← thêm *goredis.Client
  internal/logic/workflowlogic.go ← cache-aside trong GetWorkflow
                                    invalidate trong Update/Delete
  internal/engine/executor.go     ← publishEvent sau mỗi step + finish
  internal/logic/executionlogic.go ← truyền Redis vào RunWorkflow
  gateway.api                 ← thêm StreamExecutionReq + route
  internal/handler/executionstreamhandler.go ← SSE handler
```

---

## Bước 1 — Thêm Redis vào Docker Compose

**File**: [docker-compose.yml](../docker-compose.yml)

```yaml
redis:
  image: redis:7-alpine
  ports:
    - "6379:6379"
  healthcheck:
    test: ["CMD", "redis-cli", "ping"]
    interval: 5s
    timeout: 3s
    retries: 5
```

Và trong Makefile, `db-up` bây giờ chờ cả hai:
```makefile
db-up:
	docker-compose up -d --wait postgres redis
```

`--wait` block cho đến khi cả `pg_isready` lẫn `redis-cli ping` đều pass.

---

## Bước 2 — Config và ServiceContext

### config.go

**File**: [api-gateway/internal/config/config.go](../api-gateway/internal/config/config.go)

```go
type Config struct {
    rest.RestConf
    DB    DBConfig
    Redis RedisConf   // mới
}

type RedisConf struct {
    Addr string
}
```

### gateway-api.yaml

```yaml
Redis:
  Addr: "localhost:6379"
```

### servicecontext.go

**File**: [api-gateway/internal/svc/servicecontext.go](../api-gateway/internal/svc/servicecontext.go)

```go
import goredis "github.com/redis/go-redis/v9"

type ServiceContext struct {
    Config config.Config
    DB     *gorm.DB
    Redis  *goredis.Client   // mới
}

func NewServiceContext(c config.Config) *ServiceContext {
    // ... existing DB setup ...
    rdb := goredis.NewClient(&goredis.Options{Addr: c.Redis.Addr})
    return &ServiceContext{Config: c, DB: db, Redis: rdb}
}
```

Dùng `github.com/redis/go-redis/v9` trực tiếp thay vì go-zero's redis wrapper vì go-zero không expose `Subscribe` (chỉ có `Publish`). go-redis/v9 đã là transitive dependency của go-zero, nên `go get` chỉ đơn giản là promote nó thành direct dep.

---

## Bước 3 — Cache-aside trong GetWorkflow

**File**: [api-gateway/internal/logic/workflowlogic.go](../api-gateway/internal/logic/workflowlogic.go)

```go
func (l *GetWorkflowLogic) GetWorkflow(req *types.GetWorkflowReq) (*types.WorkflowItem, error) {
    key := fmt.Sprintf("workflow:%d", req.Id)

    // Cache hit
    if cached, err := l.svcCtx.Redis.Get(l.ctx, key).Result(); err == nil {
        var item types.WorkflowItem
        if json.Unmarshal([]byte(cached), &item) == nil {
            return &item, nil
        }
    }

    // Cache miss — query DB
    var wf model.Workflow
    if err := l.svcCtx.DB.First(&wf, req.Id).Error; err != nil { ... }

    item := toWorkflowItem(wf)

    // Populate cache — TTL 5 phút
    if data, err := json.Marshal(item); err == nil {
        l.svcCtx.Redis.SetEx(l.ctx, key, string(data), 5*time.Minute)
    }

    return &item, nil
}
```

### Cache-aside pattern

```
GET /workflows/:id
       ↓
  Redis.Get("workflow:1")
    ├── Hit  → unmarshal + return  (không đụng DB)
    └── Miss → DB.First(&wf, 1)
                └── Redis.SetEx("workflow:1", json, 5min)
                └── return
```

**Tại sao không cache `ListWorkflows`?**  
List có thể thay đổi liên tục (add/delete). Cache list cần invalidate toàn bộ mỗi khi có thay đổi — quá phức tạp và ít lợi ích. Chỉ cache individual item theo ID.

### Cache invalidation

```go
// UpdateWorkflow
l.svcCtx.Redis.Del(l.ctx, fmt.Sprintf("workflow:%d", req.Id))

// DeleteWorkflow
l.svcCtx.Redis.Del(l.ctx, fmt.Sprintf("workflow:%d", req.Id))
```

Sau khi update hoặc delete workflow, key cũ bị xóa ngay. Request tiếp theo sẽ hit DB và populate cache mới. Pattern này gọi là **write-invalidate** (khác với write-through — ghi vào cache ngay khi update).

**Tại sao write-invalidate thay vì write-through?**  
Đơn giản hơn và tránh race condition: nếu update DB thành công nhưng set cache thất bại → cache stale. Invalidate đảm bảo cache miss sẽ luôn đọc từ DB.

---

## Bước 4 — Publish events trong ExecutionEngine

**File**: [api-gateway/internal/engine/executor.go](../api-gateway/internal/engine/executor.go)

```go
type ExecutionEvent struct {
    Type        string `json:"type"` // "step_done" | "finished"
    ExecutionID uint   `json:"executionId"`
    StepID      string `json:"stepId,omitempty"`
    Status      string `json:"status"`
    DurationMs  int64  `json:"durationMs,omitempty"`
}
```

Channel name: `executions:{workflowId}` — tất cả clients theo dõi execution của workflow `X` subscribe cùng channel.

```go
func RunWorkflow(ctx context.Context, db *gorm.DB, rdb *goredis.Client, execution *model.Execution, wf *model.Workflow) {
    channel := fmt.Sprintf("executions:%d", wf.ID)

    for _, stepDef := range steps {
        // ... chạy step ...

        publishEvent(rdb, channel, ExecutionEvent{
            Type:        "step_done",
            ExecutionID: execution.ID,
            StepID:      stepDef.ID,
            Status:      status,
            DurationMs:  durationMs,
        })

        if status == "failed" {
            finishExecution(db, rdb, channel, execution, "failed")
            return
        }
        // ...
    }
    finishExecution(db, rdb, channel, execution, "success")
}

func publishEvent(rdb *goredis.Client, channel string, event ExecutionEvent) {
    data, _ := json.Marshal(event)
    rdb.Publish(context.Background(), channel, string(data))  // fire-and-forget
}
```

`publishEvent` dùng `context.Background()` thay vì `ctx` của request vì:  
- `ctx` của request có thể đã bị cancel khi client disconnect  
- Event publishing phải hoàn thành dù HTTP connection đã đóng

---

## Bước 5 — SSE Endpoint

### gateway.api

```
type StreamExecutionReq {
    Id int64 `path:"id"`
}

@handler ExecutionStreamHandler
get /executions/:id/stream (StreamExecutionReq) returns ()
```

Sau `goctl api go -api gateway.api -dir .`:
- `types/types.go` được update với `StreamExecutionReq`
- `handler/routes.go` được update với route mới
- `handler/executionstreamhandler.go` skeleton được tạo (1 lần)
- `logic/executionstreamlogic.go` skeleton được tạo (rồi bị xóa — SSE không cần logic layer)

### Tại sao SSE không dùng Logic layer?

Go-zero's Logic pattern: `Logic.Method()` nhận request struct, trả về `(resp, error)`. Handler sau đó gọi `httpx.OkJsonCtx(w, resp)`.

SSE cần stream nhiều events theo thời gian qua `http.ResponseWriter`. Không thể encode điều đó vào `return (resp, error)`. Handler tự handle toàn bộ SSE — đây là exception hợp lý trong go-zero.

### SSE Handler

**File**: [api-gateway/internal/handler/executionstreamhandler.go](../api-gateway/internal/handler/executionstreamhandler.go)

```go
func ExecutionStreamHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        var req types.StreamExecutionReq
        httpx.Parse(r, &req)

        // Load execution để lấy workflowId cho channel name
        var ex model.Execution
        svcCtx.DB.First(&ex, req.Id)

        // Nếu đã xong — flush status ngay và đóng
        if ex.Status == "success" || ex.Status == "failed" {
            w.Header().Set("Content-Type", "text/event-stream")
            fmt.Fprintf(w, "data: {\"type\":\"finished\",\"status\":\"%s\"}\n\n", ex.Status)
            flusher.Flush()
            return
        }

        // Subscribe Redis channel
        channel := fmt.Sprintf("executions:%d", ex.WorkflowID)
        pubsub := svcCtx.Redis.Subscribe(r.Context(), channel)
        defer pubsub.Close()

        w.Header().Set("Content-Type", "text/event-stream")
        w.Header().Set("Cache-Control", "no-cache")
        w.Header().Set("Connection", "keep-alive")

        for {
            select {
            case msg := <-pubsub.Channel():
                fmt.Fprintf(w, "data: %s\n\n", msg.Payload)
                flusher.Flush()
                if isFinished(msg.Payload) {
                    return   // đóng connection sau "finished"
                }
            case <-r.Context().Done():
                return   // client disconnect
            }
        }
    }
}
```

### SSE format

Server-Sent Events là text protocol đơn giản:
```
data: {"type":"step_done","stepId":"step1","status":"success","durationMs":234}\n\n
data: {"type":"step_done","stepId":"step2","status":"success","durationMs":1}\n\n
data: {"type":"finished","status":"success"}\n\n
```

Mỗi event: `data: <payload>\n\n` (double newline phân tách events). Browser's `EventSource` API tự parse format này.

### Headers quan trọng

| Header | Giá trị | Tại sao |
|---|---|---|
| `Content-Type` | `text/event-stream` | Browser nhận ra SSE protocol |
| `Cache-Control` | `no-cache` | Không cache stream ở proxy |
| `Connection` | `keep-alive` | Giữ HTTP connection mở |

`http.Flusher` interface: Go's `http.ResponseWriter` implement `Flusher` cho streaming. `Flush()` đảm bảo bytes được gửi xuống network ngay lập tức, không đợi buffer đầy.

---

## Bước 6 — Luồng dữ liệu đầy đủ Stage 4

### Cache flow

```
GET /workflows/1
    ↓
GetWorkflowLogic
    ├── Redis.Get("workflow:1")  → HIT → return (không query DB)
    └── MISS → DB.First → Redis.SetEx("workflow:1", json, 5min) → return
```

### Execution + Pub/Sub flow

```
POST /webhooks/1  ─────────────────────────────────┐
    ↓                                               │
TriggerWorkflowLogic                                │
    ├── DB.First(&wf)                               │
    ├── DB.Create(&execution)  {status: "running"}  │
    └── engine.RunWorkflow(ctx, db, rdb, ...)        │
            ↓                                       │
        Step 1 chạy xong                            │
        Redis.Publish("executions:1",               │ Parallel:
            {type:"step_done",stepId:"step1",...})  │
            ↓                                       │
        Step 2 chạy xong                            │ GET /executions/1/stream
        Redis.Publish(...)                          │     ↓
            ↓                                       │ Redis.Subscribe("executions:1")
        finishExecution                             │     ↓
        Redis.Publish("executions:1",               │ <-pubsub.Channel()
            {type:"finished",status:"success"})  ───┼──→ fmt.Fprintf(w, "data: ...\n\n")
            ↓                                       │     Flush()
Response: {executionId: 1}  ←───────────────────────┘     → return (isFinished)
```

Client JavaScript:
```javascript
const es = new EventSource('/executions/1/stream')
es.onmessage = (e) => {
    const event = JSON.parse(e.data)
    console.log(event.type, event.stepId, event.status)
    if (event.type === 'finished') es.close()
}
```

---

## Bước 7 — Test

### Cache test

```bash
# Request 1: cache miss → query DB
time curl http://localhost:8888/workflows/1

# Request 2: cache hit → Redis only (nhanh hơn đáng kể)
time curl http://localhost:8888/workflows/1

# Kiểm tra Redis
docker exec $(docker-compose ps -q redis) redis-cli GET "workflow:1"
# → {"id":1,"name":"...","triggerType":"webhook",...}

# Sau khi update → cache invalidate
curl -X PUT http://localhost:8888/workflows/1 -H "Content-Type: application/json" -d '{"name":"Updated Name"}'
docker exec $(docker-compose ps -q redis) redis-cli GET "workflow:1"
# → (nil)  ← key đã bị xóa
```

### SSE test

```bash
# Terminal 1: mở SSE stream (block và chờ)
curl -N http://localhost:8888/executions/1/stream

# Terminal 2: trigger workflow
curl -X POST http://localhost:8888/webhooks/1

# Terminal 1 sẽ nhận:
# data: {"type":"step_done","executionId":1,"stepId":"step1","status":"success","durationMs":234}
# data: {"type":"finished","executionId":1,"status":"success"}
```

`curl -N` tắt buffering để nhận từng event ngay lập tức.

---

## Tóm tắt kiến thức đã học

| Khái niệm | Áp dụng |
|---|---|
| Cache-aside pattern | Get → check cache → miss → DB → populate cache |
| Write-invalidate | Update/Delete xóa cache ngay, không write-through |
| Redis TTL | `SetEx(key, val, 5min)` — cache tự expire, không cần manual cleanup |
| Redis Pub/Sub | `Publish(channel, msg)` + `Subscribe(channel)` — 1-to-many messaging |
| Server-Sent Events | `Content-Type: text/event-stream` + `data: ...\n\n` format |
| `http.Flusher` | Interface để flush response buffer ngay lập tức |
| SSE vs WebSocket | SSE: server→client only, HTTP/1.1, dễ implement. WS: bidirectional, cần upgrade |
| `context.Background()` cho publish | Tránh publish bị cancel khi HTTP request kết thúc |

---

## Cái gì chưa làm (để sang Stage 5+)

- Auth: ai cũng có thể trigger và xem execution của người khác
- Stage 5: JWT middleware → chỉ owner mới access được workflow/execution của họ
- Stage 6: Webhook trả về ngay (`{status: "pending"}`), execution chạy async qua RabbitMQ
- Cache cho `ListWorkflows` (phức tạp hơn vì cần invalidate khi add/remove)
- Retry on cache connection failure (hiện tại nếu Redis down → GetWorkflow vẫn hoạt động vì cache miss fallback về DB)
