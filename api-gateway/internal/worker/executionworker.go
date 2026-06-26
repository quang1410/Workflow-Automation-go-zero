package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	pb "api-gateway/internal/enginepb"
	"api-gateway/internal/queue"
	"api-gateway/model"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/zeromicro/go-zero/core/logx"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"gorm.io/gorm"
)

// Worker consumes execution jobs from RabbitMQ and dispatches them to the
// execution-engine gRPC service.
type Worker struct {
	amqpURL      string
	db           *gorm.DB
	engineClient pb.ExecutionEngineClient
	count        int
}

func New(amqpURL string, db *gorm.DB, engineClient pb.ExecutionEngineClient, count int) *Worker {
	return &Worker{amqpURL: amqpURL, db: db, engineClient: engineClient, count: count}
}

// Start dials RabbitMQ and spawns count consumer goroutines.
func (w *Worker) Start(ctx context.Context) error {
	conn, err := amqp.Dial(w.amqpURL)
	if err != nil {
		return fmt.Errorf("worker amqp dial: %w", err)
	}

	for i := range w.count {
		ch, err := conn.Channel()
		if err != nil {
			conn.Close()
			return fmt.Errorf("worker channel %d: %w", i, err)
		}
		if err := ch.Qos(1, 0, false); err != nil {
			ch.Close()
			conn.Close()
			return fmt.Errorf("worker qos %d: %w", i, err)
		}
		go w.consume(ctx, ch, i)
	}

	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	return nil
}

func (w *Worker) consume(ctx context.Context, ch *amqp.Channel, id int) {
	tag := fmt.Sprintf("worker-%d", id)
	msgs, err := ch.Consume(queue.QueueRun, tag, false, false, false, false, nil)
	if err != nil {
		logx.Errorf("[worker-%d] consume: %v", id, err)
		return
	}
	logx.Infof("[worker-%d] ready", id)

	for {
		select {
		case <-ctx.Done():
			ch.Close()
			return
		case d, ok := <-msgs:
			if !ok {
				logx.Infof("[worker-%d] channel closed", id)
				return
			}
			w.handle(ctx, d, id)
		}
	}
}

func (w *Worker) handle(ctx context.Context, d amqp.Delivery, id int) {
	if deathCount(d.Headers) >= queue.MaxRetries {
		logx.Errorf("[worker-%d] permanently failed after %d retries — dropping", id, queue.MaxRetries)
		d.Ack(false)
		return
	}

	var msg queue.ExecutionMessage
	if err := json.Unmarshal(d.Body, &msg); err != nil {
		logx.Errorf("[worker-%d] bad message body: %v", id, err)
		d.Ack(false)
		return
	}

	// Restore the trace started by the webhook handler and continue it here.
	ctx = otel.GetTextMapPropagator().Extract(ctx, queue.AMQPHeaderCarrier(d.Headers))
	ctx, span := otel.Tracer("api-gateway").Start(ctx, "worker.dispatch")
	defer span.End()
	span.SetAttributes(
		attribute.Int("execution.id", int(msg.ExecutionID)),
		attribute.Int("workflow.id", int(msg.WorkflowID)),
	)

	logx.Infof("[worker-%d] dispatching executionId=%d workflowId=%d to engine", id, msg.ExecutionID, msg.WorkflowID)

	// Verify the records exist before calling the engine to avoid unnecessary RPC
	var execution model.Execution
	if err := w.db.First(&execution, msg.ExecutionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			logx.Errorf("[worker-%d] execution %d not found — dropping", id, msg.ExecutionID)
			d.Ack(false)
			return
		}
		logx.Errorf("[worker-%d] db error loading execution: %v — retrying", id, err)
		d.Nack(false, false)
		return
	}

	resp, err := w.engineClient.RunExecution(ctx, &pb.RunExecutionReq{
		ExecutionId:    int64(msg.ExecutionID),
		WorkflowId:     int64(msg.WorkflowID),
		TriggerPayload: msg.TriggerPayload,
	})
	if err != nil {
		logx.Errorf("[worker-%d] engine.RunExecution failed: %v — retrying", id, err)
		d.Nack(false, false)
		return
	}

	logx.Infof("[worker-%d] executionId=%d finished status=%s", id, msg.ExecutionID, resp.Status)
	d.Ack(false)
}

func deathCount(headers amqp.Table) int64 {
	if headers == nil {
		return 0
	}
	deaths, ok := headers["x-death"].([]interface{})
	if !ok || len(deaths) == 0 {
		return 0
	}
	death, ok := deaths[0].(amqp.Table)
	if !ok {
		return 0
	}
	count, _ := death["count"].(int64)
	return count
}
