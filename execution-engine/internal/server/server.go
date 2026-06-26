package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"execution-engine/internal/model"
	"execution-engine/internal/runner"
	pb "execution-engine/pb"

	goredis "github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

const stepTimeout = 30 * time.Second

var tracer = otel.Tracer("execution-engine")

// Server implements pb.ExecutionEngineServer.
type Server struct {
	pb.UnimplementedExecutionEngineServer
	db      *gorm.DB
	rdb     *goredis.Client
	factory *runner.RunnerFactory
}

func New(db *gorm.DB, rdb *goredis.Client, factory *runner.RunnerFactory) *Server {
	return &Server{db: db, rdb: rdb, factory: factory}
}

// RunExecution runs a workflow synchronously and returns when all steps finish.
func (s *Server) RunExecution(ctx context.Context, req *pb.RunExecutionReq) (*pb.RunExecutionResp, error) {
	ctx, span := tracer.Start(ctx, "execution.run",
		trace.WithAttributes(
			attribute.Int64("execution.id", req.ExecutionId),
			attribute.Int64("workflow.id", req.WorkflowId),
		))
	defer span.End()

	var wf model.Workflow
	if err := s.db.First(&wf, req.WorkflowId).Error; err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("load workflow %d: %w", req.WorkflowId, err)
	}

	var execution model.Execution
	if err := s.db.First(&execution, req.ExecutionId).Error; err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("load execution %d: %w", req.ExecutionId, err)
	}

	// Transition pending → running
	s.db.Model(&execution).Update("status", "running")

	status, errMsg := s.runSteps(ctx, &execution, &wf)

	span.SetAttributes(attribute.String("execution.status", status))
	if status == "failed" {
		span.SetStatus(codes.Error, errMsg)
	}

	return &pb.RunExecutionResp{Status: status, Error: errMsg}, nil
}

// StreamExecution runs a workflow and streams progress events back to the caller.
func (s *Server) StreamExecution(req *pb.RunExecutionReq, stream pb.ExecutionEngine_StreamExecutionServer) error {
	ctx := stream.Context()

	var wf model.Workflow
	if err := s.db.First(&wf, req.WorkflowId).Error; err != nil {
		return fmt.Errorf("load workflow %d: %w", req.WorkflowId, err)
	}

	var execution model.Execution
	if err := s.db.First(&execution, req.ExecutionId).Error; err != nil {
		return fmt.Errorf("load execution %d: %w", req.ExecutionId, err)
	}

	s.db.Model(&execution).Update("status", "running")

	var steps []runner.StepDef
	if err := json.Unmarshal(wf.Steps, &steps); err != nil || len(steps) == 0 {
		s.finishExecution(&execution, wf.ID, "success")
		return stream.Send(&pb.ExecutionEvent{EventType: "finished", Status: "success"})
	}

	input := map[string]any{}

	for _, stepDef := range steps {
		stepCtx, cancel := context.WithTimeout(ctx, stepTimeout)
		startedAt := time.Now()

		channel := fmt.Sprintf("executions:%d", wf.ID)
		r, factoryErr := s.factory.New(stepDef, channel)

		var (
			output     map[string]any
			status     = "success"
			stepErrMsg string
		)

		if factoryErr != nil {
			status = "failed"
			stepErrMsg = factoryErr.Error()
		} else {
			var runErr error
			output, runErr = r.Run(stepCtx, input)
			if runErr != nil {
				status = "failed"
				stepErrMsg = runErr.Error()
			}
		}
		cancel()

		durationMs := time.Since(startedAt).Milliseconds()
		s.writeStepLog(&execution, stepDef, status, stepErrMsg, input, output, durationMs)
		s.publishRedisEvent(wf.ID, execution.ID, "step_done", stepDef.ID, status, durationMs)

		_ = stream.Send(&pb.ExecutionEvent{
			EventType:  "step_done",
			StepId:     stepDef.ID,
			Status:     status,
			DurationMs: durationMs,
			Error:      stepErrMsg,
		})

		if status == "failed" {
			s.finishExecution(&execution, wf.ID, "failed")
			_ = stream.Send(&pb.ExecutionEvent{EventType: "finished", Status: "failed", Error: stepErrMsg})
			return nil
		}

		input = output
	}

	s.finishExecution(&execution, wf.ID, "success")
	return stream.Send(&pb.ExecutionEvent{EventType: "finished", Status: "success"})
}

// runSteps executes all steps sequentially (used by RunExecution).
// Each step gets its own child span so Jaeger shows per-step timing.
func (s *Server) runSteps(ctx context.Context, execution *model.Execution, wf *model.Workflow) (status string, errMsg string) {
	var steps []runner.StepDef
	if err := json.Unmarshal(wf.Steps, &steps); err != nil || len(steps) == 0 {
		s.finishExecution(execution, wf.ID, "success")
		return "success", ""
	}

	input := map[string]any{}

	channel := fmt.Sprintf("executions:%d", wf.ID)

	for _, stepDef := range steps {
		stepCtx, cancel := context.WithTimeout(ctx, stepTimeout)

		stepCtx, stepSpan := tracer.Start(stepCtx, "step."+stepDef.Type,
			trace.WithAttributes(
				attribute.String("step.id", stepDef.ID),
				attribute.String("step.type", stepDef.Type),
			))

		startedAt := time.Now()

		r, factoryErr := s.factory.New(stepDef, channel)

		var (
			output     map[string]any
			stepStatus = "success"
			stepErrMsg string
		)

		if factoryErr != nil {
			stepStatus = "failed"
			stepErrMsg = factoryErr.Error()
		} else {
			var runErr error
			output, runErr = r.Run(stepCtx, input)
			if runErr != nil {
				stepStatus = "failed"
				stepErrMsg = runErr.Error()
			}
		}

		durationMs := time.Since(startedAt).Milliseconds()

		stepSpan.SetAttributes(attribute.Int64("step.duration_ms", durationMs))
		if stepStatus == "failed" {
			stepSpan.SetStatus(codes.Error, stepErrMsg)
		}
		stepSpan.End()
		cancel()

		s.writeStepLog(execution, stepDef, stepStatus, stepErrMsg, input, output, durationMs)
		s.publishRedisEvent(wf.ID, execution.ID, "step_done", stepDef.ID, stepStatus, durationMs)

		if stepStatus == "failed" {
			s.finishExecution(execution, wf.ID, "failed")
			return "failed", stepErrMsg
		}

		input = output
	}

	s.finishExecution(execution, wf.ID, "success")
	return "success", ""
}

func (s *Server) writeStepLog(execution *model.Execution, step runner.StepDef, status, errMsg string, input, output map[string]any, durationMs int64) {
	inputJSON, _ := json.Marshal(input)
	outputJSON, _ := json.Marshal(output)
	s.db.Create(&model.StepLog{
		ExecutionID: execution.ID,
		StepID:      step.ID,
		StepType:    step.Type,
		Status:      status,
		Input:       datatypes.JSON(inputJSON),
		Output:      datatypes.JSON(outputJSON),
		Error:       errMsg,
		DurationMs:  durationMs,
	})
}

func (s *Server) finishExecution(execution *model.Execution, workflowID uint, status string) {
	now := time.Now()
	s.db.Model(execution).Updates(map[string]any{
		"status":      status,
		"finished_at": &now,
	})
	s.publishRedisEvent(workflowID, execution.ID, "finished", "", status, 0)
}

type redisEvent struct {
	Type        string `json:"type"`
	ExecutionID uint   `json:"executionId"`
	StepID      string `json:"stepId,omitempty"`
	Status      string `json:"status"`
	DurationMs  int64  `json:"durationMs,omitempty"`
}

func (s *Server) publishRedisEvent(workflowID, executionID uint, eventType, stepID, status string, durationMs int64) {
	channel := fmt.Sprintf("executions:%d", workflowID)
	data, _ := json.Marshal(redisEvent{
		Type:        eventType,
		ExecutionID: executionID,
		StepID:      stepID,
		Status:      status,
		DurationMs:  durationMs,
	})
	s.rdb.Publish(context.Background(), channel, string(data))
}
