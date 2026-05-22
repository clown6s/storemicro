# Gateway — API 网关

系统唯一外部入口，监听 `:7000`。将 REST HTTP 请求通过 **grpc-gateway** 转换为内部 gRPC 调用，通过 **etcd** 动态发现后端服务实例。统一处理 JWT 认证、Redis Token 校验、限流、熔断、链路追踪、CORS、安全 Header，内部服务无需重复实现这些横切关注点。

## 目录

- [架构设计](#架构设计)
- [请求处理流水线](#请求处理流水线)
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
                          |
                    REST JSON :7000
                          |
                 +------------------------+
                 |  Gateway (:7000)        |
                 |                         |
                 |  中间件链：              |
                 |  ① limitRequestBody     |
                 |  ② RequestID            |
                 |  ③ otelhttp (span)      |
                 |  ④ Recovery             |
                 |  ⑤ CORS                 |
                 |  ⑥ Security Headers     |
                 |  ⑦ JWT Auth + RateLimit |
                 |  ⑧ grpc-gateway mux    |
                 |  ⑨ Circuit Breaker      |
                 |  ⑩ OTel gRPC Tracing   |
                 +----+--------------+----+
                      | gRPC        | gRPC
              +-------v---+    +----v-----------+
              |   User    |    |   District    |
              |  :6001    |    |   :6002       |
              +-----------+    +---------------+
                      |
                +-----+-----+
                |  etcd     |
                | 服务发现   |
                +-----------+
```

---

## 请求处理流水线

每个 HTTP 请求按固定顺序经过以下中间件链：

```
Request 到达 :7000
    |
    ① limitRequestBody (10MB 上限，超过返回 413)
    |
    ② RequestIDMiddleware
       |  从 X-Request-Id header 读取已有 ID
       |  无 ID 则 crypto/rand 生成 16 字节 hex ID
       |  写入 X-Request-Id header，透传到后端
    |
    ③ /healthz？→ 200 "ok"
       /readyz？→ 检查 Redis + etcd 连通性，返回 200/503
       /metrics？→ 可选 Bearer token 校验，返回 Prometheus 指标
       以上三条路径直接响应，跳过后续所有中间件
    |
    ④ otelhttp (OpenTelemetry HTTP span)
       |  创建 HTTP span，注入 trace_id 到响应 header
       |  后续 gRPC 调用自动继承 parent span
    |
    ⑤ RecoverMiddleware (panic 恢复)
       |  捕获所有 panic，记录完整堆栈
       |  返回 500 Internal Server Error
       |  保证 Gateway 永不因 panic 崩溃
    |
    ⑥ CORSMiddleware
       |  检查请求 Origin 是否在白名单内
       |  OPTIONS 预检请求直接返回 CORS headers
       |  非简单请求追加 Access-Control-Allow-Origin
       |  Vary: Origin 防止缓存投毒
       |  禁止 * 通配符（与 credentials 不兼容）
    |
    ⑦ SecurityHeadersMiddleware
       |  注入 8 个安全响应头（见下方安全 Header 表）
    |
    ⑧ jwtAuth (JWT + Redis 认证 + 限流)
       |
       ├─ 白名单路径？
       |  register / login / refresh-token → 跳过 JWT 校验
       |  执行限流检查：
       |    login:     Redis ZSET + Lua 滑动窗口，5次/分/IP
       |    register:  Redis ZSET + Lua 滑动窗口，3次/分/IP
       |    超限返回 429 Too Many Requests
       |
       └─ 非白名单路径：
          提取 Authorization: Bearer {token}
            ├─ 无 Bearer / 格式错误 → 401 Unauthorized
            ├─ Token 长度 > 4096 → 401
            ├─ JWT HS256 签名验证
            |  检查 signing algorithm（防 none 算法攻击）
            |  验证 issuer、audience、exp、sub claims
            |  提取 user_id
            ├─ Redis GET storemicro:token:{token}
            |  ├─ Redis 不可用 → 503 Service Unavailable（fail-closed）
            |  ├─ token 不存在 → 401
            |  └─ userID 与 claims 不一致 → 401
            └─ 注入 user_id 到请求 metadata header，放行
    |
    ⑨ grpc-gateway mux (JSON → Protobuf)
       |  根据 proto 注解的 HTTP route 匹配对应 RPC 方法
       |  将 JSON body 反序列化为 Protobuf message
       |  通过 discovery:///{service} 连接后端
    |
    ⑩ gRPC Client 调用
       |  discovery:///user → etcd 查询 → [实例1:6001, 实例2:6001]
       |  round_robin 负载均衡选择实例
       |
       |  gRPC interceptor 链：
       |  ├─ circuitBreaker（gobreaker 按服务隔离熔断）
       |  ├─ otelgrpc（gRPC span，继承 HTTP parent span）
       |  ├─ keepalive（30s 间隔 / 10s 超时）
       |  └─ retry（UNAVAILABLE 错误指数退避重试）
       |
       |  per-request context timeout（30s）
       |
       └─ 后端 gRPC 服务接收请求 → 处理 → 返回响应
    |
    ⑪ 响应原路返回
       grpc-gateway (Protobuf → JSON)
       → Security Headers
       → CORS Headers
       → otelhttp (结束 span)
       → HTTP Response → 客户端
```

**中间件顺序很重要**：
- Recovery 最先（保证不崩溃）
- Auth 在 grpc-gateway 之前（未认证请求直接拒绝，不浪费后端资源）
- Security Headers 和 CORS 在 Auth 之后（401 响应也需要安全头）

---

## 核心设计

### 1. 统一认证（JWT + Redis 双重校验）

```
请求到达 → 白名单？(register/login/refresh-token)
             │
             ├─ 是：跳过认证，执行限流检查
             │
             └─ 否：提取 Bearer token
                  ↓
             JWT 签名验证（HS256）
                  ↓
             Redis 查询 storemicro:token:{token}
                  ↓
             Redis 返回 userID ↔ JWT claims.userID 匹配？
                  ↓ 是                    ↓ 否
             放行，注入 user_id        返回 401
```

- **白名单路径**：`/api/v1/user/register`、`/api/v1/user/login`、`/api/v1/user/refresh-token`
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

配置项（config.yaml `circuit_breaker`）：

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

### 5. 服务发现与负载均衡

```
gateway → discovery:///user → etcd 查询 → [实例1:6001, 实例2:6001]
                                    ↓
                              round_robin 负载均衡
                                    ↓
                           gRPC 客户端直连后端实例
```

- 使用 Kratos `discovery://` scheme + etcd resolver
- 客户端负载均衡策略：`round_robin`
- gRPC 连接池 + keepalive（30s 间隔 / 10s 超时）
- 自动健康检查 + 重试策略（配置化）

### 6. 优雅退出

```
SIGINT / SIGTERM
    ↓
停止接受新连接
    ↓
WaitForStateChange → 等待 gRPC 在途请求完成
    ↓
关闭 gRPC 连接池
    ↓
关闭 Redis 连接
    ↓
关闭 etcd 客户端
    ↓
关闭 OTel tracer provider (flush spans)
    ↓
进程退出
```

---

## 目录结构

```
gateway/
├── api/
│   ├── user/v1/
│   │   ├── user.proto             # 用户服务 proto（从 user 服务复制，保持同步）
│   │   ├── user.pb.go             # protobuf 消息结构（protoc 生成）
│   │   ├── user_grpc.pb.go        # gRPC 客户端接口（protoc 生成）
│   │   └── user.pb.gw.go          # grpc-gateway HTTP 路由注册（protoc 生成）
│   └── district/v1/
│       ├── district.proto         # 行政区划 proto
│       ├── district.pb.go
│       ├── district_grpc.pb.go
│       └── district.pb.gw.go
│
├── cmd/gateway/
│   ├── main.go                    # 入口：配置→logger→tracer→proxy→HTTP Server→优雅退出
│   ├── proxy.go                   # GatewayProxy：etcd/Redis/gRPC 连接、路由注册、ServeHTTP
│   ├── registry.go                # 服务注册表（新增服务只需加一行）
│   ├── auth.go                    # JWT 认证 + Redis token 校验 + 限流 + CORS + 安全 Header
│   ├── appconfig.go               # YAML 配置加载、${VAR:default} 展开、参数校验
│   ├── circuitbreaker.go          # per-service gobreaker 熔断器 + gRPC unary interceptor
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
| `appconfig.go` | `AppConfig` 结构体、YAML 加载、JWT secret 长度校验 |
| `circuitbreaker.go` | gobreaker per-service 熔断器，gRPC unary client interceptor |

---

## 配置管理

### 配置项

| 配置块 | 说明 |
|--------|------|
| `name` / `version` | 网关标识 |
| `server` | HTTP 监听地址 `:7000`，read 30s / write 60s / idle 120s |
| `etcd` | etcd 服务发现（endpoints、dial_timeout） |
| `jwt` | JWT 密钥（与 user 服务一致，>=32 字符） |
| `redis` | Redis 连接（token 校验 + 限流），pool 20, minIdle 5 |
| `grpc_client` | 负载均衡 round_robin，健康检查 10s，重试 3次，消息大小 4MB |
| `circuit_breaker` | 熔断器参数（MaxRequests/Interval/Timeout/FailureRatio/MinRequests） |
| `tracing` | OTLP endpoint + sampleRate 0.1 |
| `cors` | 允许的 Origin 白名单 |

### 环境变量覆盖

config.yaml 中的 `${VAR:default}` 语法会在加载时展开：

```yaml
redis:
  addr: "${REDIS_HOST:10.211.55.5}:${REDIS_PORT:6379}"
  password: "${REDIS_PASSWORD:}"
```

---

## 启动方式

### 本地开发

```bash
cd gateway
go run ./cmd/gateway/ -conf configs/config.yaml
```

启动流程：

1. `LoadAppConfig()` 加载配置 + `${VAR}` 展开
2. 初始化 zap logger（dev: Console / prod: JSON）
3. 初始化 OTLP tracer（endpoint 为空则 no-op）
4. `NewGatewayProxy()` 创建网关代理：
   - 连接 Redis（带重试）
   - 连接 etcd（带重试）
   - 创建 Kratos etcd registry + gRPC discovery resolver
   - 通过 `discovery:///` 连接各后端服务
   - 循环 `services` 注册所有 grpc-gateway handler
   - 设置熔断拦截器 + OTel stats handler
5. 创建 HTTP Server，绑定 handler chain
6. 监听 SIGINT/SIGTERM → 优雅退出

### Docker

```bash
docker build -t storemicro-gateway .
docker run -p 7000:7000 \
  -e JWT_SECRET=xxx -e REDIS_PASSWORD=xxx -e METRICS_PASSWORD=xxx \
  -e ETCD_ENDPOINT=etcd:2379 \
  storemicro-gateway
```

---

## 健康检查与可观测性

| 端点 | 说明 |
|------|------|
| `GET /healthz` | 存活探针，始终返回 200 |
| `GET /readyz` | 就绪探针，检查 Redis + etcd 连通性 |
| `GET /metrics` | Prometheus 指标（可选 Bearer token 认证） |

追踪链路：

```
Client HTTP Request
  → Gateway HTTP span (otelhttp)
  → Gateway gRPC client span (otelgrpc)
  → Backend gRPC server span (otelgrpc)
  → Backend biz custom span
```

---

## 常用命令

```bash
# === 运行 ===
go run ./cmd/gateway/ -conf configs/config.yaml

# === Proto 代码生成（gateway 需要额外生成 .pb.gw.go） ===
cd gateway
protoc \
  --proto_path=. \
  --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=. --grpc-gateway_opt=paths=source_relative \
  api/user/v1/user.proto

# === 其他 ===
go mod tidy          # 整理依赖
go vet ./...         # 静态检查
```

> **注意**：只有 gateway 需要 `--grpc-gateway_out` 生成 `.pb.gw.go`，user/district 服务不需要。

---

## 扩展：接入新服务

以接入 `order` 服务为例，只需 **两步**：

### 1. 复制 proto 并生成代码

```bash
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
var services = []ServiceRegistration{
    {Name: "user", RegisterHandler: userv1.RegisterUserHandler},
    {Name: "district", RegisterHandler: districtv1.RegisterDistrictHandler},
    // 新增：
    {Name: "order", RegisterHandler: orderv1.RegisterOrderHandler},
}
```

**不需要修改 `proxy.go`**。`NewGatewayProxy` 自动循环 `services` 注册所有服务，etcd 连接、gRPC 连接、熔断器全部自动创建。
