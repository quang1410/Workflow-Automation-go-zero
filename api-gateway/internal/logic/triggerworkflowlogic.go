// Code scaffolded by goctl. Safe to edit.
// goctl 1.10.1

package logic

import (
	"context"

	"api-gateway/internal/svc"
	"api-gateway/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type TriggerWorkflowLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewTriggerWorkflowLogic(ctx context.Context, svcCtx *svc.ServiceContext) *TriggerWorkflowLogic {
	return &TriggerWorkflowLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *TriggerWorkflowLogic) TriggerWorkflow(req *types.TriggerReq) (resp *types.TriggerResp, err error) {
	l.Logger.Infof("workflow %d triggered", req.WorkflowId)
	return &types.TriggerResp{
		Message:     "workflow triggered successfully",
		ExecutionId: 1001,
	}, nil
}
