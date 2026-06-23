.PHONY: run build tidy playground health curl-workflows db-up db-down db-logs gen-api help

# Default target
help:
	@echo "Usage: make <target>"
	@echo ""
	@echo "Targets:"
	@echo "  run            Start the API gateway on port 8888"
	@echo "  build          Build the gateway binary"
	@echo "  playground     Run the playground step runner demo"
	@echo "  tidy           Tidy all Go modules"
	@echo "  health         Check /health endpoint"
	@echo "  curl-workflows Check /workflows endpoint"
	@echo "  db-up          Start PostgreSQL via docker-compose"
	@echo "  db-down        Stop PostgreSQL"
	@echo "  db-logs        Tail PostgreSQL logs"
	@echo "  gen-api        Regenerate handlers/types from gateway.api"
	@echo "  help           Show this help message"

run:
	go run api-gateway/gateway.go -f api-gateway/etc/gateway-api.yaml

build:
	cd api-gateway && go build -o gateway .

playground:
	go run playground/step_runner_demo.go

tidy:
	go work sync
	cd api-gateway && go mod tidy

health:
	curl -s http://localhost:8888/health | jq .

curl-workflows:
	curl -s http://localhost:8888/workflows | jq .

db-up:
	docker-compose up -d postgres

db-down:
	docker-compose down

db-logs:
	docker-compose logs -f postgres

gen-api:
	cd api-gateway && ~/go/bin/goctl api go -api gateway.api -dir .
