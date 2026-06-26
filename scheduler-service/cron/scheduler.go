package cron

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/robfig/cron/v3"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// RabbitMQ topology constants — must match api-gateway/internal/queue/publisher.go
const (
	exchange      = "workflows"
	dlx           = "workflows.dlx"
	queueRun      = "execution.run"
	queueRetry    = "execution.retry"
	routingKeyRun = "execution.run"
	retryTTLMs    = 10_000
)

// TriggerConfig is the JSON stored in Workflow.TriggerConfig for schedule workflows.
type TriggerConfig struct {
	Cron string `json:"cron"`
}

// ExecutionMessage mirrors the struct in api-gateway/internal/queue.
type ExecutionMessage struct {
	ExecutionID    uint   `json:"executionId"`
	WorkflowID     uint   `json:"workflowId"`
	TriggerPayload string `json:"triggerPayload"`
}

// Workflow is a minimal projection of the api-gateway model (no import cycle).
type Workflow struct {
	ID            uint
	TriggerType   string
	TriggerConfig datatypes.JSON
	IsActive      bool
	DeletedAt     gorm.DeletedAt
}

// Execution mirrors the DB table for inserting pending records.
type Execution struct {
	ID             uint `gorm:"primarykey"`
	WorkflowID     uint
	Status         string
	StartedAt      time.Time
	TriggerPayload datatypes.JSON
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Scheduler polls the DB every minute and publishes execution messages for
// any schedule-triggered workflow whose cron expression fires in that window.
type Scheduler struct {
	db      *gorm.DB
	amqpURL string
	conn    *amqp.Connection
	ch      *amqp.Channel
}

func New(db *gorm.DB, amqpURL string) (*Scheduler, error) {
	conn, err := amqp.Dial(amqpURL)
	if err != nil {
		return nil, fmt.Errorf("amqp dial: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("amqp channel: %w", err)
	}
	s := &Scheduler{db: db, amqpURL: amqpURL, conn: conn, ch: ch}
	if err := s.declareTopology(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// Run starts a goroutine that fires every minute and blocks until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	log.Println("[scheduler] started — checking every minute")

	// Fire once immediately on startup, then tick every minute.
	s.checkAndTrigger()

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[scheduler] stopping")
			return
		case <-ticker.C:
			s.checkAndTrigger()
		}
	}
}

func (s *Scheduler) checkAndTrigger() {
	var workflows []Workflow
	if err := s.db.Table("workflows").
		Where("trigger_type = ? AND is_active = ? AND deleted_at IS NULL", "schedule", true).
		Find(&workflows).Error; err != nil {
		log.Printf("[scheduler] db error: %v", err)
		return
	}

	now := time.Now().Truncate(time.Minute)
	minuteAgo := now.Add(-time.Minute)

	for _, wf := range workflows {
		var cfg TriggerConfig
		if err := json.Unmarshal(wf.TriggerConfig, &cfg); err != nil || cfg.Cron == "" {
			continue
		}

		sched, err := cron.ParseStandard(cfg.Cron)
		if err != nil {
			log.Printf("[scheduler] workflow %d: bad cron %q: %v", wf.ID, cfg.Cron, err)
			continue
		}

		// Does this cron expression fire within [minuteAgo, now)?
		next := sched.Next(minuteAgo)
		if next.Before(now.Add(time.Second)) {
			s.trigger(wf)
		}
	}
}

func (s *Scheduler) trigger(wf Workflow) {
	execution := Execution{
		WorkflowID:     wf.ID,
		Status:         "pending",
		StartedAt:      time.Now(),
		TriggerPayload: datatypes.JSON(`{"source":"schedule"}`),
	}
	if err := s.db.Table("executions").Create(&execution).Error; err != nil {
		log.Printf("[scheduler] create execution for workflow %d: %v", wf.ID, err)
		return
	}

	msg := ExecutionMessage{
		ExecutionID:    execution.ID,
		WorkflowID:     wf.ID,
		TriggerPayload: `{"source":"schedule"}`,
	}
	body, _ := json.Marshal(msg)
	err := s.ch.Publish(exchange, routingKeyRun, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         body,
	})
	if err != nil {
		log.Printf("[scheduler] publish workflow %d: %v", wf.ID, err)
		return
	}
	log.Printf("[scheduler] triggered workflow %d → executionId %d", wf.ID, execution.ID)
}

func (s *Scheduler) declareTopology() error {
	if err := s.ch.ExchangeDeclare(exchange, "topic", true, false, false, false, nil); err != nil {
		return fmt.Errorf("exchange %q: %w", exchange, err)
	}
	if err := s.ch.ExchangeDeclare(dlx, "direct", true, false, false, false, nil); err != nil {
		return fmt.Errorf("exchange %q: %w", dlx, err)
	}
	_, err := s.ch.QueueDeclare(queueRun, true, false, false, false, amqp.Table{
		"x-dead-letter-exchange":    dlx,
		"x-dead-letter-routing-key": routingKeyRun,
	})
	if err != nil {
		return fmt.Errorf("queue %q: %w", queueRun, err)
	}
	if err := s.ch.QueueBind(queueRun, routingKeyRun, exchange, false, nil); err != nil {
		return fmt.Errorf("bind %q: %w", queueRun, err)
	}
	_, err = s.ch.QueueDeclare(queueRetry, true, false, false, false, amqp.Table{
		"x-message-ttl":             int32(retryTTLMs),
		"x-dead-letter-exchange":    exchange,
		"x-dead-letter-routing-key": routingKeyRun,
	})
	if err != nil {
		return fmt.Errorf("queue %q: %w", queueRetry, err)
	}
	if err := s.ch.QueueBind(queueRetry, routingKeyRun, dlx, false, nil); err != nil {
		return fmt.Errorf("bind %q: %w", queueRetry, err)
	}
	return nil
}

func (s *Scheduler) Close() {
	if s.ch != nil {
		s.ch.Close()
	}
	if s.conn != nil {
		s.conn.Close()
	}
}
