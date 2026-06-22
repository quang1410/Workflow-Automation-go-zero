// Code scaffolded by goctl. Safe to edit.
// goctl 1.10.1

package logic

import (
	"context"

	"api-gateway/internal/svc"
	"api-gateway/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type ListWorkflowsLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewListWorkflowsLogic(ctx context.Context, svcCtx *svc.ServiceContext) *ListWorkflowsLogic {
	return &ListWorkflowsLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *ListWorkflowsLogic) ListWorkflows() (resp *types.ListWorkflowsResp, err error) {
	items := []types.WorkflowItem{
		{Id: 1, Name: "Notify on new user", TriggerType: "webhook", IsActive: true},
		{Id: 2, Name: "Daily report", TriggerType: "schedule", IsActive: false},
	}
	return &types.ListWorkflowsResp{
		Items: items,
		Total: int64(len(items)),
	}, nil
}
