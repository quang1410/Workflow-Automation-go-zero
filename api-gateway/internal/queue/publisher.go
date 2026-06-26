package queue

import (
	"context"
	"encoding/json"
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
)

// AMQPHeaderCarrier adapts amqp.Table to the OTel TextMapCarrier interface so
// W3C traceparent/tracestate headers can be injected into and extracted from
// RabbitMQ message headers.
type AMQPHeaderCarrier amqp.Table

func (c AMQPHeaderCarrier) Get(key string) string {
	v, ok := amqp.Table(c)[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

func (c AMQPHeaderCarrier) Set(key, val string) {
	amqp.Table(c)[key] = val
}

func (c AMQPHeaderCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range amqp.Table(c) {
		keys = append(keys, k)
	}
	return keys
}

const (
	Exchange      = "workflows"
	DLX           = "workflows.dlx"
	QueueRun      = "execution.run"
	QueueRetry    = "execution.retry"
	RoutingKeyRun = "execution.run"

	retryTTLMs = 10_000 // 10-second delay before a failed message is re-queued
	MaxRetries = 3      // drop permanently after this many deaths
)

// ExecutionMessage is the payload published when a webhook trigger fires.
type ExecutionMessage struct {
	ExecutionID    uint   `json:"executionId"`
	WorkflowID     uint   `json:"workflowId"`
	TriggerPayload string `json:"triggerPayload"`
}

// Publisher enqueues execution jobs. One connection + one channel is enough
// for the publish path; each worker goroutine opens its own channel to consume.
type Publisher struct {
	conn *amqp.Connection
	ch   *amqp.Channel
}

// New dials RabbitMQ, declares the full topology, and returns a ready Publisher.
func New(url string) (*Publisher, error) {
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, fmt.Errorf("amqp dial: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("amqp channel: %w", err)
	}
	p := &Publisher{conn: conn, ch: ch}
	if err := p.declareTopology(); err != nil {
		p.Close()
		return nil, fmt.Errorf("declare topology: %w", err)
	}
	return p, nil
}

// Publish enqueues an execution message as a durable, persistent delivery.
// W3C traceparent/tracestate are injected into the AMQP headers so the
// downstream worker can continue the same trace.
func (p *Publisher) Publish(ctx context.Context, msg ExecutionMessage) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	headers := amqp.Table{}
	otel.GetTextMapPropagator().Inject(ctx, AMQPHeaderCarrier(headers))
	return p.ch.PublishWithContext(ctx, Exchange, RoutingKeyRun, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         body,
		Headers:      headers,
	})
}

// Conn exposes the underlying connection so workers can open their own channels.
func (p *Publisher) Conn() *amqp.Connection {
	return p.conn
}

func (p *Publisher) Close() {
	if p.ch != nil {
		p.ch.Close()
	}
	if p.conn != nil {
		p.conn.Close()
	}
}

// declareTopology sets up the retry/DLQ pattern:
//
//	execution.run ──(nack)──► workflows.dlx ──► execution.retry
//	                                                   │ TTL 10s
//	execution.run ◄──────────────────────────────────┘
//
// After MaxRetries deaths the worker acks (drops) the message permanently.
func (p *Publisher) declareTopology() error {
	// Main exchange — topic type allows future routing-key expansions
	if err := p.ch.ExchangeDeclare(Exchange, "topic", true, false, false, false, nil); err != nil {
		return fmt.Errorf("exchange %q: %w", Exchange, err)
	}
	// Dead-letter exchange — direct for simple DLQ routing
	if err := p.ch.ExchangeDeclare(DLX, "direct", true, false, false, false, nil); err != nil {
		return fmt.Errorf("exchange %q: %w", DLX, err)
	}

	// Main queue: rejected messages go to DLX (which feeds retry queue)
	_, err := p.ch.QueueDeclare(QueueRun, true, false, false, false, amqp.Table{
		"x-dead-letter-exchange":    DLX,
		"x-dead-letter-routing-key": RoutingKeyRun,
	})
	if err != nil {
		return fmt.Errorf("queue %q: %w", QueueRun, err)
	}
	if err := p.ch.QueueBind(QueueRun, RoutingKeyRun, Exchange, false, nil); err != nil {
		return fmt.Errorf("bind %q: %w", QueueRun, err)
	}

	// Retry queue: holds rejected messages for TTL then routes back to main exchange
	_, err = p.ch.QueueDeclare(QueueRetry, true, false, false, false, amqp.Table{
		"x-message-ttl":             int32(retryTTLMs),
		"x-dead-letter-exchange":    Exchange,
		"x-dead-letter-routing-key": RoutingKeyRun,
	})
	if err != nil {
		return fmt.Errorf("queue %q: %w", QueueRetry, err)
	}
	if err := p.ch.QueueBind(QueueRetry, RoutingKeyRun, DLX, false, nil); err != nil {
		return fmt.Errorf("bind %q: %w", QueueRetry, err)
	}

	return nil
}
