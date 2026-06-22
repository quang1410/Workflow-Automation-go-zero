# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# Run the API gateway (port 8888)
go run api-gateway/gateway.go -f api-gateway/etc/gateway-api.yaml

# Run playground exercises
go run playground/step_runner_demo.go

# Build the binary
cd api-gateway && go build -o gateway

# Regenerate handler/types from API definition (requires goctl)
goctl api go -api api-gateway/gateway.api -dir api-gateway

# Test endpoints
curl http://localhost:8888/health
curl http://localhost:8888/workflows
curl -X POST http://localhost:8888/webhooks/1
```

## Architecture

This is a **10-stage progressive learning project** building a workflow automation engine (similar to n8n/Zapier) in Go. Stage 1 is complete — later stages add PostgreSQL, Redis, RabbitMQ, gRPC, Docker, OpenTelemetry, and AI integration. See [docs/plan-to-learn.md](docs/plan-to-learn.md) for the full roadmap.

### Current structure

```
api-gateway/      # Go-Zero REST service (port 8888) — Stage 1
  gateway.api     # API definition (goctl source of truth for handlers/types)
  etc/gateway-api.yaml  # Runtime config (host, port)
  internal/
    config/       # Config struct (extends rest.RestConf)
    handler/      # HTTP layer — parse request, call logic, write response
    logic/        # Business logic — one file per endpoint
    svc/          # ServiceContext: dependency injection container
    types/        # Request/response structs (auto-generated from .api)
playground/       # Standalone Go exercises (goroutines, channels, interfaces)
```

### Go-Zero pattern

Every endpoint follows: **Handler → Logic → ServiceContext**

- `handler/` parses HTTP request via `httpx.Parse`, instantiates a Logic, calls it, writes response via `httpx.OkJsonCtx`
- `logic/` holds all business logic; receives `*svc.ServiceContext` for shared dependencies
- `svc/ServiceContext` is the DI root — add DB, cache, queue clients here as stages progress
- `types/types.go` and `handler/routes.go` are **auto-generated** by `goctl` from `gateway.api` — edit the `.api` file, not these directly

### Planned service topology (Stages 2–10)

```
API Gateway (go-zero HTTP)
    ├── PostgreSQL via GORM          (Stage 2)
    ├── Execution engine             (Stage 3)
    ├── Redis cache + SSE pub/sub    (Stage 4)
    ├── Keycloak/JWT auth middleware (Stage 5)
    └── RabbitMQ producer            (Stage 6)
            ↓
    Execution Engine (go-zero RPC/gRPC, Stage 7)
            ├── Step runners: HTTP, Transform, Delay, Condition, Email, AI
            └── Goroutine worker pool with context timeout per step
```

### Go workspace

`go.work` ties together multiple modules. When new services are added (e.g., an RPC service in Stage 7), add them with `go work use ./service-name`.
