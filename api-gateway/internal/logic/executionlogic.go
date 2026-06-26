package logic

import (
	"context"
	"errors"
	"fmt"
	"time"

	"api-gateway/internal/queue"
	"api-gateway/internal/svc"
	"api-gateway/internal/types"
	"api-gateway/model"

	"github.com/zeromicro/go-zero/core/logx"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// --- Trigger ---

type TriggerWorkflowLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewTriggerWorkflowLogic(ctx context.Context, svcCtx *svc.ServiceContext) *TriggerWorkflowLogic {
	return &TriggerWorkflowLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *TriggerWorkflowLogic) TriggerWorkflow(req *types.TriggerReq) (*types.TriggerResp, error) {
	var wf model.Workflow
	if err := l.svcCtx.DB.First(&wf, req.WorkflowId).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("workflow not found")
		}
		return nil, err
	}

	payload := req.Payload
	if payload == "" {
		payload = "{}"
	}

	// Create execution as "pending" — the worker will transition it to "running"
	// once it dequeues and starts processing.
	execution := model.Execution{
		WorkflowID:     uint(req.WorkflowId),
		Status:         "pending",
		StartedAt:      time.Now(),
		TriggerPayload: datatypes.JSON(payload),
	}
	if err := l.svcCtx.DB.Create(&execution).Error; err != nil {
		return nil, err
	}

	// Publish to RabbitMQ and return immediately — execution runs asynchronously.
	msg := queue.ExecutionMessage{
		ExecutionID:    execution.ID,
		WorkflowID:     uint(req.WorkflowId),
		TriggerPayload: payload,
	}
	if err := l.svcCtx.Queue.Publish(l.ctx, msg); err != nil {
		return nil, fmt.Errorf("enqueue execution: %w", err)
	}

	return &types.TriggerResp{
		Message:     "execution queued",
		ExecutionId: int64(execution.ID),
	}, nil
}

// --- List Executions ---

type ListExecutionsLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewListExecutionsLogic(ctx context.Context, svcCtx *svc.ServiceContext) *ListExecutionsLogic {
	return &ListExecutionsLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *ListExecutionsLogic) ListExecutions(req *types.ListExecutionsReq) (*types.ListExecutionsResp, error) {
	var executions []model.Execution
	if err := l.svcCtx.DB.Where("workflow_id = ?", req.WorkflowId).Find(&executions).Error; err != nil {
		return nil, err
	}
	items := make([]types.ExecutionItem, len(executions))
	for i, ex := range executions {
		items[i] = types.ExecutionItem{
			Id:        int64(ex.ID),
			Status:    ex.Status,
			StartedAt: ex.StartedAt.Format("2006-01-02T15:04:05Z07:00"),
		}
	}
	return &types.ListExecutionsResp{Items: items, Total: int64(len(items))}, nil
}

// --- Get Execution ---

type GetExecutionLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewGetExecutionLogic(ctx context.Context, svcCtx *svc.ServiceContext) *GetExecutionLogic {
	return &GetExecutionLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *GetExecutionLogic) GetExecution(req *types.GetExecutionReq) (*types.ExecutionItem, error) {
	var ex model.Execution
	if err := l.svcCtx.DB.Preload("StepLogs").First(&ex, req.Id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("execution not found")
		}
		return nil, err
	}

	stepLogs := make([]types.StepLogItem, len(ex.StepLogs))
	for i, s := range ex.StepLogs {
		stepLogs[i] = types.StepLogItem{
			Id:         int64(s.ID),
			StepID:     s.StepID,
			StepType:   s.StepType,
			Status:     s.Status,
			Input:      string(s.Input),
			Output:     string(s.Output),
			Error:      s.Error,
			DurationMs: s.DurationMs,
		}
	}
	return &types.ExecutionItem{
		Id:        int64(ex.ID),
		Status:    ex.Status,
		StartedAt: ex.StartedAt.Format("2006-01-02T15:04:05Z07:00"),
		StepLogs:  stepLogs,
	}, nil
}
