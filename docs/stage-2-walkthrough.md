# Stage 2 — PostgreSQL + GORM: Walkthrough từng bước

> **Mục tiêu**: Thay thế hardcoded data (Stage 1) bằng PostgreSQL thật. Học GORM CRUD, AutoMigrate, JSONB, và Transaction.

---

## Tổng quan những gì sẽ thay đổi

```
Stage 1:                       Stage 2:
GET /workflows → []            GET /workflows → Query PostgreSQL
                               POST /workflows → INSERT vào DB
                               GET /executions/:id → Preload StepLogs
                               POST /webhooks/:id → Transaction (Execution + StepLog)
```

---

## Bước 1 — Thêm PostgreSQL vào docker-compose.yml

**File**: [docker-compose.yml](../docker-compose.yml)

```yaml
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: workflow
      POSTGRES_PASSWORD: workflow
      POSTGRES_DB: workflow_db
    ports:
      - "5432:5432"
    volumes:
      - postgres_data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U workflow -d workflow_db"]
      interval: 5s
      timeout: 5s
      retries: 5

volumes:
  postgres_data:
```

Chạy:
```bash
docker-compose up -d
```

**Tại sao dùng `postgres:16-alpine`?** Image alpine nhỏ hơn, đủ cho development. `healthcheck` để đảm bảo container đã ready trước khi app connect.

---

## Bước 2 — Thêm dependencies GORM vào go.mod

```bash
cd api-gateway
go get gorm.io/gorm
go get gorm.io/driver/postgres
go get gorm.io/datatypes
```

- `gorm.io/gorm` — ORM core
- `gorm.io/driver/postgres` — driver kết nối PostgreSQL (dùng `pgx` bên dưới)
- `gorm.io/datatypes` — hỗ trợ kiểu `datatypes.JSON` để map JSONB column

---

## Bước 3 — Định nghĩa GORM Models

**File**: [api-gateway/model/models.go](../api-gateway/model/models.go)

```go
package model

import (
    "time"
    "gorm.io/datatypes"
    "gorm.io/gorm"
)

type Workflow struct {
    gorm.Model                            // ID, CreatedAt, UpdatedAt, DeletedAt
    Name          string         `gorm:"not null"`
    TriggerType   string         `gorm:"not null"` // webhook | schedule | manual
    TriggerConfig datatypes.JSON            // JSONB: { "url": "..." }
    Steps         datatypes.JSON            // JSONB: [{ "id": "step1", ... }]
    IsActive      bool           `gorm:"default:true"`
}

type Execution struct {
    gorm.Model
    WorkflowID     uint
    Workflow       Workflow               // belongs-to association
    Status         string `gorm:"default:running"` // running | success | failed
    StartedAt      time.Time
    FinishedAt     *time.Time             // pointer → nullable
    TriggerPayload datatypes.JSON
    StepLogs       []StepLog              // has-many association
}

type StepLog struct {
    gorm.Model
    ExecutionID uint
    StepID      string
    StepType    string
    Status      string // success | failed | skipped
    Input       datatypes.JSON
    Output      datatypes.JSON
    Error       string
    DurationMs  int64
}
```

### Điểm cần chú ý

| Kỹ thuật | Giải thích |
|---|---|
| `gorm.Model` | Nhúng vào struct để tự động có `ID`, `CreatedAt`, `UpdatedAt`, `DeletedAt` |
| `datatypes.JSON` | Map sang column `jsonb` trong PostgreSQL — lưu arbitrary JSON, query được bằng `->` operator |
| `*time.Time` (pointer) | Nullable timestamp — `nil` nghĩa là chưa có giá trị, GORM sẽ lưu `NULL` |
| `StepLogs []StepLog` | Khai báo quan hệ has-many, dùng khi `Preload("StepLogs")` |
| `gorm:"default:true"` | DB-level default, không phải Go-level — quan trọng khi INSERT thiếu field |

---

## Bước 4 — Cập nhật Config để có DB DSN

**File**: [api-gateway/internal/config/config.go](../api-gateway/internal/config/config.go)

```go
package config

import "github.com/zeromicro/go-zero/rest"

type Config struct {
    rest.RestConf
    DB DBConfig        // thêm vào
}

type DBConfig struct {
    DSN string
}
```

**File**: [api-gateway/etc/gateway-api.yaml](../api-gateway/etc/gateway-api.yaml)

```yaml
Name: gateway-api
Host: 0.0.0.0
Port: 8888

DB:
  DSN: "host=localhost user=workflow password=workflow dbname=workflow_db port=5432 sslmode=disable TimeZone=Asia/Ho_Chi_Minh"
```

Go-Zero tự động đọc YAML và map vào `Config` struct bằng reflection. Field `DB.DSN` trong YAML tương ứng với `Config.DB.DSN` trong Go.

---

## Bước 5 — Khởi tạo GORM trong ServiceContext

**File**: [api-gateway/internal/svc/servicecontext.go](../api-gateway/internal/svc/servicecontext.go)

```go
package svc

import (
    "api-gateway/internal/config"
    "api-gateway/model"
    "log"

    "gorm.io/driver/postgres"
    "gorm.io/gorm"
)

type ServiceContext struct {
    Config config.Config
    DB     *gorm.DB        // thêm vào
}

func NewServiceContext(c config.Config) *ServiceContext {
    db, err := gorm.Open(postgres.Open(c.DB.DSN), &gorm.Config{})
    if err != nil {
        log.Fatalf("failed to connect to database: %v", err)
    }

    if err := db.AutoMigrate(&model.Workflow{}, &model.Execution{}, &model.StepLog{}); err != nil {
        log.Fatalf("failed to auto migrate: %v", err)
    }

    return &ServiceContext{
        Config: c,
        DB:     db,
    }
}
```

### AutoMigrate làm gì?

`AutoMigrate` so sánh struct với schema hiện tại trong DB rồi:
- Tạo table nếu chưa có
- Thêm column mới nếu struct có field mới
- **Không xóa** column cũ (an toàn)

Chạy mỗi lần app start — thích hợp cho development. Production thường dùng migration file riêng (flyway, golang-migrate).

---

## Bước 6 — Cập nhật gateway.api (API definition)

**File**: [api-gateway/gateway.api](../api-gateway/gateway.api)

Thêm các types và endpoints mới:

```
// Workflow CRUD
post /workflows (CreateWorkflowReq) returns (CreateWorkflowResp)
get  /workflows/:id (GetWorkflowReq) returns (WorkflowItem)
put  /workflows/:id (UpdateWorkflowReq) returns (UpdateWorkflowResp)
delete /workflows/:id (DeleteWorkflowReq) returns (DeleteWorkflowResp)

// Execution APIs
get /workflows/:workflowId/executions (ListExecutionsReq) returns (ListExecutionsResp)
get /executions/:id (GetExecutionReq) returns (ExecutionItem)
```

Sau khi sửa `.api`, regenerate code:

```bash
cd api-gateway
goctl api go -api gateway.api -dir .
```

Lệnh này ghi đè `internal/types/types.go` và `internal/handler/routes.go` — **đừng sửa 2 file này tay**.

---

## Bước 7 — Implement Workflow CRUD Logic

**File**: [api-gateway/internal/logic/workflowlogic.go](../api-gateway/internal/logic/workflowlogic.go)

### Create

```go
func (l *CreateWorkflowLogic) CreateWorkflow(req *types.CreateWorkflowReq) (*types.CreateWorkflowResp, error) {
    wf := model.Workflow{
        Name:          req.Name,
        TriggerType:   req.TriggerType,
        TriggerConfig: datatypes.JSON(req.TriggerConfig),
        Steps:         datatypes.JSON(req.Steps),
        IsActive:      true,
    }
    if err := l.svcCtx.DB.Create(&wf).Error; err != nil {
        return nil, err
    }
    return &types.CreateWorkflowResp{Id: int64(wf.ID)}, nil
}
```

Sau `DB.Create(&wf)`, GORM tự điền `wf.ID` từ giá trị auto-increment của DB — đó là lý do trả về `wf.ID` sau khi create.

### List

```go
func (l *ListWorkflowsLogic) ListWorkflows() (*types.ListWorkflowsResp, error) {
    var workflows []model.Workflow
    if err := l.svcCtx.DB.Find(&workflows).Error; err != nil {
        return nil, err
    }
    items := make([]types.WorkflowItem, len(workflows))
    for i, wf := range workflows {
        items[i] = toWorkflowItem(wf)
    }
    return &types.ListWorkflowsResp{Items: items, Total: int64(len(items))}, nil
}
```

### Get (với 404 handling)

```go
func (l *GetWorkflowLogic) GetWorkflow(req *types.GetWorkflowReq) (*types.WorkflowItem, error) {
    var wf model.Workflow
    if err := l.svcCtx.DB.First(&wf, req.Id).Error; err != nil {
        if errors.Is(err, gorm.ErrRecordNotFound) {
            return nil, errors.New("workflow not found")
        }
        return nil, err
    }
    item := toWorkflowItem(wf)
    return &item, nil
}
```

`errors.Is(err, gorm.ErrRecordNotFound)` phân biệt "không tìm thấy" vs "lỗi DB" để trả về thông báo lỗi phù hợp.

### Update

```go
func (l *UpdateWorkflowLogic) UpdateWorkflow(req *types.UpdateWorkflowReq) (*types.UpdateWorkflowResp, error) {
    updates := map[string]any{"is_active": req.IsActive}
    if req.Name != ""          { updates["name"] = req.Name }
    if req.TriggerType != ""   { updates["trigger_type"] = req.TriggerType }
    if req.TriggerConfig != "" { updates["trigger_config"] = datatypes.JSON(req.TriggerConfig) }
    if req.Steps != ""         { updates["steps"] = datatypes.JSON(req.Steps) }

    if err := l.svcCtx.DB.Model(&model.Workflow{}).Where("id = ?", req.Id).Updates(updates).Error; err != nil {
        return nil, err
    }
    return &types.UpdateWorkflowResp{Ok: true}, nil
}
```

Dùng `map[string]any` thay vì truyền struct để tránh GORM bỏ qua zero-value fields (GORM chỉ update fields khác zero khi truyền struct).

### Delete (Soft Delete)

```go
func (l *DeleteWorkflowLogic) DeleteWorkflow(req *types.DeleteWorkflowReq) (*types.DeleteWorkflowResp, error) {
    if err := l.svcCtx.DB.Delete(&model.Workflow{}, req.Id).Error; err != nil {
        return nil, err
    }
    return &types.DeleteWorkflowResp{Ok: true}, nil
}
```

Vì `Workflow` có `gorm.Model` (có `DeletedAt`), `DB.Delete` chỉ set `DeletedAt = now()` — **không xóa row thật**. Các query sau tự động thêm `WHERE deleted_at IS NULL`.

### Helper

```go
func toWorkflowItem(wf model.Workflow) types.WorkflowItem {
    return types.WorkflowItem{
        Id:            int64(wf.ID),
        Name:          wf.Name,
        TriggerType:   wf.TriggerType,
        TriggerConfig: string(wf.TriggerConfig),
        Steps:         string(wf.Steps),
        IsActive:      wf.IsActive,
    }
}
```

---

## Bước 8 — Implement Execution APIs với Transaction

**File**: [api-gateway/internal/logic/executionlogic.go](../api-gateway/internal/logic/executionlogic.go)

### Trigger (tạo Execution trong Transaction)

```go
func (l *TriggerWorkflowLogic) TriggerWorkflow(req *types.TriggerReq) (*types.TriggerResp, error) {
    // Kiểm tra workflow tồn tại
    var wf model.Workflow
    if err := l.svcCtx.DB.First(&wf, req.WorkflowId).Error; err != nil {
        if errors.Is(err, gorm.ErrRecordNotFound) {
            return nil, errors.New("workflow not found")
        }
        return nil, err
    }

    var execution model.Execution
    txErr := l.svcCtx.DB.Transaction(func(tx *gorm.DB) error {
        execution = model.Execution{
            WorkflowID:     uint(req.WorkflowId),
            Status:         "running",
            StartedAt:      time.Now(),
            TriggerPayload: datatypes.JSON(req.Payload),
        }
        if err := tx.Create(&execution).Error; err != nil {
            return err  // Transaction tự rollback khi return error
        }
        // Stage 3 sẽ thay bằng step logs thật từ kết quả chạy
        steps := []model.StepLog{
            {ExecutionID: execution.ID, StepID: "step1", StepType: "pending", Status: "pending"},
        }
        return tx.Create(&steps).Error
    })
    if txErr != nil {
        return nil, txErr
    }

    return &types.TriggerResp{
        Message:     "workflow triggered successfully",
        ExecutionId: int64(execution.ID),
    }, nil
}
```

**Tại sao dùng Transaction?** Nếu insert `Execution` thành công nhưng insert `StepLog` thất bại, ta sẽ có execution record không có step logs — dữ liệu không nhất quán. Transaction đảm bảo cả hai thành công hoặc cả hai bị rollback.

Pattern `DB.Transaction(func(tx *gorm.DB) error { ... })`: nếu closure return `error` → GORM tự rollback; nếu return `nil` → GORM commit.

### List Executions

```go
func (l *ListExecutionsLogic) ListExecutions(req *types.ListExecutionsReq) (*types.ListExecutionsResp, error) {
    var executions []model.Execution
    if err := l.svcCtx.DB.Where("workflow_id = ?", req.WorkflowId).Find(&executions).Error; err != nil {
        return nil, err
    }
    // map sang response types...
}
```

### Get Execution (với Preload)

```go
func (l *GetExecutionLogic) GetExecution(req *types.GetExecutionReq) (*types.ExecutionItem, error) {
    var ex model.Execution
    if err := l.svcCtx.DB.Preload("StepLogs").First(&ex, req.Id).Error; err != nil {
        // ...
    }
    // map ex.StepLogs sang []types.StepLogItem
}
```

`Preload("StepLogs")` — GORM chạy query thứ hai `SELECT * FROM step_logs WHERE execution_id IN (...)` rồi gán vào `ex.StepLogs`. Tránh N+1 queries so với loop và query từng cái.

---

## Bước 9 — Implement Handlers

**File**: [api-gateway/internal/handler/workflowhandler.go](../api-gateway/internal/handler/workflowhandler.go)

Pattern chuẩn Go-Zero cho mỗi handler:

```go
func CreateWorkflowHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        var req types.CreateWorkflowReq
        if err := httpx.Parse(r, &req); err != nil {   // parse JSON body + path params
            httpx.ErrorCtx(r.Context(), w, err)
            return
        }
        l := logic.NewCreateWorkflowLogic(r.Context(), svcCtx)
        resp, err := l.CreateWorkflow(&req)
        if err != nil {
            httpx.ErrorCtx(r.Context(), w, err)
        } else {
            httpx.OkJsonCtx(r.Context(), w, resp)      // 200 + JSON response
        }
    }
}
```

`httpx.Parse` xử lý cả JSON body lẫn path params (`:id`) và query params trong một lần gọi.

---

## Bước 10 — Kiểm tra

Đảm bảo PostgreSQL đang chạy:
```bash
docker-compose up -d
```

Chạy server:
```bash
go run api-gateway/gateway.go -f api-gateway/etc/gateway-api.yaml
```

### Test Workflow CRUD

```bash
# Tạo workflow
curl -X POST http://localhost:8888/workflows \
  -H "Content-Type: application/json" \
  -d '{
    "name": "My first workflow",
    "triggerType": "webhook",
    "triggerConfig": "{\"url\":\"/webhooks/1\"}",
    "steps": "[{\"id\":\"step1\",\"type\":\"send_email\",\"config\":{\"to\":\"admin@example.com\"}}]"
  }'
# → {"id":1}

# List workflows
curl http://localhost:8888/workflows
# → {"items":[{"id":1,"name":"My first workflow",...}],"total":1}

# Get một workflow
curl http://localhost:8888/workflows/1

# Update
curl -X PUT http://localhost:8888/workflows/1 \
  -H "Content-Type: application/json" \
  -d '{"name":"Updated name","isActive":false}'

# Delete (soft delete)
curl -X DELETE http://localhost:8888/workflows/1
```

### Test Execution

```bash
# Trigger webhook → tạo execution
curl -X POST http://localhost:8888/webhooks/1 \
  -H "Content-Type: application/json" \
  -d '{"payload":"{\"user\":\"alice\"}"}'
# → {"message":"workflow triggered successfully","executionId":1}

# List executions của workflow 1
curl http://localhost:8888/workflows/1/executions

# Xem chi tiết execution + step logs
curl http://localhost:8888/executions/1
```

### Verify trong DB

```bash
docker exec -it <postgres-container> psql -U workflow -d workflow_db
```

```sql
SELECT id, name, trigger_type, is_active, deleted_at FROM workflows;
SELECT id, workflow_id, status, started_at FROM executions;
SELECT id, execution_id, step_id, status FROM step_logs;
```

---

## Tóm tắt kiến thức đã học

| Khái niệm | Áp dụng |
|---|---|
| GORM AutoMigrate | Tự tạo/update schema từ struct — chạy mỗi lần app start |
| `datatypes.JSON` | Lưu JSON linh hoạt vào JSONB column — Steps và TriggerConfig |
| Soft delete | `gorm.Model` có `DeletedAt` → `Delete()` chỉ set timestamp, không xóa row |
| GORM Transaction | `DB.Transaction(func(tx) error)` — all-or-nothing cho Execution + StepLog |
| `Preload` | Load association trong 1 query phụ — tránh N+1 khi lấy StepLogs |
| ServiceContext | DI container — `*gorm.DB` được inject vào mọi Logic qua `svcCtx.DB` |
| Handler pattern | `httpx.Parse` → Logic → `httpx.OkJsonCtx` — nhất quán mọi endpoint |

---

## Cái gì chưa làm (để sang Stage 3)

- `TriggerWorkflow` hiện chỉ tạo 1 placeholder StepLog với status `pending` — Stage 3 sẽ thực sự chạy từng step và ghi output/error thật vào `StepLog`
- Chưa có filter theo `user_id` (Stage 5 — sau khi có Auth)
- Chưa có cache (Stage 4 — Redis)
