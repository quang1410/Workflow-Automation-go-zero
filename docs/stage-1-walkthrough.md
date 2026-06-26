# Stage 1 — Go cơ bản + Go-Zero API: Walkthrough từng bước

> **Mục tiêu**: Dựng Go-Zero HTTP service với 3 endpoints hardcoded (chưa có DB), đồng thời học các khái niệm Go cốt lõi qua playground exercises.

---

## Tổng quan những gì sẽ xây dựng

```
api-gateway/          ← Go-Zero HTTP service (port 8888)
  gateway.go          ← entry point
  gateway.api         ← API definition (source of truth)
  etc/gateway-api.yaml
  internal/
    config/           ← Config struct
    handler/          ← HTTP layer (parse request → gọi logic → ghi response)
    logic/            ← Business logic
    svc/              ← ServiceContext (DI container)
    types/            ← Request/response structs (auto-generated)

playground/
  step_runner_demo.go ← Bài tập Go: goroutine, channel, context, interface
```

---

## Bước 1 — Khởi tạo project với Go Workspace

```bash
mkdir go-workflow && cd go-workflow
go work init
goctl api new api-gateway
go work use ./api-gateway
```

**`go.work`** — Go Workspace file, dùng khi có nhiều module trong cùng repo:

```
go 1.24.0

use ./api-gateway
```

Khi thêm service mới (Stage 7 — execution-engine), chạy `go work use ./execution-engine` để workspace nhận ra module đó.

**`goctl api new api-gateway`** scaffold sẵn cấu trúc thư mục và file `gateway.api` mẫu. Từ đây mình sửa `.api` rồi để goctl generate code.

---

## Bước 2 — Viết API Definition (gateway.api)

**File**: [api-gateway/gateway.api](../api-gateway/gateway.api)

File `.api` là **source of truth** — goctl đọc file này để generate handlers, routes, và types. Không bao giờ sửa tay file generated.

```
syntax = "v1"

// --- Types ---
type HealthResp {
    Status  string `json:"status"`
    Version string `json:"version"`
}

type WorkflowItem {
    Id          int64  `json:"id"`
    Name        string `json:"name"`
    TriggerType string `json:"triggerType"`
    // ...
}

type TriggerReq {
    WorkflowId int64  `path:"workflowId"`   // path param :workflowId
    Payload    string `json:"payload,optional"`
}

type TriggerResp {
    Message string `json:"message"`
}

// --- Routes ---
service gateway-api {
    @handler HealthHandler
    get /health returns (HealthResp)

    @handler ListWorkflowsHandler
    get /workflows returns (ListWorkflowsResp)

    @handler TriggerWorkflowHandler
    post /webhooks/:workflowId (TriggerReq) returns (TriggerResp)
}
```

### Cú pháp quan trọng trong .api

| Cú pháp | Ý nghĩa |
|---|---|
| `path:"workflowId"` | Lấy giá trị từ URL path (`:workflowId`) |
| `json:"name,optional"` | Field không bắt buộc trong request body |
| `@handler HealthHandler` | Tên function handler sẽ được generate |
| `(TriggerReq)` | Request body type |
| `returns (TriggerResp)` | Response type |

---

## Bước 3 — Generate code với goctl

```bash
cd api-gateway
goctl api go -api gateway.api -dir .
```

Lệnh này tạo/ghi đè:
- `internal/types/types.go` — Go structs tương ứng với types trong `.api`
- `internal/handler/routes.go` — đăng ký tất cả routes vào Go-Zero server
- `internal/handler/*handler.go` — skeleton cho từng handler (chỉ generate lần đầu, không ghi đè)
- `internal/logic/*logic.go` — skeleton cho từng logic (chỉ generate lần đầu)

**Quy tắc**: Chỉ sửa `handler/` và `logic/`. Không sửa `types/types.go` và `handler/routes.go` — chúng bị ghi đè mỗi lần chạy goctl.

---

## Bước 4 — Config struct và YAML

**File**: [api-gateway/internal/config/config.go](../api-gateway/internal/config/config.go)

```go
package config

import "github.com/zeromicro/go-zero/rest"

type Config struct {
    rest.RestConf   // nhúng vào: Host, Port, Timeout, MaxConns, ...
}
```

`rest.RestConf` chứa sẵn các field cấu hình server. Stage 2 mở rộng thêm `DB DBConfig`.

**File**: [api-gateway/etc/gateway-api.yaml](../api-gateway/etc/gateway-api.yaml)

```yaml
Name: gateway-api
Host: 0.0.0.0
Port: 8888
```

Go-Zero dùng reflection để map YAML keys → Config struct fields tự động.

---

## Bước 5 — ServiceContext (Dependency Injection)

**File**: [api-gateway/internal/svc/servicecontext.go](../api-gateway/internal/svc/servicecontext.go)

```go
package svc

import "api-gateway/internal/config"

type ServiceContext struct {
    Config config.Config
    // Stage 2 thêm: DB *gorm.DB
    // Stage 4 thêm: Redis *redis.Client
}

func NewServiceContext(c config.Config) *ServiceContext {
    return &ServiceContext{Config: c}
}
```

`ServiceContext` là **DI container** — khởi tạo 1 lần ở `main()`, được pass vào tất cả handlers và từ đó vào logic. Mọi shared dependency (DB, Redis, Queue) đều sống ở đây.

---

## Bước 6 — Entry Point (gateway.go)

**File**: [api-gateway/gateway.go](../api-gateway/gateway.go)

```go
package main

import (
    "flag"
    "fmt"
    "api-gateway/internal/config"
    "api-gateway/internal/handler"
    "api-gateway/internal/svc"
    "github.com/zeromicro/go-zero/core/conf"
    "github.com/zeromicro/go-zero/rest"
)

var configFile = flag.String("f", "etc/gateway-api.yaml", "the config file")

func main() {
    flag.Parse()

    var c config.Config
    conf.MustLoad(*configFile, &c)   // đọc YAML → Config struct

    server := rest.MustNewServer(c.RestConf)
    defer server.Stop()

    ctx := svc.NewServiceContext(c)  // khởi tạo DI container
    handler.RegisterHandlers(server, ctx)  // đăng ký tất cả routes

    fmt.Printf("Starting server at %s:%d...\n", c.RestConf.Host, c.RestConf.Port)
    server.Start()
}
```

Luồng khởi động: **parse flag → load YAML → tạo server → tạo ServiceContext → đăng ký routes → start**.

---

## Bước 7 — Implement Logic (hardcoded, chưa có DB)

### Health

**File**: [api-gateway/internal/logic/healthlogic.go](../api-gateway/internal/logic/healthlogic.go)

```go
type HealthLogic struct {
    logx.Logger
    ctx    context.Context
    svcCtx *svc.ServiceContext
}

func NewHealthLogic(ctx context.Context, svcCtx *svc.ServiceContext) *HealthLogic {
    return &HealthLogic{
        Logger: logx.WithContext(ctx),
        ctx:    ctx,
        svcCtx: svcCtx,
    }
}

func (l *HealthLogic) Health() (*types.HealthResp, error) {
    return &types.HealthResp{
        Status:  "ok",
        Version: "1.0.0",
    }, nil
}
```

Mọi Logic struct đều có 3 field: `logx.Logger` (logging), `ctx` (context), `svcCtx` (dependencies). Pattern này nhất quán ở mọi logic trong project.

### Anatomy của một Logic struct

```
NewXxxLogic(ctx, svcCtx)   ← constructor, được gọi từ handler
  └── l.svcCtx.DB          ← truy cập shared resource
  └── l.ctx                ← truyền vào DB query, HTTP call để có timeout/cancel
  └── l.Logger.Info(...)   ← structured log kèm trace ID tự động
```

---

## Bước 8 — Implement Handlers

### Pattern chuẩn

**File**: [api-gateway/internal/handler/healthhandler.go](../api-gateway/internal/handler/healthhandler.go)

```go
func HealthHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        l := logic.NewHealthLogic(r.Context(), svcCtx)
        resp, err := l.Health()
        if err != nil {
            httpx.ErrorCtx(r.Context(), w, err)
        } else {
            httpx.OkJsonCtx(r.Context(), w, resp)
        }
    }
}
```

Handler không có `httpx.Parse` vì `/health` không nhận request body hay path params.

### Handler có request params

```go
func TriggerWorkflowHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        var req types.TriggerReq
        if err := httpx.Parse(r, &req); err != nil {   // parse :workflowId + body
            httpx.ErrorCtx(r.Context(), w, err)
            return
        }
        l := logic.NewTriggerWorkflowLogic(r.Context(), svcCtx)
        resp, err := l.TriggerWorkflow(&req)
        if err != nil {
            httpx.ErrorCtx(r.Context(), w, err)
        } else {
            httpx.OkJsonCtx(r.Context(), w, resp)
        }
    }
}
```

`httpx.Parse` xử lý cùng lúc: **path params** (`:workflowId`), **query params** (`?page=1`), **JSON body** — không cần parse riêng từng loại.

### Routes (auto-generated, không sửa tay)

**File**: [api-gateway/internal/handler/routes.go](../api-gateway/internal/handler/routes.go)

```go
func RegisterHandlers(server *rest.Server, serverCtx *svc.ServiceContext) {
    server.AddRoutes([]rest.Route{
        {Method: http.MethodGet,  Path: "/health",              Handler: HealthHandler(serverCtx)},
        {Method: http.MethodGet,  Path: "/workflows",           Handler: ListWorkflowsHandler(serverCtx)},
        {Method: http.MethodPost, Path: "/webhooks/:workflowId",Handler: TriggerWorkflowHandler(serverCtx)},
    })
}
```

---

## Bước 9 — Playground Exercises

**File**: [playground/step_runner_demo.go](../playground/step_runner_demo.go)

Playground không dùng Go-Zero — chạy độc lập để học Go thuần. Bốn khái niệm được minh họa:

### 1. Custom error type

```go
type StepError struct {
    StepID string
    Reason string
}

func (e *StepError) Error() string {
    return fmt.Sprintf("step %s failed: %s", e.StepID, e.Reason)
}
```

Implement interface `error` (có method `Error() string`). Dùng `errors.As(err, &stepErr)` để unwrap và lấy field `StepID`.

### 2. StepRunner interface

```go
type StepRunner interface {
    Run(ctx context.Context, input map[string]any) (map[string]any, error)
}

type HTTPStepRunner struct{ URL string }
type TransformStepRunner struct{ OutputKey string }
```

Interface định nghĩa **hành vi**, không quan tâm struct cụ thể. Stage 3 sẽ dùng đúng interface này trong engine thật, nên playground là bản preview của thiết kế thật.

### 3. Fan-out với goroutine + channel

```go
results := make(chan StepResult, len(runners))   // buffered channel

for _, s := range runners {
    s := s   // capture loop variable — quan trọng!
    go func() {
        out, err := s.runner.Run(ctx, map[string]any{"from": s.id})
        results <- StepResult{StepID: s.id, Output: out, Err: err}
    }()
}

for range runners {
    r := <-results   // collect từng kết quả
    // xử lý r
}
```

`s := s` trong loop là pattern bắt buộc ở Go < 1.22 để tránh closure capture cùng một biến. Channel có buffer bằng số goroutines để không có goroutine nào bị block khi gửi.

### 4. Context timeout + cancellation

```go
ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
defer cancel()

runner := &HTTPStepRunner{URL: "https://slow-api.example.com"}
out, err := runner.Run(ctx, nil)
```

Bên trong runner, `select` lắng nghe cả hai channel:

```go
func (r *HTTPStepRunner) Run(ctx context.Context, input map[string]any) (map[string]any, error) {
    select {
    case <-time.After(delay):           // thành công sau delay
        return map[string]any{"status": 200}, nil
    case <-ctx.Done():                  // timeout hoặc cancel
        return nil, &StepError{StepID: "http", Reason: ctx.Err().Error()}
    }
}
```

`ctx.Done()` trả về channel được đóng khi timeout hoặc bị cancel. Đây là cơ chế Go-Zero dùng để timeout từng step trong Stage 3.

### 5. Sequential workflow với done channel

```go
done := make(chan struct{})
go func() {
    defer close(done)
    input := map[string]any{}
    for _, s := range steps {
        out, err := s.runner.Run(ctx, input)
        if err != nil {
            fmt.Printf("[%s] FAILED — stopping\n", s.id)
            return   // close(done) được gọi bởi defer
        }
        input = out   // output step này = input step tiếp theo
    }
}()
<-done   // chờ workflow xong
```

Chạy playground:
```bash
go run playground/step_runner_demo.go
```

---

## Bước 10 — Chạy và kiểm tra

```bash
go run api-gateway/gateway.go -f api-gateway/etc/gateway-api.yaml
```

```bash
# Health check
curl http://localhost:8888/health
# → {"status":"ok","version":"1.0.0"}

# List workflows (hardcoded empty)
curl http://localhost:8888/workflows
# → {"items":[],"total":0}

# Trigger webhook
curl -X POST http://localhost:8888/webhooks/1 \
  -H "Content-Type: application/json" \
  -d '{"payload":"{\"user\":\"alice\"}"}'
# → {"message":"triggered","executionId":0}  (Stage 1: hardcoded, chưa có DB)
```

---

## Luồng dữ liệu một request

```
curl POST /webhooks/1
    ↓
routes.go          RegisterHandlers → match path → TriggerWorkflowHandler
    ↓
handler            httpx.Parse(r, &req)  → req.WorkflowId = 1, req.Payload = ...
    ↓
logic              NewTriggerWorkflowLogic(ctx, svcCtx)
                   l.TriggerWorkflow(&req)  → return TriggerResp
    ↓
handler            httpx.OkJsonCtx(w, resp)  → 200 {"message":"triggered"}
```

---

## Tóm tắt kiến thức đã học

| Khái niệm | Áp dụng |
|---|---|
| Go-Zero `.api` file | Source of truth cho API — goctl generate types + routes từ đây |
| `goctl api go` | Code generation — tạo handler skeleton + types + routes |
| `ServiceContext` | DI container — khởi tạo một lần, inject vào mọi logic |
| `httpx.Parse` | Parse path params + query params + JSON body trong một lần gọi |
| `httpx.OkJsonCtx` | Trả 200 + JSON response với trace context |
| `goroutine` + `channel` | Fan-out: chạy nhiều step song song, collect results |
| `context.WithTimeout` | Giới hạn thời gian một operation, lan truyền qua call stack |
| `interface` | `StepRunner.Run` — định nghĩa hành vi, dùng lại ở Stage 3 |
| Custom `error` | `StepError{StepID, Reason}` — lỗi có context, unwrap bằng `errors.As` |
| `select` | Lắng nghe nhiều channel đồng thời — timeout pattern cốt lõi |

---

## Cái gì chưa làm (để sang Stage 2)

- Logic trả về data hardcoded — Stage 2 thay bằng PostgreSQL query
- `ServiceContext` chưa có `*gorm.DB` — Stage 2 thêm vào
- `go.work` chỉ có `api-gateway` — Stage 7 thêm `execution-engine`
