package logic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"api-gateway/internal/svc"
	"api-gateway/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type LoginLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewLoginLogic(ctx context.Context, svcCtx *svc.ServiceContext) *LoginLogic {
	return &LoginLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

// Login proxies username/password to Keycloak's ROPC token endpoint.
func (l *LoginLogic) Login(req *types.LoginReq) (*types.LoginResp, error) {
	tokenURL := fmt.Sprintf("%s/realms/workflow-app/protocol/openid-connect/token",
		l.svcCtx.Config.Auth.KeycloakURL)

	form := url.Values{
		"grant_type": {"password"},
		"client_id":  {"workflow-client"},
		"username":   {req.Username},
		"password":   {req.Password},
	}

	resp, err := http.PostForm(tokenURL, form)
	if err != nil {
		return nil, fmt.Errorf("keycloak unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("invalid credentials")
	}

	var result types.LoginResp
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}
