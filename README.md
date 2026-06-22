# Workflow Automation Engine

A workflow automation engine built with Go and Go-Zero — similar to n8n/Zapier. Users define workflows as a trigger + a sequence of steps; the engine executes them automatically when the trigger fires.

Built as a **10-stage progressive learning project**, each stage introducing a new layer of the Go ecosystem.

---

## Concept

```
Workflow = Trigger + [Step1 → Step2 → Step3 ...]

Trigger types:   webhook · schedule (cron) · manual
Step types:      http_request · transform · delay · condition · send_email · ai_task
```

**Workflow definition (stored as JSON in PostgreSQL):**

```json
{
  "id": "wf_abc123",
  "name": "Notify on new user",
  "trigger": { "type": "webhook" },
  "steps": [
    { "id": "step1", "type": "http_request", "config": { "url": "https://api.example.com/notify", "method": "POST" } },
    { "id": "step2", "type": "send_email",   "config": { "to": "admin@example.com", "subject": "New user: {{step1.body.name}}" } }
  ]
}
```

---

## Architecture

```
Client
  └── API Gateway (Go-Zero HTTP, port 8888)
        ├── Workflow CRUD API
        ├── POST /webhooks/:id  →  trigger execution
        └── Execution log API
                    │
              RabbitMQ (async queue)
                    │
        Execution Engine (Go-Zero RPC)
              └── Step Runner (goroutine pool per step)
                    ├── http_request · transform · delay
                    ├── condition · send_email · ai_task
                    └── context timeout per step

  PostgreSQL  — workflows, executions, step_logs
  Redis       — workflow definition cache, pub/sub (SSE)
  Jaeger      — distributed tracing per execution
```

---

## Quickstart (Stage 1)

```bash
# Start the API gateway
go run api-gateway/gateway.go -f api-gateway/etc/gateway-api.yaml

# Test endpoints
curl http://localhost:8888/health
curl http://localhost:8888/workflows
curl -X POST http://localhost:8888/webhooks/1
```

---

## Roadmap

| Stage | Focus | Key tech |
|-------|-------|----------|
| ✅ 1 | Go basics + REST API | Go-Zero, goroutine, channel, context |
| 2 | Database layer | PostgreSQL, GORM, JSONB |
| 3 | Execution engine | Interface pattern, worker pool |
| 4 | Real-time events | Redis cache, pub/sub, SSE |
| 5 | Authentication | Keycloak, JWT, JWKS |
| 6 | Async execution | RabbitMQ, dead-letter queue |
| 7 | Microservice split | gRPC, Protobuf, etcd discovery |
| 8 | Containerisation | Docker, docker-compose, Makefile |
| 9 | Observability | OpenTelemetry, Jaeger, Prometheus |
| 10 | AI integration | Claude API, OpenAI, streaming |

Full plan: [docs/plan-to-learn.md](docs/plan-to-learn.md)
