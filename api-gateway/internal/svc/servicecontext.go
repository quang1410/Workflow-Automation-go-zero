package svc

import (
	"api-gateway/internal/config"
	pb "api-gateway/internal/enginepb"
	"api-gateway/internal/queue"
	"api-gateway/model"
	"log"

	goredis "github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type ServiceContext struct {
	Config       config.Config
	DB           *gorm.DB
	Redis        *goredis.Client
	Queue        *queue.Publisher
	EngineClient pb.ExecutionEngineClient
}

func NewServiceContext(c config.Config) *ServiceContext {
	db, err := gorm.Open(postgres.Open(c.DB.DSN), &gorm.Config{})
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}

	if err := db.AutoMigrate(&model.Workflow{}, &model.Execution{}, &model.StepLog{}); err != nil {
		log.Fatalf("failed to auto migrate: %v", err)
	}

	rdb := goredis.NewClient(&goredis.Options{Addr: c.Redis.Addr})

	pub, err := queue.New(c.RabbitMQ.URL)
	if err != nil {
		log.Fatalf("failed to connect to RabbitMQ: %v", err)
	}

	conn, err := grpc.NewClient(c.Engine.Addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
	)
	if err != nil {
		log.Fatalf("failed to connect to execution-engine: %v", err)
	}

	return &ServiceContext{
		Config:       c,
		DB:           db,
		Redis:        rdb,
		Queue:        pub,
		EngineClient: pb.NewExecutionEngineClient(conn),
	}
}
