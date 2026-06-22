// Code scaffolded by goctl. Safe to edit.
// goctl 1.10.1

package handler

import (
	"net/http"

	"api-gateway/internal/logic"
	"api-gateway/internal/svc"
	"github.com/zeromicro/go-zero/rest/httpx"
)

func ListWorkflowsHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l := logic.NewListWorkflowsLogic(r.Context(), svcCtx)
		resp, err := l.ListWorkflows()
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
		} else {
			httpx.OkJsonCtx(r.Context(), w, resp)
		}
	}
}
