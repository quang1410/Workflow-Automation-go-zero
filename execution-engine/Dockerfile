FROM golang:1.25-alpine AS builder
WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o engine .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata netcat-openbsd
WORKDIR /app
COPY --from=builder /build/engine .
COPY etc/ etc/
EXPOSE 9090
HEALTHCHECK --interval=10s --timeout=5s --start-period=10s --retries=5 \
    CMD nc -z localhost 9090 || exit 1
CMD ["./engine", "-f", "etc/engine.docker.yaml"]
