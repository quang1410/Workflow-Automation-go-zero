package logic

import (
	"context"
	"errors"

	"api-gateway/internal/svc"
	"api-gateway/internal/types"
	"api-gateway/model"

	"github.com/zeromicro/go-zero/core/logx"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// --- Create ---

type CreateWorkflowLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewCreateWorkflowLogic(ctx context.Context, svcCtx *svc.ServiceContext) *CreateWorkflowLogic {
	return &CreateWorkflowLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *CreateWorkflowLogic) CreateWorkflow(req *types.CreateWorkflowReq) (*types.CreateWorkflowResp, error) {
	wf := model.Workflow{
		Name:          req.Name,
		TriggerType:   req.TriggerType,
		TriggerConfig: datatypes.JSON(req.TriggerConfig),
		Steps:         datatypes.JSON(req.Steps),
		IsActive:      true,
	}
	if err := l.svcCtx.DB.Create(&wf).Error; err != nil {
		return nil, err
	}
	return &types.CreateWorkflowResp{Id: int64(wf.ID)}, nil
}

// --- List ---

type ListWorkflowsLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewListWorkflowsLogic(ctx context.Context, svcCtx *svc.ServiceContext) *ListWorkflowsLogic {
	return &ListWorkflowsLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *ListWorkflowsLogic) ListWorkflows() (*types.ListWorkflowsResp, error) {
	var workflows []model.Workflow
	if err := l.svcCtx.DB.Find(&workflows).Error; err != nil {
		return nil, err
	}
	items := make([]types.WorkflowItem, len(workflows))
	for i, wf := range workflows {
		items[i] = toWorkflowItem(wf)
	}
	return &types.ListWorkflowsResp{Items: items, Total: int64(len(items))}, nil
}

// --- Get ---

type GetWorkflowLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewGetWorkflowLogic(ctx context.Context, svcCtx *svc.ServiceContext) *GetWorkflowLogic {
	return &GetWorkflowLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *GetWorkflowLogic) GetWorkflow(req *types.GetWorkflowReq) (*types.WorkflowItem, error) {
	var wf model.Workflow
	if err := l.svcCtx.DB.First(&wf, req.Id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("workflow not found")
		}
		return nil, err
	}
	item := toWorkflowItem(wf)
	return &item, nil
}

// --- Update ---

type UpdateWorkflowLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewUpdateWorkflowLogic(ctx context.Context, svcCtx *svc.ServiceContext) *UpdateWorkflowLogic {
	return &UpdateWorkflowLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *UpdateWorkflowLogic) UpdateWorkflow(req *types.UpdateWorkflowReq) (*types.UpdateWorkflowResp, error) {
	updates := map[string]any{"is_active": req.IsActive}
	if req.Name != "" {
		updates["name"] = req.Name
	}
	if req.TriggerType != "" {
		updates["trigger_type"] = req.TriggerType
	}
	if req.TriggerConfig != "" {
		updates["trigger_config"] = datatypes.JSON(req.TriggerConfig)
	}
	if req.Steps != "" {
		updates["steps"] = datatypes.JSON(req.Steps)
	}
	if err := l.svcCtx.DB.Model(&model.Workflow{}).Where("id = ?", req.Id).Updates(updates).Error; err != nil {
		return nil, err
	}
	return &types.UpdateWorkflowResp{Ok: true}, nil
}

// --- Delete ---

type DeleteWorkflowLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewDeleteWorkflowLogic(ctx context.Context, svcCtx *svc.ServiceContext) *DeleteWorkflowLogic {
	return &DeleteWorkflowLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *DeleteWorkflowLogic) DeleteWorkflow(req *types.DeleteWorkflowReq) (*types.DeleteWorkflowResp, error) {
	if err := l.svcCtx.DB.Delete(&model.Workflow{}, req.Id).Error; err != nil {
		return nil, err
	}
	return &types.DeleteWorkflowResp{Ok: true}, nil
}

// --- helper ---

func toWorkflowItem(wf model.Workflow) types.WorkflowItem {
	return types.WorkflowItem{
		Id:            int64(wf.ID),
		Name:          wf.Name,
		TriggerType:   wf.TriggerType,
		TriggerConfig: string(wf.TriggerConfig),
		Steps:         string(wf.Steps),
		IsActive:      wf.IsActive,
	}
}
