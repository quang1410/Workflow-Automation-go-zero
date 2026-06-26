# Stage 8 — Docker: Containerization

> **Mục tiêu**: Đóng gói cả 3 services thành Docker images. Chạy toàn bộ hệ thống bằng `docker-compose up` duy nhất — không cần `go run` thủ công hay cài Go trên máy chạy.

---

## Vấn đề của Stage 7

Ở Stage 7, để chạy hệ thống cần mở 3+ terminal và khởi động đúng thứ tự thủ công:

```
Terminal 1: make db-up && make mq-up && make etcd-up
Terminal 2: make engine
Terminal 3: make run
Terminal 4: make scheduler
```

Nếu execution-engine chưa kịp lắng nghe thì api-gateway lỗi. Không có cách kiểm soát startup order.

---

## Giải pháp: Multi-stage Dockerfile + depends_on health checks

```
docker-compose up --build
       │
       ├── Build images (golang:1.25-alpine builder → alpine:3.21 runtime)
       │
       ├── Start infrastructure (với healthcheck)
       │   ├── postgres  ──→ healthy: pg_isready
       │   ├── redis     ──→ healthy: redis-cli ping
       │   ├── rabbitmq  ──→ healthy: rabbitmq-diagnostics ping
       │   └── etcd      ──→ healthy: etcdctl endpoint health
       │
       ├── Start execution-engine (depends: postgres + redis healthy)
       │   └── healthy: nc -z localhost 9090
       │
       ├── Start api-gateway (depends: postgres + redis + rabbitmq + execution-engine healthy)
       │
       └── Start scheduler-service (depends: postgres + rabbitmq healthy)
```

docker-compose đảm bảo đúng startup order. Không cần thứ tự thủ công.

---

## Multi-stage Dockerfile

Cả 3 services dùng cùng pattern. Ví dụ [api-gateway/Dockerfile](../api-gateway/Dockerfile):

```dockerfile
# Stage 1: Builder — có Go compiler, build tools
FROM golang:1.25-alpine AS builder
WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download           # Cache dependencies trước

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o gateway .

# Stage 2: Runtime — chỉ có binary + config, không có Go compiler
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata wget
WORKDIR /app
COPY --from=builder /build/gateway .
COPY etc/ etc/
EXPOSE 8888
HEALTHCHECK --interval=10s CMD wget -qO- http://localhost:8888/health || exit 1
CMD ["./gateway", "-f", "etc/gateway-api.docker.yaml"]
```

### Tại sao multi-stage?

| Stage | Image size | Có gì |
|---|---|---|
| Builder (`golang:1.25-alpine`) | ~300MB | Go compiler, build tools, source code |
| Runtime (`alpine:3.21`) | ~15MB | Binary + config + TLS certs |

**Final image < 20MB** so với ~300MB nếu dùng builder image trực tiếp.

### `-ldflags="-s -w"`

- `-s`: strip symbol table (debug info)
- `-w`: strip DWARF debug info
- Giảm binary size thêm ~30%

### `CGO_ENABLED=0`

Tắt CGo → binary **statically linked** → chạy được trên bất kỳ linux image nào kể cả `scratch` (image trống hoàn toàn). Nếu để mặc định, binary phụ thuộc vào `libc` của builder image — sẽ lỗi khi chạy trên alpine.

---

## Config cho Docker vs Local

Vấn đề: trong Docker, các services nói chuyện với nhau qua **tên service** (Docker DNS), không phải `localhost`.

| Service | Local | Docker |
|---|---|---|
| PostgreSQL | `localhost:5432` | `postgres:5432` |
| Redis | `localhost:6379` | `redis:6379` |
| RabbitMQ | `localhost:5672` | `rabbitmq:5672` |
| execution-engine | `localhost:9090` | `execution-engine:9090` |
| Keycloak | `localhost:8080` | `keycloak:8080` |

Giải pháp: mỗi service có 2 config file:
- `etc/service.yaml` — local dev (giữ nguyên)
- `etc/service.docker.yaml` — dùng trong container (Docker service names)

Dockerfile copy cả 2 vào image:
```dockerfile
COPY etc/ etc/
```

CMD chỉ định file docker:
```dockerfile
CMD ["./gateway", "-f", "etc/gateway-api.docker.yaml"]
```

---

## Health checks và depends_on

### Tại sao cần?

Nếu api-gateway start khi rabbitmq chưa sẵn sàng → `amqp.Dial()` fail → crash.

Giải pháp: `condition: service_healthy` trong `depends_on`:

```yaml
api-gateway:
  depends_on:
    rabbitmq:
      condition: service_healthy  # chờ đến khi healthcheck pass
```

docker-compose sẽ không start `api-gateway` cho đến khi `rabbitmq` healthcheck trả về 0.

### Health checks per service

```yaml
# postgres — pg_isready kiểm tra database connection
healthcheck:
  test: ["CMD-SHELL", "pg_isready -U workflow -d workflow_db"]

# redis
healthcheck:
  test: ["CMD", "redis-cli", "ping"]

# rabbitmq
healthcheck:
  test: ["CMD", "rabbitmq-diagnostics", "-q", "ping"]
  start_period: 30s   # RabbitMQ cần ~20s để boot

# execution-engine (gRPC) — kiểm tra TCP port
healthcheck:
  test: ["CMD", "nc", "-z", "localhost", "9090"]
  start_period: 10s

# api-gateway (HTTP)
HEALTHCHECK --interval=10s CMD wget -qO- http://localhost:8888/health || exit 1
```

### execution-engine dùng `netcat-openbsd`

gRPC không có HTTP endpoint để wget/curl kiểm tra. Giải pháp: kiểm tra TCP port mở bằng `nc -z`:
- `-z`: scan mode — kết nối và đóng ngay, không gửi data
- Cần `netcat-openbsd` (busybox nc không có `-z`)

```dockerfile
RUN apk add --no-cache ca-certificates tzdata netcat-openbsd
```

---

## Cách chạy

### Full stack (Docker)

```bash
make up
# Lần đầu: build images (~2-3 phút)
# Sau đó: docker-compose up -d --build

# Theo dõi logs
make logs

# Dừng
make down
```

### Build images trước (không start)

```bash
make build-images
# Để test build không lỗi
```

### Chỉ start infra (local dev)

```bash
make infra-up
# Starts: postgres, redis, rabbitmq, etcd
# Rồi chạy services thủ công:
make engine    # terminal 1
make run       # terminal 2
make scheduler # terminal 3
```

---

## Startup flow đầy đủ

```
make up
  │
  ├─ docker-compose up -d --build
  │
  ├─ Build phase:
  │   execution-engine: go build → /app/engine (10MB)
  │   api-gateway:      go build → /app/gateway (20MB)
  │   scheduler-service: go build → /app/scheduler (8MB)
  │
  ├─ Start phase (parallel where possible):
  │   postgres  ──→ healthy (5s)
  │   redis     ──→ healthy (2s)
  │   rabbitmq  ──→ healthy (30s) ← bottleneck
  │   etcd      ──→ healthy (15s)
  │   keycloak  ──→ healthy (60s) ← slow, optional
  │
  ├─ execution-engine starts (postgres + redis healthy)
  │   └── healthy (nc -z :9090)
  │
  ├─ api-gateway starts (postgres + redis + rabbitmq + execution-engine healthy)
  │
  └─ scheduler-service starts (postgres + rabbitmq healthy)
```

Total cold start: ~45s (limited by rabbitmq).

---

## Test sau khi up

```bash
# Health check
curl http://localhost:8888/health
# {"status":"ok","version":"1.0.0"}

# Xem logs api-gateway
docker-compose logs api-gateway

# Xem logs execution-engine
docker-compose logs execution-engine

# Trigger webhook (không cần token vì không có auth trên webhook endpoint)
curl -X POST http://localhost:8888/webhooks/1 \
  -H "Content-Type: application/json" \
  -d '{"data":"hello from docker"}'

# Check execution status
curl -H "Authorization: Bearer $TOKEN" http://localhost:8888/executions/1
```

---

## Layer caching — tại sao tách `COPY go.mod go.sum` riêng

```dockerfile
COPY go.mod go.sum ./
RUN go mod download   # layer này cache lại

COPY . .              # chỉ invalidate khi source code thay đổi
RUN go build ...
```

Nếu chỉ thay đổi source code (không thêm dependency):
- Layer `go mod download` được cache → skip
- Chỉ rebuild layer `go build`
- **Tiết kiệm 30-60s** mỗi lần build

Nếu thêm dependency mới (thay đổi `go.mod`):
- Layer `go mod download` invalidate → re-download
- Rebuild từ đầu

---

## Các files mới

| File | Mô tả |
|---|---|
| [api-gateway/Dockerfile](../api-gateway/Dockerfile) | Multi-stage build cho HTTP gateway |
| [execution-engine/Dockerfile](../execution-engine/Dockerfile) | Multi-stage build cho gRPC service |
| [scheduler-service/Dockerfile](../scheduler-service/Dockerfile) | Multi-stage build cho cron service |
| [api-gateway/etc/gateway-api.docker.yaml](../api-gateway/etc/gateway-api.docker.yaml) | Config dùng Docker service names |
| [execution-engine/etc/engine.docker.yaml](../execution-engine/etc/engine.docker.yaml) | Config dùng Docker service names |
| [scheduler-service/etc/scheduler.docker.yaml](../scheduler-service/etc/scheduler.docker.yaml) | Config dùng Docker service names |

---

## Tóm tắt kiến thức đã học

| Khái niệm | Áp dụng |
|---|---|
| Multi-stage build | Builder image riêng → runtime image nhỏ (~15MB) |
| `CGO_ENABLED=0` | Static binary — chạy được trên bất kỳ linux image |
| `-ldflags="-s -w"` | Strip debug symbols → binary nhỏ hơn |
| Layer caching | `COPY go.mod` + `go mod download` trước source code |
| Docker service DNS | Các service giao tiếp qua tên service, không phải `localhost` |
| `depends_on condition: service_healthy` | Startup ordering dựa vào health checks |
| `netcat-openbsd` | TCP health check cho gRPC service (không có HTTP endpoint) |
| `restart: on-failure` | Tự restart nếu crash do startup race condition |

---

## Cái gì chưa làm (Stage 9+)

- Stage 9: OpenTelemetry + Jaeger — distributed tracing qua HTTP/gRPC/RabbitMQ
- `.dockerignore` files cho từng service (tối ưu build context)
- Multi-platform builds (`docker buildx` cho ARM/x86)
- Production hardening: non-root user, read-only filesystem
