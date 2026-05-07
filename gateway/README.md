# gateway — HTTP 网关服务

将外部 REST HTTP 请求转发为内部 gRPC 调用，同时通过 etcd 动态发现后端服务实例。统一的 JWT 认证和 Redis token 校验在网关层完成，内部服务无需重复校验。

---

## 生成步骤（重要：先看这里）

### ⚠️ gateway 需要生成 3 个文件（其他服务只需要 2 个）

| 文件 | 说明 | 生成参数 |
|------|------|----------|
| `*.pb.go` | protobuf 消息结构 | `--go_out=. --go_opt=paths=source_relative` |
| `*_grpc.pb.go` | gRPC 客户端接口 | `--go-grpc_out=. --go-grpc_opt=paths=source_relative` |
| `*.pb.gw.go` | HTTP 路由（grpc-gateway） | `--grpc-gateway_out=. --grpc-gateway_opt=paths=source_relative` |

> **只有 gateway 目录需要 `--grpc-gateway_out`，后端服务（user/product/district）不需要。**

---

### 完整生成命令模板

```bash
cd gateway

protoc \
  --proto_path=. \
  --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=. --grpc-gateway_opt=paths=source_relative \
  api/{服务名}/v1/{服务名}.proto
```

将 `{服务名}` 替换为实际服务名（如 `user`、`product`、`district`）。

---

### 各服务 proto 文件路径

| 服务 | gateway 下 proto 路径 | 生成后 .pb.gw.go 路径 |
|------|----------------------|----------------------|
| user | `api/user/v1/user.proto` | `api/user/v1/user.pb.gw.go` |
| product | `api/product/v1/product.proto` | `api/product/v1/product.pb.gw.go` |
| district | `api/district/v1/district.proto` | `api/district/v1/district.pb.gw.go` |

---

## 目录

- [职责](#职责)
- [目录结构](#目录结构)
- [配置文件](#配置文件)
- [启动方式](#启动方式)
- [对外 REST 接口](#对外-rest-接口)
- [认证与登出流程](#认证与登出流程)
- [请求流转过程](#请求流转过程)
- [扩展：接入新服务](#扩展接入新服务)
- [常用命令](#常用命令)

---

## 职责

| 能力 | 说明 |
|------|------|
| 协议转换 | REST JSON ↔ gRPC Protobuf（由 grpc-gateway 生成代码驱动） |
| 服务发现 | 通过 etcd + Kratos discovery resolver 动态解析后端地址 |
| 负载均衡 | `round_robin` 策略，自动轮询多个后端实例 |
| JWT 认证 | 网关统一鉴权，白名单路径免校验 |
| Token 校验 | Redis 验证 token 有效性，支持单设备登录踢人 |
| 链路追踪 | OpenTelemetry + OTLP gRPC → Jaeger（`storemicro-gateway`） |
| 优雅退出 | 监听 SIGINT/SIGTERM，等待当前请求完成后关闭 |

---

## 目录结构

```
gateway/
├── api/
│   ├── product/v1/
│   │   ├── product.proto         # 接口定义（源文件）
│   │   ├── product.pb.go          # protobuf 消息结构（生成）
│   │   ├── product_grpc.pb.go     # gRPC 客户端接口（生成）
│   │   └── product.pb.gw.go      # grpc-gateway HTTP 路由（生成）
│   └── user/v1/
│       ├── user.proto
│       ├── user.pb.go
│       ├── user_grpc.pb.go
│       └── user.pb.gw.go
├── cmd/gateway/
│   ├── main.go        # 程序入口，HTTP 服务器生命周期、tracing 初始化
│   ├── proxy.go       # GatewayProxy：etcd 连接、gRPC 连接、路由注册、Redis 连接
│   ├── registry.go    # services 注册表（新增服务只需加一行）
│   ├── auth.go        # JWT 认证中间件 + Redis token 校验
│   ├── appconfig.go   # YAML 配置加载，AppConfig 结构体
│   └── logger.go     # zap 日志，Kratos log.Logger 桥接
├── configs/
│   └── config.yaml    # 配置文件
├── third_party/
│   └── google/        # proto HTTP 注解依赖（含 protobuf/descriptor.proto）
└── go.mod
```

> `registry.go` 中的 `services` 变量是服务注册表，新增后端服务只需加一行 `ServiceRegistration{}`，连接/注册/关闭逻辑全部循环化。

---

## 配置文件

`configs/config.yaml`

```yaml
name: storemicro
version: v1

# HTTP 服务器
server:
  addr: :8080

# etcd 服务发现
etcd:
  endpoints:
    - 10.211.55.5:2379
  dial_timeout: 5s

# 日志级别：debug | info | warn | error
log:
  level: info

# JWT 认证（与 user 服务保持一致的 secret）
jwt:
  secret: "store-micro-jwt-secret-2026"

# Redis（用于 token 校验，支持单设备登录踢人）
redis:
  addr: "10.211.55.5:6379"
  password: "admin@zzp"
  db: 0
```

---

## 启动方式

### 开发模式

```bash
cd gateway
go run ./cmd/gateway/ -conf configs/config.yaml
```

### 编译后运行

```bash
cd gateway
go build -o bin/gateway ./cmd/gateway/
./bin/gateway -conf configs/config.yaml
```

启动成功日志：

```
INFO  storemicro gateway starting  listen=:8080  conf=configs
INFO  etcd registry connected       endpoints=[10.211.55.5:2379]
INFO  OTLP tracer initialized      service=storemicro-gateway  endpoint=10.211.55.5:4317
INFO  service registered           service=user
INFO  service registered           service=product
```

---

## 对外 REST 接口

所有路由由 proto 文件中的 `google.api.http` 注解驱动，grpc-gateway 自动生成。

### 用户服务

| 方法 | 路径 | 说明 | 是否需要认证 |
|------|------|------|-------------|
| `POST` | `/api/v1/user/register` | 用户注册 | 否（白名单） |
| `POST` | `/api/v1/user/login` | 用户登录 | 否（白名单） |
| `POST` | `/api/v1/user/logout` | 用户登出 | 是 |
| `GET` | `/api/v1/user/{id}` | 按 ID 查询用户 | 是 |
| `GET` | `/api/v1/user/list` | 用户列表（分页+搜索） | 是 |
| `POST` | `/api/v1/user/ping` | 健康检查 | 否（白名单） |

**注册示例**

```bash
curl -X POST http://localhost:8080/api/v1/user/register \
  -H "Content-Type: application/json" \
  -d '{"phone":"13800138000","password":"123456","realName":"张三"}'
```

响应：
```json
{"id": 1, "message": "registered"}
```

**登录示例**

```bash
curl -X POST http://localhost:8080/api/v1/user/login \
  -H "Content-Type: application/json" \
  -d '{"phone":"13800138000","password":"123456"}'
```

响应：
```json
{
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "user": {"id": 1, "phone": "13800138000", "realName": "张三"}
}
```

**登出示例**

```bash
curl -X POST http://localhost:8080/api/v1/user/logout \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer {token}" \
  -d '{"token":"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."}'
```

响应：
```json
{"message": "logged out"}
```

---

### 商品服务

| 方法 | 路径 | 说明 | 是否需要认证 |
|------|------|------|-------------|
| `GET` | `/api/v1/product/{id}` | 按 ID 查询商品 | 是 |
| `GET` | `/api/v1/product/list` | 商品列表（分页） | 是 |
| `POST` | `/api/v1/product/ping` | 健康检查 | 否（白名单） |

---

## 认证与登出流程

### JWT 认证（网关层）

1. 请求到达 gateway，`jwtAuth` 中间件检查路径是否在白名单
2. 非白名单请求必须从 `Authorization: Bearer {token}` 获取 token
3. 验证 JWT 签名（secret：`store-micro-jwt-secret-2026`）
4. JWT 校验通过后，查询 Redis `storemicro:token:{token}` 是否存在
5. token 不存在（被踢掉或已登出）→ 返回 401 `"token expired, please re-login"`
6. Redis 不可用 → 降级，仅 JWT 校验通过即放行

### 单设备登录

- 登录时 user 服务将 token 写入 Redis：
  - `storemicro:token:{token}` → userID（正向）
  - `storemicro:user_token:{userID}` → token（反向）
- 重复登录时，先删除旧 token 的两条映射，再写新的
- 旧 token 在网关层被 Redis 校验拦截 → 401

### 登出

- 调用 `POST /api/v1/user/logout`，传入 token
- user 服务删除 Redis 中该 token 的双映射
- 后续请求在网关层返回 401

---

## 请求流转过程

```
Client
  │  POST /api/v1/user/login  (JSON)
  ▼
gateway (:8080)
  │  JWT 白名单 → 跳过认证
  │  grpc-gateway 将 JSON 转为 Protobuf
  ▼
etcd discovery resolver
  │  查询 etcd 获取 user 服务实例地址
  ▼
user service (:9001)
  │  处理 Login 业务逻辑
  │  生成 JWT + 写入 Redis token
  ▼
gateway
  │  将 Protobuf 响应转为 JSON
  ▼
Client
  │  收到 {"token":"...","user":{...}}
```

```
Client（携带 token）
  │  GET /api/v1/user/list?page=1&phone=138
  │  Header: Authorization: Bearer {token}
  ▼
gateway (:8080)
  │  JWT 认证中间件
  │    1) 验证 JWT 签名
  │    2) Redis 查询 storemicro:token:{token} 是否存在
  │  OTel HTTP tracing span
  ▼
gateway → gRPC client
  │  otelgrpc ClientHandler tracing
  ▼
user service (:9001)
  │  otelgrpc ServerHandler tracing
  │  biz 层自定义 tracing span（ListUsers）
  ▼
返回 JSON 响应
```

---

## 扩展：接入新服务

以接入 `order`（订单）服务为例：

### 第一步：复制 proto 文件并更新

将 order 服务的 `api/order/v1/order.proto` 复制到 `gateway/api/order/v1/`，然后生成代码：

```bash
cd gateway
protoc \
  --proto_path=. \
  --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=. --grpc-gateway_opt=paths=source_relative \
  api/order/v1/order.proto
```

### 第二步：修改 `registry.go`

```go
// gateway/cmd/gateway/registry.go
var services = []ServiceRegistration{
    {
        Name:            "user",
        RegisterHandler: userv1.RegisterUserHandler,
    },
    {
        Name:            "product",
        RegisterHandler: productv1.RegisterProductHandler,
    },
    // 新增：
    {
        Name:            "order",
        RegisterHandler: orderv1.RegisterOrderHandler,
    },
}
```

> 不需要手动修改 `proxy.go`！`NewGatewayProxy` 会自动循环 `services` 注册所有服务。

### 第三步：重启 gateway

```bash
go run ./cmd/gateway/ -conf configs/config.yaml
```

---

## 常用命令

```bash
# 运行
go run ./cmd/gateway/ -conf configs/config.yaml

# 编译
go build -o bin/gateway ./cmd/gateway/

# 重新生成 user proto（在 gateway 目录下）
protoc \
  --proto_path=. \
  --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=. --grpc-gateway_opt=paths=source_relative \
  api/user/v1/user.proto

# 重新生成 product proto
protoc \
  --proto_path=. \
  --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=. --grpc-gateway_opt=paths=source_relative \
  api/product/v1/product.proto

# 整理依赖
go mod tidy
```

---

## Jaeger 链路追踪

- OTLP gRPC 端点：`10.211.55.5:4317`
- Jaeger UI：`http://10.211.55.5:16686`
- 服务名：`storemicro-gateway`、`storemicro-user`、`storemicro-product`
- 请求链：Gateway(HTTP span) → Gateway(gRPC client span) → User/Product(gRPC server span)
