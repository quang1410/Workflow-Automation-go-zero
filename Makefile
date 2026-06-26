.PHONY: help dev run build playground tidy gen-api db-up db-down db-logs db-psql keycloak-up keycloak-token mq-up scheduler etcd-up engine gen-rpc up down logs build-images infra-up

help:
	@echo "=== Docker (Stage 8) ==="
	@echo "  up            Build images and start all services via docker-compose"
	@echo "  down          Stop and remove all containers"
	@echo "  logs          Follow logs for all services"
	@echo "  build-images  Build Docker images without starting containers"
	@echo "  infra-up      Start only infrastructure (postgres, redis, rabbitmq, etcd)"
	@echo ""
	@echo "=== Local dev (no Docker) ==="
	@echo "  run           Start API gateway (port 8888)"
	@echo "  engine        Run execution-engine gRPC service (port 9090)"
	@echo "  scheduler     Run scheduler-service (cron triggers)"
	@echo "  build         Build gateway binary"
	@echo "  playground    Run playground step runner demo"
	@echo ""
	@echo "=== Infrastructure ==="
	@echo "  db-up         Start PostgreSQL + Redis"
	@echo "  mq-up         Start RabbitMQ (Management UI: http://localhost:15672)"
	@echo "  keycloak-up   Start Keycloak (slow ~60s startup)"
	@echo "  etcd-up       Start etcd (service registry)"
	@echo "  db-down       Stop all containers"
	@echo "  db-logs       Tail PostgreSQL logs"
	@echo "  db-psql       Open psql shell"
	@echo ""
	@echo "=== Codegen ==="
	@echo "  tidy          Sync go.work and tidy modules"
	@echo "  gen-api       Regenerate handlers/types from gateway.api"
	@echo "  gen-rpc       Regenerate gRPC code from engine.proto"
	@echo "  keycloak-token  Get token for alice (user role)"

up:
	docker-compose up -d --build

down:
	docker-compose down

logs:
	docker-compose logs -f

build-images:
	docker-compose build

infra-up:
	docker-compose up -d --wait postgres redis rabbitmq etcd

dev: db-up run

run:
	go run api-gateway/gateway.go -f api-gateway/etc/gateway-api.yaml

build:
	cd api-gateway && go build -o gateway .

playground:
	go run playground/step_runner_demo.go

tidy:
	go work sync
	cd api-gateway && go mod tidy

gen-api:
	cd api-gateway && ~/go/bin/goctl api go -api gateway.api -dir .

db-up:
	docker-compose up -d --wait postgres redis

mq-up:
	docker-compose up -d --wait rabbitmq

scheduler:
	go run scheduler-service/main.go -f scheduler-service/etc/scheduler.yaml

etcd-up:
	docker-compose up -d --wait etcd

engine:
	go run execution-engine/main.go -f execution-engine/etc/engine.yaml

gen-rpc:
	@export PATH="$$PATH:$$HOME/go/bin" && \
	protoc \
	  --proto_path=execution-engine/pb \
	  --go_out=execution-engine --go_opt=module=execution-engine \
	  --go-grpc_out=execution-engine --go-grpc_opt=module=execution-engine \
	  engine.proto && \
	protoc \
	  --proto_path=execution-engine/pb \
	  --go_out=api-gateway/internal/enginepb --go_opt=paths=source_relative \
	  --go-grpc_out=api-gateway/internal/enginepb --go-grpc_opt=paths=source_relative \
	  engine.proto

keycloak-up:
	docker-compose up -d --wait keycloak

keycloak-token:
	@curl -s -X POST http://localhost:8080/realms/workflow-app/protocol/openid-connect/token \
		-d "grant_type=password&client_id=workflow-client&username=alice&password=alice123" \
		| python3 -m json.tool

db-down:
	docker-compose down

db-logs:
	docker-compose logs -f postgres

db-psql:
	docker exec -it $$(docker-compose ps -q postgres) psql -U workflow -d workflow_db
