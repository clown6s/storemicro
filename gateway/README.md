# Gateway — API 网关

系统唯一外部入口。将 REST HTTP 请求通过 **grpc-gateway** 转换为内部 gRPC 调用，通过 **etcd** 动态发现后端服务实例。统一处理 JWT 认证、Redis Token 校验、限流、熔断、链路追踪、CORS、安全 Header，内部服务无需重复实现这些横切关注点。

## 目录

- [架构设计](#架构设计)
- [核心设计](#核心设计)
- [目录结构](#目录结构)
- [配置管理](#配置管理)
- [启动方式](#启动方式)
- [健康检查与可观测性](#健康检查与可观测性)
- [常用命令](#常用命令)
- [扩展：接入新服务](#扩展接入新服务)

---

## 架构设计

```
                      HTTP Client
                          │
                    REST JSON :8080
                          │
                 ┌────────▼──────────┐
                 │   Gateway (:8080) │
                 │                  │
                 │  中间件链：       │
                 │  ① Recovery      │
                 │  ② CORS          │
                 │  ③ Security Headers│
                 │  ④ JWT Auth      │
                 │  ⑤ Rate Limit    │
                 │  ⑥ grpc-gateway  │
                 │  ⑦ Circuit Breaker│
                 │  ⑧ OTel Tracing  │
                 └──┬───────────┬───┘
                    │  gRPC     │  gRPC
            ┌───────▼───┐ ┌───▼──────────┐
            │   User    │ │   District   │
            │  :9001    │ │   :9003      │
            └───────────┘ └──────────────┘
                    │
              ┌─────┴─────┐
              │  etcd     │
              │ 服务发现   │
              └───────────┘
```

### 请求处理流水线

每个 HTTP 请求经过以下中间件链：

```
Request
  → limitRequestBody (10MB 上限，防 OOM)
  → recoverMiddleware (panic 恢复)
  → corsMiddleware (白名单 CORS，支持 credentials)
  → securityHeadersMiddleware (7 个安全 Header)
  → jwtAuth (JWT 验证 + Redis token 校验 + userID 注入)
  → grpc-gateway (JSON → Protobuf)
  → OTel HTTP Handler (追踪 span)
  → gRPC Client (带熔断拦截器 + OTel stats handler)
  → 后端服务
```

中间件顺序很重要：Recovery 最先（保证不崩溃），Auth 在 grpc-gateway 之前（未认证请求直接拒绝）。

---

## 核心设计

### 1. 统一认证（JWT + Redis 双重校验）

```
请求到达 → 白名单？(register/login) → 是：跳过认证
                                    → 否：提取 Bearer token
                                        ↓
                                   JWT 签名验证（HS256）
                                        ↓
                                   Redis 查询 storemicro:token:{token}
                                        ↓
                                   Redis 返回 userID ↔ JWT 中 userID 匹配？
                                        ↓ 是                              ↓ 否
                                   放行，注入 user_id              返回 401
```

- **白名单路径**：`/api/v1/user/register`、`/api/v1/user/login`、`/healthz`、`/readyz`、`/metrics`
- **Fail-closed 策略**：Redis 不可用时拒绝认证，不放行（安全优先于可用性）
- **userID 匹配校验**：防止 token 被替换后 userID 与 Redis 记录不一致

### 2. Redis 滑动窗口限流

使用 ZSET + Lua 脚本实现基于 IP 的滑动窗口限流：

| 路径 | 限制 | 窗口 |
|------|------|------|
| `/api/v1/user/login` | 5 次/IP | 60 秒 |
| `/api/v1/user/register` | 3 次/IP | 60 秒 |

Redis key：`storemicro:ratelimit:{path}:{IP}`，Lua 脚本保证原子性。

### 3. per-service 熔断（gobreaker）

每个后端服务独立的熔断器，gRPC 一元调用拦截器实现：

```
正常请求 → 成功计数递增
失败请求 → 失败计数递增
                ↓
失败率超过阈值（默认 50%，最小样本 10 次）
                ↓
        ┌───────┴───────┐
   CLOSED → OPEN      HALF-OPEN
   (正常放行)  (拒绝请求)  (放行 1 个探测)
        ↑           │           │
        └───────────┘───────────┘
         冷却时间到期后进入 HALF-OPEN
```

配置项：

| 参数 | 默认值 | 说明 |
|------|--------|------|
| MaxRequests | 5 | HALF-OPEN 状态最大探测数 |
| Interval | 60s | CLOSED 状态计数重置间隔 |
| Timeout | 30s | OPEN 状态持续时间 |
| FailureRatio | 0.5 | 失败率阈值 |
| MinRequests | 10 | 触发熔断的最小请求数 |

### 4. 安全 Header

每个响应自动添加以下 Header：

| Header | 值 | 作用 |
|--------|-----|------|
| `X-Content-Type-Options` | `nosniff` | 防 MIME 嗅探 |
| `X-Frame-Options` | `DENY` | 防点击劫持 |
| `X-XSS-Protection` | `1; mode=block` | 防 XSS |
| `Referrer-Policy` | `strict-origin-when-cross-origin` | 控制 Referrer 泄露 |
| `Content-Security-Policy` | 严格策略 | 防 CSP 注入 |
| `Cache-Control` | `no-store` | API 响应不缓存 |
| `Strict-Transport-Security` | `max-age=31536000` | 强制 HTTPS |
| `Permissions-Policy` | 最小权限 | 限制浏览器 API |

### 5. CORS 白名单

基于白名单的 CORS，禁止 `*` 通配符（与 credentials 不兼容）：
- 支持预检请求（OPTIONS）
- 仅允许白名单内的 Origin
- 暴露必要响应 Header

### 6. 服务发现与负载均衡

```
gateway → discovery:///user → etcd 查询 → [实例1:9001, 实例2:9001]
                                        ↓
                                  round_robin 负载均衡
                                        ↓
                               gRPC 客户端直连后端实例
```

- 使用 Kratos `discovery://` scheme + etcd resolver
- 客户端负载均衡策略：`round_robin`
- gRPC 连接池 + keepalive（30s 间隔 / 10s 超时）
- 自动健康检查 + 重试策略（配置化）

### 7. 优雅退出

```
SIGINT / SIGTERM
    ↓
停止接受新连接
    ↓
等待进行中的请求完成（ctx timeout）
    ↓
关闭 gRPC 连接池
    ↓
关闭 Redis 连接
    ↓
关闭 etcd 客户端
    ↓
关闭 OTel tracer provider
    ↓
进程退出
```

---

## 目录结构

```
gateway/
├── api/
│   ├── user/v1/
│   │   ├── user.proto             # 用户服务 proto（从 user 服务复制）
│   │   ├── user.pb.go             # protobuf 消息结构（protoc 生成）
│   │   ├── user_grpc.pb.go        # gRPC 客户端接口（protoc 生成）
│   │   └── user.pb.gw.go          # grpc-gateway HTTP 路由（protoc 生成）
│   └── district/v1/
│       ├── district.proto         # 行政区划 proto（从 district 服务复制）
│       ├── district.pb.go
│       ├── district_grpc.pb.go
│       └── district.pb.gw.go
│
├── cmd/gateway/
│   ├── main.go                    # 入口：配置 → logger → tracer → proxy → HTTP Server → 优雅退出
│   ├── proxy.go                   # GatewayProxy：etcd/Redis/gRPC 连接、路由注册、ServeHTTP
│   ├── registry.go                # 服务注册表（新增服务只需加一行）
│   ├── auth.go                    # JWT 认证中间件 + Redis token 校验 + 限流 + CORS + 安全 Header
│   ├── appconfig.go               # YAML 配置加载、${VAR:default} 展开、配置校验
│   ├── circuitbreaker.go          # per-service gobreaker 熔断器 + gRPC 拦截器
│   └── flag.go                    # 命令行参数定义（-conf）
│
├── configs/
│   └── config.yaml                # 网关配置
├── Dockerfile                     # 多阶段构建
├── third_party/google/            # proto HTTP 注解依赖
├── go.mod
└── go.sum
```

### 文件职责

| 文件 | 职责 |
|------|------|
| `main.go` | 启动/关闭生命周期，健康检查端点（/healthz、/readyz、/metrics） |
| `proxy.go` | `GatewayProxy` 核心结构体，管理 etcd/Redis/gRPC 连接，注册所有服务路由 |
| `registry.go` | `services []ServiceRegistration` 注册表，新增后端服务只需加一行 |
| `auth.go` | 全部中间件：Recovery、CORS、Security Headers、JWT Auth、Rate Limit |
| `appconfig.go` | `AppConfig` 结构体、YAML 加载、`${VAR:default}` 单次正则展开、JWT secret 长度校验 |
| `circuitbreaker.go` | gobreaker per-service 熔断器，gRPC unary client interceptor |
| `flag.go` | `-conf` 命令行参数 |

---

## 配置管理

### 配置加载流程

```
LoadAppConfig →  读取 config.yaml + ${VAR:default} 展开
       ↓
校验           →  JWT secret ≥ 32 字符，空 Redis 密码告警
       ↓
AppConfig      →  Name, Version, Server, Etcd, Jwt, Redis, Log, Tracing, MetricsPassword
```

### 配置项

| 配置块 | 说明 |
|--------|------|
| `name` / `version` | 网关标识 |
| `server` | HTTP 监听地址（`:8080`）和超时（read 30s, write 60s, idle 120s） |
| `etcd` | etcd 服务发现（endpoints、dial_timeout） |
| `jwt` | JWT 密钥（与 user 服务一致，≥32 字符） |
| `redis` | Redis 连接（token 校验 + 限流） |
| `grpc_client` | gRPC 客户端配置（负载均衡、健康检查、重试、消息大小限制） |
| `circuit_breaker` | 熔断器配置（MaxRequests、Interval、Timeout、FailureRatio、MinRequests） |
| `log` | 日志级别 |
| `tracing` | OTLP 追踪端点和采样率 |
| `metrics_password` | /metrics 端点 Bearer token（时恒比较） |

### gRPC 客户端策略

通过 `BuildServiceConfig` 生成 gRPC service config JSON：

| 策略 | 配置 |
|------|------|
| 负载均衡 | `round_robin` |
| 健康检查 | 每 10s 一次，超时 5s |
| 重试 | 可配置，最大 3 次，仅对 IDEMPOTENT 方法重试 |
| Keepalive | 30s 间隔，10s 超时 |

---

## 启动方式

### 本地开发

```bash
cd gateway
go run ./cmd/gateway/ -conf configs/config.yaml
```

启动流程：

1. `LoadAppConfig()` 加载 + 展开配置
3. 初始化 zap logger
4. 初始化 OTLP tracer
5. `NewGatewayProxy()` 创建网关代理：
   - 连接 Redis（带重试）
   - 连接 etcd（带重试）
   - 创建 Kratos etcd registry + gRPC discovery resolver
   - 通过 `discovery:///` 连接各后端服务
   - 循环 `services` 注册所有 grpc-gateway handler
   - 设置熔断拦截器 + OTel stats handler
6. 创建 HTTP Server，绑定 handler chain
7. 监听 SIGINT/SIGTERM → 优雅退出

### 编译运行

```bash
cd gateway
go build -o bin/gateway ./cmd/gateway/
./bin/gateway -conf configs/config.yaml
```

### Docker

```bash
docker build -t storemicro-gateway .
docker run -p 8080:8080 \
  -e JWT_SECRET=xxx -e REDIS_PASSWORD=xxx -e METRICS_PASSWORD=xxx \
  storemicro-gateway
```

---

## 健康检查与可观测性

| 端点 | 说明 |
|------|------|
| `GET /healthz` | 存活探针，始终返回 200 |
| `GET /readyz` | 就绪探针，检查 Redis + etcd 连通性 |
| `GET /metrics` | Prometheus 指标（可选 Bearer token 认证，时恒比较） |

追踪链路：

```
Client HTTP Request
  → Gateway HTTP span (otelhttp)
  → Gateway gRPC client span (otelgrpc)
  → Backend gRPC server span (otelgrpc)
  → Backend biz custom span (如 user ListUsers)
```

---

## 常用命令

```bash
# === 运行 ===
go run ./cmd/gateway/ -conf configs/config.yaml

# === 编译 ===
go build -o bin/gateway ./cmd/gateway/

# === Proto 代码生成（gateway 需要额外生成 .pb.gw.go） ===
cd gateway
protoc \
  --proto_path=. \
  --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=. --grpc-gateway_opt=paths=source_relative \
  api/user/v1/user.proto

# === 生成 district proto ===
protoc \
  --proto_path=. \
  --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=. --grpc-gateway_opt=paths=source_relative \
  api/district/v1/district.proto

# === 其他 ===
go mod tidy          # 整理依赖
go test ./...        # 运行测试
go vet ./...         # 静态检查
```

> **注意**：只有 gateway 需要 `--grpc-gateway_out` 生成 `.pb.gw.go`，user/district 服务不需要。

---

## 扩展：接入新服务

以接入 `order` 服务为例，只需 **两步**：

### 1. 复制 proto 并生成代码

```bash
# 复制 proto 文件
mkdir -p gateway/api/order/v1
cp ../order/api/order/v1/order.proto gateway/api/order/v1/
cp ../order/api/order/v1/order.pb.go gateway/api/order/v1/
cp ../order/api/order/v1/order_grpc.pb.go gateway/api/order/v1/

# 生成 .pb.gw.go（gateway 特有）
cd gateway
protoc \
  --proto_path=. \
  --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=. --grpc-gateway_opt=paths=source_relative \
  api/order/v1/order.proto
```

### 2. 修改 registry.go 加一行

```go
// gateway/cmd/gateway/registry.go
var services = []ServiceRegistration{
    {
        Name:            "user",
        RegisterHandler: userv1.RegisterUserHandler,
    },
    {
        Name:            "district",
        RegisterHandler: districtv1.RegisterDistrictHandler,
    },
    // 新增这一行：
    {
        Name:            "order",
        RegisterHandler: orderv1.RegisterOrderHandler,
    },
}
```

不需要修改 `proxy.go`！`NewGatewayProxy` 自动循环 `services` 注册所有服务，etcd 连接、gRPC 连接、熔断器全部自动创建。

### 3. 重启

```bash
go run ./cmd/gateway/ -conf configs/config.yaml
```
