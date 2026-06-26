package middleware

import (
	"context"
	"net/http"
	"strings"

	"api-gateway/internal/auth"

	keyfunc "github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/zeromicro/go-zero/core/logx"
)

type JWTMiddleware struct {
	kf keyfunc.Keyfunc
}

// NewJWTMiddleware fetches JWKS from jwksURL and caches it with background refresh.
// If Keycloak is unreachable, the middleware rejects all requests until the key is fetched.
func NewJWTMiddleware(jwksURL string) *JWTMiddleware {
	k, err := keyfunc.NewDefaultCtx(context.Background(), []string{jwksURL})
	if err != nil {
		logx.Errorf("JWKS init failed (%s): %v — all protected requests will be rejected", jwksURL, err)
		return &JWTMiddleware{}
	}
	return &JWTMiddleware{kf: k}
}

func (m *JWTMiddleware) Handle(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokenStr := bearerToken(r)
		if tokenStr == "" {
			writeUnauthorized(w, "missing token")
			return
		}

		if m.kf == nil {
			writeUnauthorized(w, "auth service unavailable")
			return
		}

		token, err := jwt.Parse(tokenStr, m.kf.Keyfunc,
			jwt.WithValidMethods([]string{"RS256"}),
			jwt.WithExpirationRequired(),
		)
		if err != nil || !token.Valid {
			writeUnauthorized(w, "invalid token")
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			writeUnauthorized(w, "invalid claims")
			return
		}

		userID, _ := claims["sub"].(string)
		roles := realmRoles(claims)

		ctx := auth.WithUserID(r.Context(), userID)
		ctx = auth.WithRoles(ctx, roles)
		next(w, r.WithContext(ctx))
	}
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return h[7:]
	}
	return ""
}

// realmRoles extracts roles from Keycloak's realm_access claim.
func realmRoles(claims jwt.MapClaims) []string {
	ra, ok := claims["realm_access"].(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := ra["roles"].([]any)
	if !ok {
		return nil
	}
	roles := make([]string, 0, len(raw))
	for _, r := range raw {
		if s, ok := r.(string); ok {
			roles = append(roles, s)
		}
	}
	return roles
}

func writeUnauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	w.Write([]byte(`{"code":401,"message":"` + msg + `"}`)) //nolint:errcheck
}
