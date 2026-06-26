package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"scheduler-service/cron"

	"gopkg.in/yaml.v3"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type Config struct {
	DSN         string `yaml:"DSN"`
	RabbitMQURL string `yaml:"RabbitMQURL"`
}

var configFile = flag.String("f", "etc/scheduler.yaml", "config file path")

func main() {
	flag.Parse()

	data, err := os.ReadFile(*configFile)
	if err != nil {
		log.Fatalf("read config: %v", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		log.Fatalf("parse config: %v", err)
	}

	db, err := gorm.Open(postgres.Open(cfg.DSN), &gorm.Config{})
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}

	scheduler, err := cron.New(db, cfg.RabbitMQURL)
	if err != nil {
		log.Fatalf("init scheduler: %v", err)
	}
	defer scheduler.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	scheduler.Run(ctx)
	log.Println("scheduler stopped")
}
