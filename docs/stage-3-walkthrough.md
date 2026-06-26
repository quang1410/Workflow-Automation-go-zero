# Stage 3 — Execution Engine: Walkthrough từng bước

> **Mục tiêu**: Thay thế placeholder StepLog ở Stage 2 bằng engine chạy thật — đọc `steps` từ JSONB, chạy từng step tuần tự với context timeout, ghi kết quả vào `step_logs`.

---

## Tổng quan những gì thay đổi

```
api-gateway/internal/engine/   ← Package mới hoàn toàn
  runner.go         ← StepDef struct, StepRunner interface, factory
  executor.go       ← RunWorkflow orchestrates toàn bộ flow
  http_runner.go    ← step type: http_request
  transform_runner.go ← step type: transform
  delay_runner.go   ← step type: delay
  condition_runner.go ← step type: condition
  email_runner.go   ← step type: send_email

api-gateway/internal/logic/executionlogic.go  ← TriggerWorkflow gọi engine
```

Stage 2 chỉ insert 1 placeholder StepLog `{StepID: "step1", StepType: "pending"}`.  
Stage 3 thay bằng engine thật: parse JSON steps → chạy từng step → ghi input/output/duration vào DB.

---

## Bước 1 — Thiết kế StepDef và StepRunner interface

**File**: [api-gateway/internal/engine/runner.go](../api-gateway/internal/engine/runner.go)

```go
// StepDef mirrors the JSON stored in workflows.steps JSONB column.
type StepDef struct {
    ID     string         `json:"id"`
    Type   string         `json:"type"`
    Config map[string]any `json:"config"`
}

type StepRunner interface {
    Run(ctx context.Context, input map[string]any) (map[string]any, error)
}
```

`StepDef` là struct Go ánh xạ chính xác với JSON lưu trong cột `steps JSONB` của bảng `workflows`:

```json
[
  {"id": "step1", "type": "http_request", "config": {"url": "https://...", "method": "GET"}},
  {"id": "step2", "type": "send_email",   "config": {"to": "admin@example.com", "subject": "done"}}
]
```

`StepRunner` là interface cốt lõi — nhận `input` (output của step trước), trả về `output` (input của step sau). Interface này giống hệt playground Stage 1, nhưng nay là production code.

### Factory pattern

```go
func NewStepRunner(step StepDef) (StepRunner, error) {
    switch step.Type {
    case "http_request": return newHTTPRequestRunner(step.Config)
    case "transform":    return newTransformRunner(step.Config)
    case "delay":        return newDelayRunner(step.Config)
    case "condition":    return newConditionRunner(step.Config)
    case "send_email":   return newSendEmailRunner(step.Config)
    default:             return nil, fmt.Errorf("unknown step type: %s", step.Type)
    }
}
```

Factory: nhận `StepDef`, trả về `StepRunner` cụ thể. Logic chọn runner bị giới hạn ở đây — mỗi runner không biết gì về các runner khác.

---

## Bước 2 — Implement các StepRunner

### http_request

**File**: [api-gateway/internal/engine/http_runner.go](../api-gateway/internal/engine/http_runner.go)

```go
func (r *httpRequestRunner) Run(ctx context.Context, input map[string]any) (map[string]any, error) {
    req, err := http.NewRequestWithContext(ctx, r.method, r.url, bodyReader)
    // ...
    resp, err := http.DefaultClient.Do(req)
    // ...
    return map[string]any{"status": resp.StatusCode, "body": parsed}, nil
}
```

**Điểm quan trọng**: `http.NewRequestWithContext(ctx, ...)` — truyền context vào HTTP call. Khi `ctx` timeout hoặc bị cancel, `DefaultClient.Do` sẽ trả về lỗi ngay lập tức. Đây là cách context propagation hoạt động trong Go.

Config nhận: `url`, `method` (mặc định GET), `body` (optional JSON string).  
Output: `{"status": 200, "body": <parsed JSON hoặc raw string>}`

### transform

**File**: [api-gateway/internal/engine/transform_runner.go](../api-gateway/internal/engine/transform_runner.go)

```go
func (r *transformRunner) Run(ctx context.Context, input map[string]any) (map[string]any, error) {
    t, _ := template.New("transform").Parse(r.tmpl)
    var buf bytes.Buffer
    t.Execute(&buf, input)
    return map[string]any{r.outputKey: buf.String()}, nil
}
```

Dùng `text/template` của Go standard library. Input map được expose vào template.

Ví dụ config:
```json
{"template": "Hello {{.name}}!", "outputKey": "greeting"}
```
Input `{"name": "Alice"}` → Output `{"greeting": "Hello Alice!"}`

### delay

**File**: [api-gateway/internal/engine/delay_runner.go](../api-gateway/internal/engine/delay_runner.go)

```go
func (r *delayRunner) Run(ctx context.Context, input map[string]any) (map[string]any, error) {
    select {
    case <-time.After(r.duration):
        return input, nil   // pass input through unchanged
    case <-ctx.Done():
        return nil, fmt.Errorf("delay interrupted: %w", ctx.Err())
    }
}
```

Pattern `select` quen thuộc từ playground. `input` được pass-through không thay đổi. Config: `{"seconds": 5}`.

### condition

**File**: [api-gateway/internal/engine/condition_runner.go](../api-gateway/internal/engine/condition_runner.go)

Evaluate biểu thức đơn giản: `field operator value`.

Config ví dụ: `{"field": "status", "operator": "==", "value": "200"}`

Hỗ trợ operators: `eq/==`, `neq/!=`, `gt/>`, `lt/<`.

Output: `{"condition": true/false, "input": {...}}` — step sau có thể đọc `condition` để quyết định logic tiếp theo.

### send_email

**File**: [api-gateway/internal/engine/email_runner.go](../api-gateway/internal/engine/email_runner.go)

Stage 3: chỉ `fmt.Printf` ra stdout. Stage sau sẽ kết nối SMTP thật.

Config: `{"to": "user@example.com", "subject": "...", "body": "..."}`.  
Output: `{"sent": true, "to": "user@example.com"}`

---

## Bước 3 — Executor: RunWorkflow

**File**: [api-gateway/internal/engine/executor.go](../api-gateway/internal/engine/executor.go)

Đây là heart của Stage 3 — orchestrate toàn bộ execution flow:

```go
func RunWorkflow(ctx context.Context, db *gorm.DB, execution *model.Execution, wf *model.Workflow) {
    var steps []StepDef
    json.Unmarshal(wf.Steps, &steps)   // parse JSONB → []StepDef

    input := map[string]any{}

    for _, stepDef := range steps {
        stepCtx, cancel := context.WithTimeout(ctx, stepTimeout)  // 30s per step
        startedAt := time.Now()

        runner, _ := NewStepRunner(stepDef)
        output, err := runner.Run(stepCtx, input)
        cancel()   // LUÔN cancel để giải phóng resources

        durationMs := time.Since(startedAt).Milliseconds()
        // ... ghi StepLog vào DB

        if err != nil {
            finishExecution(db, execution, "failed")
            return
        }

        input = output   // output step này = input step tiếp theo
    }

    finishExecution(db, execution, "success")
}
```

### Tại sao `cancel()` phải gọi ngay cả khi không timeout?

```go
stepCtx, cancel := context.WithTimeout(ctx, stepTimeout)
// ... Run ...
cancel()  // CRITICAL: tránh context leak
```

`context.WithTimeout` tạo internal goroutine để track deadline. Nếu không gọi `cancel()`, goroutine đó chỉ được giải phóng khi deadline tới. Với nhiều steps, nhiều executions → goroutine leak. Pattern chuẩn: luôn `defer cancel()` hoặc gọi ngay sau khi dùng xong.

### Data flow giữa các steps

```
input = {}

Step 1 (http_request) → output = {"status": 200, "body": {"user": "alice"}}
                ↓
Step 2 (transform)     input = {"status": 200, "body": {...}}
                       output = {"greeting": "Hello alice!"}
                ↓
Step 3 (send_email)    input = {"greeting": "Hello alice!"}
```

Output của mỗi step trở thành input của step tiếp theo. Đây là data pipeline pattern — giống n8n/Zapier.

### finishExecution

```go
func finishExecution(db *gorm.DB, execution *model.Execution, status string) {
    now := time.Now()
    db.Model(execution).Updates(map[string]any{
        "status":      status,
        "finished_at": &now,
    })
}
```

Dùng `map[string]any` để update — cùng lý do như Stage 2: tránh GORM bỏ qua zero values khi dùng struct update.

---

## Bước 4 — Wire engine vào TriggerWorkflow

**File**: [api-gateway/internal/logic/executionlogic.go](../api-gateway/internal/logic/executionlogic.go)

Stage 2 (cũ):
```go
txErr := l.svcCtx.DB.Transaction(func(tx *gorm.DB) error {
    tx.Create(&execution)
    // Placeholder
    steps := []model.StepLog{{StepID: "step1", StepType: "pending", Status: "pending"}}
    return tx.Create(&steps).Error
})
```

Stage 3 (mới):
```go
execution := model.Execution{
    WorkflowID:     uint(req.WorkflowId),
    Status:         "running",
    StartedAt:      time.Now(),
    TriggerPayload: datatypes.JSON(payload),
}
l.svcCtx.DB.Create(&execution)   // GORM điền execution.ID

// Run workflow synchronously — Stage 6 moves this to async via RabbitMQ.
engine.RunWorkflow(l.ctx, l.svcCtx.DB, &execution, &wf)
```

**Tại sao bỏ Transaction?**  
Transaction ở Stage 2 dùng để atomic insert Execution + placeholder StepLogs. Nay StepLogs được tạo trong `RunWorkflow` (mỗi step xong tạo 1 log), không thể gói tất cả vào 1 transaction trước. Chỉ cần tạo Execution record trước, engine tự handle StepLogs.

**Tại sao synchronous?**  
HTTP request chờ workflow chạy xong mới trả về. Đơn giản nhất. Stage 6 (RabbitMQ) sẽ tách thành: webhook → enqueue → trả về ngay (< 50ms), worker xử lý async.

**`l.ctx` là gì?**  
Context của HTTP request — khi client disconnect, `l.ctx` bị cancel → `RunWorkflow` nhận được qua `stepCtx`. Timeout per step là 30s, nhưng nếu request timeout trước → toàn bộ execution dừng.

---

## Bước 5 — Test end-to-end

### 1. Tạo workflow có steps thật

```bash
curl -X POST http://localhost:8888/workflows \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Test Workflow",
    "triggerType": "webhook",
    "steps": "[{\"id\":\"step1\",\"type\":\"http_request\",\"config\":{\"url\":\"https://httpbin.org/get\",\"method\":\"GET\"}},{\"id\":\"step2\",\"type\":\"send_email\",\"config\":{\"to\":\"admin@example.com\",\"subject\":\"done\"}}]"
  }'
# → {"id": 1}
```

### 2. Trigger workflow

```bash
curl -X POST http://localhost:8888/webhooks/1
# → {"message":"workflow triggered successfully","executionId":1}
```

Server log sẽ in:
```
[EMAIL] to=admin@example.com subject="done" body=""
```

### 3. Kiểm tra step logs

```bash
curl http://localhost:8888/executions/1
```

Response:
```json
{
  "id": 1,
  "status": "success",
  "startedAt": "2026-06-25T10:00:00Z",
  "stepLogs": [
    {
      "stepId": "step1",
      "stepType": "http_request",
      "status": "success",
      "input": "{}",
      "output": "{\"body\":{...},\"status\":200}",
      "durationMs": 234
    },
    {
      "stepId": "step2",
      "stepType": "send_email",
      "status": "success",
      "input": "{\"body\":{...},\"status\":200}",
      "output": "{\"sent\":true,\"to\":\"admin@example.com\"}",
      "durationMs": 1
    }
  ]
}
```

### 4. Test workflow failed (step type không tồn tại)

```bash
curl -X POST http://localhost:8888/workflows \
  -H "Content-Type: application/json" \
  -d '{"name":"Bad Workflow","triggerType":"webhook","steps":"[{\"id\":\"s1\",\"type\":\"unknown_type\",\"config\":{}}]"}'

curl -X POST http://localhost:8888/webhooks/2
curl http://localhost:8888/executions/2
# → status: "failed", stepLogs[0].error: "unknown step type: unknown_type"
```

---

## Luồng dữ liệu đầy đủ Stage 3

```
POST /webhooks/1
    ↓
TriggerWorkflowLogic
    ├── DB.First(&wf, 1)              -- load workflow + steps JSON
    ├── DB.Create(&execution)         -- status: "running"
    └── engine.RunWorkflow(ctx, db, &execution, &wf)
            ↓
        json.Unmarshal(wf.Steps)      -- parse "[]StepDef"
            ↓
        Loop steps:
          ├── context.WithTimeout(ctx, 30s)
          ├── NewStepRunner(stepDef)  -- factory chọn runner
          ├── runner.Run(stepCtx, input)
          ├── DB.Create(&StepLog)     -- ghi input/output/duration
          └── input = output          -- chain sang step tiếp theo
            ↓
        finishExecution(db, execution, "success")
            -- DB.Updates: status="success", finished_at=now
    ↓
Response: {"executionId": 1, "message": "workflow triggered successfully"}
```

---

## Tóm tắt kiến thức đã học

| Khái niệm | Áp dụng |
|---|---|
| `interface` + factory | `StepRunner` interface, `NewStepRunner` switch — thêm step type mới chỉ cần thêm 1 case |
| `context.WithTimeout` per step | Mỗi step có deadline riêng 30s — step chậm không block cả workflow |
| `cancel()` | Gọi ngay sau `Run()` để tránh goroutine leak |
| Data pipeline | `input = output` — output step trước là input step sau |
| `http.NewRequestWithContext` | HTTP call nhận context — timeout/cancel được propagate tự động |
| `text/template` | Biến đổi data động với Go template syntax |
| `select` trong delay/condition | Lắng nghe cả timeout lẫn ctx.Done() |
| GORM `Create(&struct)` | GORM fill `.ID` in-place sau khi insert thành công |

---

## Cái gì chưa làm (để sang Stage 4+)

- Stage 4: Execution chạy async (không block HTTP request)
- Stage 4: Redis cache workflow definitions
- Stage 5: Auth — chỉ owner mới trigger được workflow của họ
- Stage 6: RabbitMQ queue — webhook chỉ enqueue, worker xử lý
- Step `transform` chưa hỗ trợ nested fields (`.body.name`) — cần `FuncMap` custom
- Step `condition` chỉ so sánh top-level fields của input map
