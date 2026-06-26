package logic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"api-gateway/internal/auth"
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
		UserID:        auth.UserIDFromCtx(l.ctx),
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
	q := l.svcCtx.DB
	// admins see all workflows; regular users see only their own
	if !auth.HasRole(l.ctx, "admin") {
		userID := auth.UserIDFromCtx(l.ctx)
		q = q.Where("user_id = ? OR user_id = ''", userID)
	}
	if err := q.Find(&workflows).Error; err != nil {
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
	key := fmt.Sprintf("workflow:%d", req.Id)

	// Cache hit
	if cached, err := l.svcCtx.Redis.Get(l.ctx, key).Result(); err == nil {
		var item types.WorkflowItem
		if json.Unmarshal([]byte(cached), &item) == nil {
			if err := l.checkOwnership(item.Id); err != nil {
				return nil, err
			}
			return &item, nil
		}
	}

	// Cache miss — query DB
	var wf model.Workflow
	if err := l.svcCtx.DB.First(&wf, req.Id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("workflow not found")
		}
		return nil, err
	}

	if err := l.checkOwnershipModel(&wf); err != nil {
		return nil, err
	}

	item := toWorkflowItem(wf)

	// Populate cache with 5-minute TTL
	if data, err := json.Marshal(item); err == nil {
		l.svcCtx.Redis.SetEx(l.ctx, key, string(data), 5*time.Minute)
	}

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
	var wf model.Workflow
	if err := l.svcCtx.DB.First(&wf, req.Id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("workflow not found")
		}
		return nil, err
	}
	if err := l.checkOwnershipModel(&wf); err != nil {
		return nil, err
	}

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
	if err := l.svcCtx.DB.Model(&wf).Updates(updates).Error; err != nil {
		return nil, err
	}
	l.svcCtx.Redis.Del(l.ctx, fmt.Sprintf("workflow:%d", req.Id))
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
	var wf model.Workflow
	if err := l.svcCtx.DB.First(&wf, req.Id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("workflow not found")
		}
		return nil, err
	}
	if err := l.checkOwnershipModel(&wf); err != nil {
		return nil, err
	}

	if err := l.svcCtx.DB.Delete(&wf).Error; err != nil {
		return nil, err
	}
	l.svcCtx.Redis.Del(l.ctx, fmt.Sprintf("workflow:%d", req.Id))
	return &types.DeleteWorkflowResp{Ok: true}, nil
}

// --- helpers ---

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

// checkOwnership verifies the calling user can access workflow by id.
// Admins bypass ownership checks. Empty UserID on the workflow means it
// predates auth and any authenticated user can access it.
func (l *GetWorkflowLogic) checkOwnership(workflowID int64) error {
	if auth.HasRole(l.ctx, "admin") {
		return nil
	}
	var wf model.Workflow
	if err := l.svcCtx.DB.Select("user_id").First(&wf, workflowID).Error; err != nil {
		return errors.New("workflow not found")
	}
	return ownerCheck(l.ctx, wf.UserID)
}

func (l *GetWorkflowLogic) checkOwnershipModel(wf *model.Workflow) error {
	if auth.HasRole(l.ctx, "admin") {
		return nil
	}
	return ownerCheck(l.ctx, wf.UserID)
}

func (l *UpdateWorkflowLogic) checkOwnershipModel(wf *model.Workflow) error {
	if auth.HasRole(l.ctx, "admin") {
		return nil
	}
	return ownerCheck(l.ctx, wf.UserID)
}

func (l *DeleteWorkflowLogic) checkOwnershipModel(wf *model.Workflow) error {
	if auth.HasRole(l.ctx, "admin") {
		return nil
	}
	return ownerCheck(l.ctx, wf.UserID)
}

// ownerCheck returns nil if userID is empty (pre-auth workflow) or matches the calling user.
func ownerCheck(ctx context.Context, ownerID string) error {
	if ownerID == "" {
		return nil
	}
	if auth.UserIDFromCtx(ctx) != ownerID {
		return errors.New("forbidden")
	}
	return nil
}
