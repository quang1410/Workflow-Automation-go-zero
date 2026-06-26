package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"execution-engine/internal/config"
	"execution-engine/internal/runner"
	"execution-engine/internal/server"
	pb "execution-engine/pb"

	goredis "github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc"
	"gopkg.in/yaml.v3"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var configFile = flag.String("f", "etc/engine.yaml", "config file path")

func main() {
	flag.Parse()

	data, err := os.ReadFile(*configFile)
	if err != nil {
		log.Fatalf("read config: %v", err)
	}
	var cfg config.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		log.Fatalf("parse config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Init OpenTelemetry — traces exported to Jaeger via OTLP gRPC.
	shutdownTracer, err := initTracer(ctx, cfg.JaegerEndpoint)
	if err != nil {
		log.Fatalf("init tracer: %v", err)
	}
	defer shutdownTracer(context.Background())

	db, err := gorm.Open(postgres.Open(cfg.DSN), &gorm.Config{})
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}

	rdb := goredis.NewClient(&goredis.Options{Addr: cfg.RedisAddr})

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		log.Fatalf("listen: %v", err)
	}

	// Environment variables take precedence over YAML config for API keys,
	// so docker-compose can inject secrets without embedding them in config files.
	if v := os.Getenv("ANTHROPIC_API_KEY"); v != "" {
		cfg.AnthropicAPIKey = v
	}
	if v := os.Getenv("OPENAI_API_KEY"); v != "" {
		cfg.OpenAIAPIKey = v
	}

	factory := &runner.RunnerFactory{
		AnthropicAPIKey: cfg.AnthropicAPIKey,
		OpenAIAPIKey:    cfg.OpenAIAPIKey,
		Rdb:             rdb,
	}

	grpcServer := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
	)
	pb.RegisterExecutionEngineServer(grpcServer, server.New(db, rdb, factory))

	go func() {
		<-ctx.Done()
		log.Println("shutting down execution-engine...")
		grpcServer.GracefulStop()
	}()

	log.Printf("execution-engine listening on :%d", cfg.Port)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

// initTracer sets up the global OTel TracerProvider with an OTLP gRPC exporter
// pointing at endpoint (host:port). Returns a shutdown function.
// If endpoint is empty, installs a no-op provider so traces are silently dropped.
func initTracer(ctx context.Context, endpoint string) (func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }
	if endpoint == "" {
		return noop, nil
	}

	exp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithEndpoint(endpoint),
	)
	if err != nil {
		return noop, fmt.Errorf("create OTLP exporter: %w", err)
	}

	res, _ := resource.New(ctx,
		resource.WithAttributes(attribute.String("service.name", "execution-engine")),
	)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return tp.Shutdown, nil
}
