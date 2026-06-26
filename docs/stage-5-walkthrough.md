# Stage 5 — Keycloak + JWT Auth: Walkthrough từng bước

> **Mục tiêu**: Bảo vệ tất cả workflow/execution endpoints bằng JWT. Chỉ owner mới xem/sửa/xóa workflow của mình. Admin thấy tất cả. `/auth/login` proxy tới Keycloak.

---

## Tổng quan những gì thay đổi

```
docker-compose.yml               ← thêm keycloak service
keycloak/realm-export.json       ← auto-import realm + users khi startup
api-gateway/
  internal/config/config.go      ← thêm AuthConfig{JWKSUrl, KeycloakURL}
  etc/gateway-api.yaml           ← thêm Auth config
  model/models.go                ← thêm UserID field vào Workflow
  internal/types/types.go        ← thêm LoginReq, LoginResp
  internal/auth/claims.go        ← context helpers: UserIDFromCtx, HasRole
  internal/middleware/
    authmiddleware.go             ← validate JWT qua Keycloak JWKS
  internal/handler/
    loginhandler.go               ← POST /auth/login handler
    routes.go                     ← split public vs protected routes
  internal/logic/
    loginlogic.go                 ← proxy tới Keycloak token endpoint
    workflowlogic.go              ← set UserID khi create, filter khi list/get/update/delete
```

---

## Bước 1 — Keycloak trong Docker Compose

**File**: [docker-compose.yml](../docker-compose.yml)

```yaml
keycloak:
  image: quay.io/keycloak/keycloak:24.0
  environment:
    KEYCLOAK_ADMIN: admin
    KEYCLOAK_ADMIN_PASSWORD: admin
  ports:
    - "8080:8080"
  volumes:
    - ./keycloak/realm-export.json:/opt/keycloak/data/import/realm-export.json
  command: start-dev --import-realm
  healthcheck:
    test: ["CMD-SHELL", "curl -sf http://localhost:8080/realms/workflow-app || exit 1"]
    interval: 10s
    start_period: 60s
```

`--import-realm` tự động import file JSON khi container khởi động lần đầu.  
`start_period: 60s` — Keycloak khởi động chậm (~30-60s), healthcheck không tính failure trong giai đoạn này.

**File**: [keycloak/realm-export.json](../keycloak/realm-export.json)

```json
{
  "realm": "workflow-app",
  "clients": [{ "clientId": "workflow-client", "publicClient": true, "directAccessGrantsEnabled": true }],
  "roles": { "realm": [{"name": "user"}, {"name": "admin"}] },
  "users": [
    { "username": "alice", "credentials": [{"value": "alice123"}], "realmRoles": ["user"] },
    { "username": "bob",   "credentials": [{"value": "bob123"}],  "realmRoles": ["admin"] }
  ]
}
```

- **`publicClient: true`** — client không cần client_secret (dễ test với curl)
- **`directAccessGrantsEnabled: true`** — bật Resource Owner Password Credentials (ROPC) flow

---

## Bước 2 — Config + Model

### config.go

**File**: [api-gateway/internal/config/config.go](../api-gateway/internal/config/config.go)

```go
type AuthConfig struct {
    JWKSUrl     string // validate token signature
    KeycloakURL string // proxy login requests
}
```

### gateway-api.yaml

```yaml
Auth:
  JWKSUrl: "http://localhost:8080/realms/workflow-app/protocol/openid-connect/certs"
  KeycloakURL: "http://localhost:8080"
```

### model/models.go

```go
type Workflow struct {
    gorm.Model
    UserID string `gorm:"index"` // Keycloak subject (sub claim)
    // ...
}
```

`AutoMigrate` tự động thêm cột `user_id` (không phá vỡ dữ liệu cũ — rows cũ có `user_id = ""`).

---

## Bước 3 — Auth Context Helpers

**File**: [api-gateway/internal/auth/claims.go](../api-gateway/internal/auth/claims.go)

```go
func WithUserID(ctx context.Context, id string) context.Context { ... }
func WithRoles(ctx context.Context, roles []string) context.Context { ... }

func UserIDFromCtx(ctx context.Context) string { ... }
func HasRole(ctx context.Context, role string) bool { ... }
```

Dùng **unexported** `contextKey` type để tránh collision với keys từ thư viện khác:

```go
type contextKey string
const userIDKey contextKey = "userID"
```

Nếu dùng bare string `"userID"` làm key, bất kỳ package nào cũng có thể ghi đè. Unexported type đảm bảo chỉ package `auth` mới set/get giá trị này.

---

## Bước 4 — JWT Middleware

**File**: [api-gateway/internal/middleware/authmiddleware.go](../api-gateway/internal/middleware/authmiddleware.go)

```go
type JWTMiddleware struct {
    kf keyfunc.Keyfunc // JWKS key function with background refresh
}

func NewJWTMiddleware(jwksURL string) *JWTMiddleware {
    k, err := keyfunc.NewDefaultCtx(context.Background(), []string{jwksURL})
    // ...
    return &JWTMiddleware{kf: k}
}

func (m *JWTMiddleware) Handle(next http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        tokenStr := bearerToken(r) // Extract from "Authorization: Bearer <token>"
        
        token, err := jwt.Parse(tokenStr, m.kf.Keyfunc,
            jwt.WithValidMethods([]string{"RS256"}),
            jwt.WithExpirationRequired(),
        )
        if err != nil || !token.Valid {
            writeUnauthorized(w, "invalid token")
            return
        }
        
        claims := token.Claims.(jwt.MapClaims)
        userID := claims["sub"].(string)           // Keycloak user UUID
        roles  := realmRoles(claims)               // from realm_access.roles
        
        ctx := auth.WithUserID(r.Context(), userID)
        ctx = auth.WithRoles(ctx, roles)
        next(w, r.WithContext(ctx))
    }
}
```

### Keycloak JWT structure

```json
{
  "sub": "a1b2c3d4-...",        ← userID
  "preferred_username": "alice",
  "realm_access": {
    "roles": ["user", "offline_access", "default-roles-workflow-app"]
  },
  "exp": 1234567890
}
```

### JWKS và key rotation

`github.com/MicahParks/keyfunc/v3` tự động:
- Fetch public keys từ JWKS endpoint khi khởi động
- Cache keys in-memory
- Background refresh khi key rotation xảy ra
- Retry nếu Keycloak tạm thời unavailable

Kết quả: JWT middleware không block server start dù Keycloak chưa sẵn sàng.

---

## Bước 5 — Login Endpoint

### loginlogic.go

**File**: [api-gateway/internal/logic/loginlogic.go](../api-gateway/internal/logic/loginlogic.go)

```go
func (l *LoginLogic) Login(req *types.LoginReq) (*types.LoginResp, error) {
    tokenURL := l.svcCtx.Config.Auth.KeycloakURL + "/realms/workflow-app/protocol/openid-connect/token"
    
    form := url.Values{
        "grant_type": {"password"},
        "client_id":  {"workflow-client"},
        "username":   {req.Username},
        "password":   {req.Password},
    }
    
    resp, _ := http.PostForm(tokenURL, form)
    // decode và return LoginResp{access_token, refresh_token, expires_in}
}
```

Flow này gọi là **Resource Owner Password Credentials (ROPC)** — client gửi username/password trực tiếp thay vì redirect. Đủ tốt cho dev/testing nhưng không dùng cho production (PKCE flow an toàn hơn).

---

## Bước 6 — Routes: Public vs Protected

**File**: [api-gateway/internal/handler/routes.go](../api-gateway/internal/handler/routes.go)

```go
func RegisterHandlers(server *rest.Server, serverCtx *svc.ServiceContext) {
    // Public — không cần token
    server.AddRoutes([]rest.Route{
        {Method: "GET",  Path: "/health",           Handler: HealthHandler(serverCtx)},
        {Method: "POST", Path: "/auth/login",        Handler: LoginHandler(serverCtx)},
        {Method: "POST", Path: "/webhooks/:workflowId", Handler: TriggerWorkflowHandler(serverCtx)},
        {Method: "GET",  Path: "/executions/:id/stream", Handler: ExecutionStreamHandler(serverCtx)},
    })
    
    // Protected — phải có Bearer token hợp lệ
    jwtMW := middleware.NewJWTMiddleware(serverCtx.Config.Auth.JWKSUrl)
    server.AddRoutes(
        rest.WithMiddlewares(
            []rest.Middleware{jwtMW.Handle},
            rest.Route{Method: "GET",    Path: "/workflows",   Handler: ...},
            rest.Route{Method: "POST",   Path: "/workflows",   Handler: ...},
            // ...
        ),
    )
}
```

`rest.WithMiddlewares(ms []Middleware, rs ...Route) []Route` — wrap từng route trong slice với middleware chain, trả về `[]Route` (không phải variadic — không dùng `...` khi truyền vào `AddRoutes`).

**Tại sao webhooks và stream là public?**  
Webhooks được gọi bởi external systems (không có JWT).  
Stream endpoint — client JS dùng `EventSource` API không support custom headers.

---

## Bước 7 — User Filtering trong WorkflowLogic

**File**: [api-gateway/internal/logic/workflowlogic.go](../api-gateway/internal/logic/workflowlogic.go)

### Create: gán UserID

```go
wf := model.Workflow{
    UserID: auth.UserIDFromCtx(l.ctx), // Keycloak sub claim
    // ...
}
```

### List: filter theo role

```go
q := l.svcCtx.DB
if !auth.HasRole(l.ctx, "admin") {
    userID := auth.UserIDFromCtx(l.ctx)
    q = q.Where("user_id = ? OR user_id = ''", userID)
}
q.Find(&workflows)
```

`user_id = ''` cho phép truy cập các workflows tạo trước khi thêm auth (backward compat).

### Get/Update/Delete: kiểm tra ownership

```go
func ownerCheck(ctx context.Context, ownerID string) error {
    if ownerID == "" {
        return nil // pre-auth workflow, allow all
    }
    if auth.UserIDFromCtx(ctx) != ownerID {
        return errors.New("forbidden") // → 403
    }
    return nil
}
```

---

## Bước 8 — Luồng dữ liệu đầy đủ Stage 5

### Login flow

```
POST /auth/login {"username": "alice", "password": "alice123"}
    ↓
LoginLogic.Login()
    ↓
Keycloak: POST /realms/workflow-app/protocol/openid-connect/token
    ↓
Response: {"access_token": "eyJ...", "expires_in": 3600}
```

### Protected request flow

```
GET /workflows
Authorization: Bearer eyJ...
    ↓
JWTMiddleware.Handle()
    ├── Extract Bearer token
    ├── jwt.Parse() với JWKS key function
    ├── Validate RS256 signature + expiry
    ├── Extract sub → userID
    ├── Extract realm_access.roles → ["user"]
    └── Inject vào request context
    ↓
ListWorkflowsLogic.ListWorkflows()
    ├── auth.HasRole(ctx, "admin") → false
    ├── WHERE user_id = 'a1b2c3...' OR user_id = ''
    └── Return filtered list
```

---

## Bước 9 — Test

### Start Keycloak (lần đầu ~60s)

```bash
make keycloak-up
# Chờ healthcheck: docker-compose ps → keycloak: healthy
```

### Lấy token

```bash
# Dùng Makefile shortcut
make keycloak-token

# Hoặc thủ công
TOKEN=$(curl -s -X POST http://localhost:8080/realms/workflow-app/protocol/openid-connect/token \
  -d "grant_type=password&client_id=workflow-client&username=alice&password=alice123" \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['access_token'])")
```

### Test auth

```bash
# Không có token → 401
curl http://localhost:8888/workflows

# Có token → 200
curl -H "Authorization: Bearer $TOKEN" http://localhost:8888/workflows

# Alice tạo workflow
curl -X POST http://localhost:8888/workflows \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"Alice Workflow","triggerType":"webhook"}'
# → {"id": 1}

# Bob (admin) lấy token
BOB_TOKEN=$(curl -s -X POST http://localhost:8080/realms/workflow-app/protocol/openid-connect/token \
  -d "grant_type=password&client_id=workflow-client&username=bob&password=bob123" \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['access_token'])")

# Bob thấy tất cả workflows (admin bypass)
curl -H "Authorization: Bearer $BOB_TOKEN" http://localhost:8888/workflows

# Charlie (không tồn tại) → 401
curl -H "Authorization: Bearer invalidtoken" http://localhost:8888/workflows
```

### Verify JWKS endpoint

```bash
# Xem public keys Keycloak expose
curl http://localhost:8080/realms/workflow-app/protocol/openid-connect/certs | python3 -m json.tool
```

---

## Tóm tắt kiến thức đã học

| Khái niệm | Áp dụng |
|---|---|
| OAuth2 ROPC flow | POST username/password → Keycloak → access_token |
| JWT (RS256) | Token ký bằng RSA private key, verify bằng public key từ JWKS |
| JWKS | JSON Web Key Set — Keycloak expose public keys để bất kỳ service nào verify token |
| `golang-jwt/jwt/v5` | Parse + validate JWT, extract claims |
| `MicahParks/keyfunc/v3` | Auto-fetch + cache JWKS, background key rotation support |
| Context injection | Middleware inject userID/roles → logic layer đọc qua `auth.UserIDFromCtx(ctx)` |
| `unexported contextKey` | Type-safe context keys tránh collision giữa packages |
| `rest.WithMiddlewares` | go-zero API để wrap route group với middleware |
| Ownership check | Load record → compare ownerID với calling userID → 403 nếu không match |
| Admin bypass | `HasRole(ctx, "admin")` → skip ownership check |

---

## Cái gì chưa làm (để sang Stage 6+)

- Webhook trigger vẫn public (bất kỳ ai cũng trigger được) — Stage 5 chỉ protect CRUD
- Token refresh endpoint (`POST /auth/refresh`)
- Keycloak user management API (tạo user từ API thay vì hardcode trong realm JSON)
- Stage 6: Webhook trả về ngay → execution async qua RabbitMQ
