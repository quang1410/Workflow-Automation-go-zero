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
	DB     *gorm.DB
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
