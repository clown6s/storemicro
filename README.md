# StoreMicro — Go 微服务电商后端

基于 **Go 1.26 + Kratos v2.9 + gRPC + etcd** 构建的微服务电商系统，采用 API Gateway 模式统一入口认证，内部服务通过 gRPC 通信，etcd 做服务注册发现。

## 架构总览

```
                        HTTP Client
                            |
                    REST JSON :7000
                            |
                   +--------v--------+
                   |     Gateway     |
                   |  :7000 (HTTP)   |
                   |                 |
                   |  . grpc-gateway |
                   |  . JWT 认证     |
                   |  . Redis 限流   |
                   |  . 熔断降级     |
                   |  . OTel 追踪    |
                   |  . CORS/Security |
                   +--+---------+---+
                      | gRPC     | gRPC
              +-------v---+ +---v-----------+
              |   User    | |   District    |
              |  :6001    | |   :6002       |
              |           | |               |
              | . 注册/登录| | . 行政区查询   |
              | . 登出    | | . 省市区树     |
              | . 用户管理 | |               |
              +---+---+---+ +------+--------+
                  |   |            |
        +---------+   +------------+
        |
   +----v-----+  +----------+  +----------+
   |  MySQL   |  |  Redis   |  |   etcd   |
   |store_micro| |token 校验|  |服务注册   |
   |          |  |单设备登录|  |雪花 node  |
   +----------+  +----------+  +----+-----+
                                   |
                            +------v------+
                            |   Jaeger    |
                            | OTLP gRPC   |
                            |  链路追踪    |
                            +-------------+
```

### 服务端口一览

| 服务 | 业务端口 | 协议 | 健康检查端口 | 说明 |
|------|---------|------|-------------|------|
| **gateway** | `:7000` | HTTP | `:7000`（内置） | API 网关，唯一外部入口 |
| **user** | `:6001` | gRPC | `:9101` | 用户注册/登录/管理 |
| **district** | `:6002` | gRPC | `:9103` | 行政区划省市区树查询 |

### 基础设施端口

| 服务 | 端口 | 说明 |
|------|------|------|
| **MySQL 8.4** | `:3306` | 数据持久化，数据库 `store_micro` |
| **Redis 8.6.2** | `:6379` | Token 校验、限流滑动窗口 |
| **etcd v3.6.9** | 仅内部 | 服务注册发现、雪花 node_id 分配 |

### API 端点一览

所有端点通过 Gateway `:7000` 暴露：

| 方法 | 路径 | 认证 | 限流 | 说明 |
|------|------|------|------|------|
| POST | `/api/v1/user/register` | 白名单 | 3次/分/IP | 用户注册 |
| POST | `/api/v1/user/login` | 白名单 | 5次/分/IP | 用户登录 |
| POST | `/api/v1/user/refresh-token` | 白名单 | 无 | 刷新 Token |
| GET | `/api/v1/user/{id}` | JWT | 无 | 获取用户信息 |
| GET | `/api/v1/user/list` | JWT | 无 | 用户列表（分页） |
| POST | `/api/v1/user/logout` | JWT | 无 | 登出 |
| PUT | `/api/v1/user/password` | JWT | 无 | 修改密码 |
| PUT | `/api/v1/user/{id}` | JWT | 无 | 更新用户信息 |
| DELETE | `/api/v1/user/{id}` | JWT | 无 | 删除用户（软删除） |
| GET | `/api/v1/district/provinceTree` | JWT | 无 | 获取省市区树 |
| GET | `/healthz` | 无 | 无 | 存活检查 |
| GET | `/readyz` | 无 | 无 | 就绪检查 |
| GET | `/metrics` | Bearer Token | 无 | Prometheus 指标 |

## 技术栈

| 组件 | 技术 | 说明 |
|------|------|------|
| 语言 | Go 1.26 | CGO_ENABLED=0 静态编译 |
| 微服务框架 | Kratos v2.9 | gRPC 服务框架，中间件链，Wire 依赖注入 |
| 内部通信 | gRPC + Protobuf v3 | 服务间高性能 RPC |
| 网关 | grpc-gateway v2 | REST JSON <-> gRPC 自动转换，proto 注解驱动路由 |
| 服务发现 | etcd clientv3 | 注册、发现、雪花算法 node_id 分配 |
| 数据库 | MySQL 8.4 | 数据持久化，事务支持，软删除 |
| 缓存 | Redis 8.6.2 | token 双映射（单设备登录），Lua 原子脚本 |
| 认证 | JWT HS256 + Redis | 网关统一鉴权，Redis token 校验实现主动失效 |
| ID 生成 | 雪花算法 | 基于 etcd 分布式锁自动分配 node_id（最多 1024 节点） |
| 限流 | Redis + Lua 滑动窗口 | 按 IP + 路径限流，防暴力破解 |
| 熔断 | gobreaker | 按服务名隔离，半开探测，可配置阈值 |
| 链路追踪 | OpenTelemetry + OTLP gRPC | 全链路 trace_id 传递，Jaeger 可视化 |
| 日志 | zap + lumberjack | 结构化日志，dev/prod 模式，文件轮转 |
| 监控 | Prometheus | /metrics 端点，Bearer token 认证保护 |
| 容器化 | Docker + docker-compose | 多阶段构建，非 root 运行，HEALTHCHECK |
| 依赖注入 | Wire | 编译时依赖注入，类型安全 |

## 项目结构

```
storemicro/                          # Go workspace (go.work)
+-- gateway/                         # API 网关 (:7000)
|   +-- cmd/gateway/
|   |   +-- main.go                  # 启动入口，优雅关闭，gRPC drain
|   +-- internal/
|   |   +-- appconfig/appconfig.go   # 配置加载 + 环境变量展开 + 参数校验
|   |   +-- auth/auth.go             # JWT 认证中间件 + Redis token 校验 + 限流 + CORS
|   |   +-- circuitbreaker/          # gRPC 客户端熔断拦截器（按服务隔离）
|   |   +-- proxy/proxy.go           # grpc-gateway 代理，etcd 服务发现，per-request 超时
|   +-- api/                         # proto 生成代码（user/v1, district/v1）
|   +-- configs/config.yaml          # 网关配置
|   +-- Dockerfile                   # 多阶段构建 + HEALTHCHECK
|
+-- user/                            # 用户服务 (:6001 gRPC, :9101 health)
|   +-- cmd/user/
|   |   +-- main.go                  # 入口，etcd 注册，雪花算法初始化，health sidecar
|   |   +-- wire.go / wire_gen.go    # Wire 依赖注入
|   +-- api/user/v1/                 # proto 定义 + 生成代码
|   +-- internal/
|   |   +-- biz/user.go              # 领域模型 + Usecase（注册/登录/登出/CRUD/校验）
|   |   +-- service/user.go          # gRPC 接口实现，错误码映射
|   |   +-- data/
|   |   |   +-- data.go              # MySQL/Redis 连接池初始化
|   |   |   +-- user.go              # Repository 实现（事务注册/更新，参数化查询）
|   |   +-- server/grpc.go           # gRPC Server + 中间件链
|   |   +-- conf/                    # 配置结构（proto 生成）
|   |   +-- pkg/
|   |       +-- snowflake/snowflake.go  # 雪花 ID（etcd 分布式锁，租约续约）
|   |       +-- jwt/jwt.go              # JWT HS256 工具
|   |       +-- redis/redis.go          # Redis Lua CAS 原子脚本
|   |       +-- log/zap.go              # Kratos log -> zap 桥接
|   +-- configs/config.yaml
|   +-- Dockerfile
|
+-- district/                        # 行政区服务 (:6002 gRPC, :9103 health)
|   +-- cmd/district/
|   |   +-- main.go                  # 入口，etcd 注册，health sidecar
|   |   +-- wire.go / wire_gen.go    # Wire 依赖注入
|   +-- api/district/v1/             # proto 定义 + 生成代码
|   +-- internal/
|   |   +-- biz/district.go          # 领域模型 + DistrictUseCase
|   |   +-- service/district.go      # gRPC + 扁平转树 + 环检测
|   |   +-- data/
|   |   |   +-- data.go              # MySQL 连接池初始化
|   |   |   +-- district.go          # 递归 CTE 查询
|   |   +-- server/grpc.go           # gRPC Server + 中间件链
|   |   +-- conf/                    # 配置结构
|   +-- configs/config.yaml
|   +-- Dockerfile
|
+-- pkg/                             # 共享工具包（独立 Go modules）
|   +-- tracing/tracing.go           # OTLP gRPC exporter + TracerProvider
|   +-- zaplogger/zaplogger.go       # 共享 zap logger（Kratos 桥接，dev/prod 模式）
|   +-- retry/retry.go               # 通用重试（指数退避 + 随机抖动）
|   +-- dbutil/dbutil.go             # 共享 MySQL 连接池配置 + DSN 校验
|   +-- cmdutil/cmdutil.go           # ResolveConfDir + MetricsBearerAuth + LoadDotenv
|   +-- envexpand/envexpand.go       # Kratos config.Source（环境变量展开）
|   +-- health/health.go             # 通用健康检查 HTTP server
|   +-- strutil/mask.go              # 手机号/邮箱脱敏
|
+-- third_party/google/              # protobuf 依赖
+-- docker-compose.yml               # 一键部署
+-- Makefile                         # build/generate/wire/docker/lint/clean
+-- go.work                          # Go workspace 定义
```

### Kratos 三层架构

每个业务服务遵循 **Service -> Biz -> Data** 三层架构：

```
+-------------------------------------------------------------+
|  service/  -- gRPC 接口层                                    |
|  . 实现 proto 定义的服务接口                                 |
|  . 参数转换：proto message <-> biz 领域模型                  |
|  . 错误码映射：biz error -> gRPC status code                 |
+-------------------------------------------------------------+
|  biz/  -- 业务逻辑层                                         |
|  . 领域模型定义（User, District）                            |
|  . Repo 接口定义（由 data 层实现）                           |
|  . Usecase：注册校验、密码强度、登录验证、分页参数标准化      |
+-------------------------------------------------------------+
|  data/  -- 数据访问层                                        |
|  . 实现 biz 层定义的 Repo 接口                              |
|  . MySQL 参数化查询（防 SQL 注入）                           |
|  . 事务操作（SELECT FOR UPDATE、唯一性检查）                  |
+-------------------------------------------------------------+
```

## 请求处理链路（完整流程）

以 `POST /api/v1/user/login` 为例，展示一个请求从到达 Gateway 到返回响应的完整链路：

```
① HTTP POST /api/v1/user/login
       │
②     limitRequestBody（10MB 限制，超过返回 413）
       │
③     RequestIDMiddleware（生成/传递 X-Request-Id header）
       │
④     /healthz、/readyz、/metrics？→ 是：直接响应，跳过后续所有中间件
       │
⑤     otelhttp（创建 OpenTelemetry HTTP span，注入 trace_id）
       │
⑥     RecoverMiddleware（panic 恢复，记录堆栈，返回 500）
       │
⑦     CORSMiddleware（检查 Origin 白名单，处理 OPTIONS 预检，Vary: Origin）
       │
⑧     SecurityHeadersMiddleware（注入 8 个安全响应头）
       │
⑨     jwtAuth 中间件：
       ├─ 路径在白名单（register/login/refresh-token）？→ 跳过 JWT 校验
       │   └─ 执行限流检查（Redis ZSET + Lua 滑动窗口）
       │       login: 5次/分/IP，register: 3次/分/IP
       │       超限返回 429
       │
       └─ 非白名单路径 → 提取 Authorization: Bearer {token}
           ├─ Bearer 缺失/格式错误 → 401
           ├─ Token 长度 > 4096 → 401
           ├─ JWT HS256 签名验证（issuer、audience、exp、sub）
           ├─ 提取 user_id from claims
           ├─ Redis GET storemicro:token:{token} → 获取存储的 userID
           │   ├─ Redis 不可用 → 503（fail-closed）
           │   ├─ token 不存在 → 401
           │   └─ userID 不匹配 → 401
           └─ 注入 user_id 到请求 metadata，放行
       │
⑩     grpc-gateway mux（JSON → Protobuf 反序列化）
       │
⑪     gRPC Client 调用：
       ├─ discovery:///user（etcd 服务发现，获取实例列表）
       ├─ round_robin 负载均衡
       ├─ gRPC interceptor 链：
       │   ├─ circuitBreaker（gobreaker 按服务熔断）
       │   ├─ otelgrpc（gRPC 链路追踪 span）
       │   ├─ keepalive（30s 间隔 / 10s 超时）
       │   └─ retry（UNAVAILABLE 错误指数退避重试）
       └─ per-request context timeout（30s）
       │
⑫     User 服务 (:6001) 接收 gRPC 请求：
       ├─ gRPC server interceptor 链：
       │   ├─ customRecovery（panic 恢复 + 完整堆栈记录）
       │   ├─ rateLimitMiddleware（IP 令牌桶，Login/Register: 10次/秒突发30）
       │   ├─ tracing.Server()（Kratos OTel）
       │   └─ logging.Server(logger)（结构化日志）
       │
       ├─ service/user.go Login()
       │   └─ 调用 biz.UserUsecase.Login()
       │
       ├─ biz/user.go Login()
       │   ├─ 手机号格式校验 ^1[3-9]\d{9}$
       │   ├─ GetByPhone(phone) → 查询用户是否存在
       │   ├─ bcrypt.CompareHashAndPassword（密码比对）
       │   │   └─ 用户不存在 → 执行 dummy hash（时间恒定防枚举）
       │   ├─ JWT.GenerateToken(userID) → 生成 access_token + refresh_token
       │   └─ Redis Lua 原子操作（单设备登录）：
       │       ├─ 读取旧 token: user_token:{userID} → oldToken
       │       ├─ DEL token:{oldToken}
       │       ├─ SET token:{newToken} → userID
       │       └─ SET user_token:{userID} → newToken
       │
       └─ 响应原路返回
           {token: "xxx", refreshToken: "xxx", data: {userInfo}}
       │
⑬     grpc-gateway 将 Protobuf 响应转回 JSON
       │
⑭     通过所有中间件原路返回（CORS、Security Headers 等）
       │
⑮     HTTP Response → 客户端
```

## 关键设计

### 网关统一认证

所有外部请求通过 Gateway 入口，内部 gRPC 服务不做重复鉴权：

1. **JWT 签名校验** -- 验证签名算法（HS256）、issuer、claims 完整性
2. **Redis token 存在性校验** -- 支持 token 主动失效（踢人、改密）
3. **userID 一致性校验** -- Redis 中存储的 userID 与 JWT claims 交叉验证
4. **白名单放行** -- `register`、`login`、`refresh-token` 免认证，但登录/注册有独立限流
5. **Fail-closed** -- Redis 不可用时拒绝认证，返回 503
6. **Token 长度限制** -- 超 4096 字节直接拒绝

### 单设备登录

通过 Redis 双映射实现同一时间只允许一个设备在线：

```
登录：
1. Lua 脚本 CAS 原子操作：
   - 读取旧 token:  user_token:{userID} -> oldToken
   - 删除旧映射:    DEL token:{oldToken}
   - 写入新映射:    SET token:{newToken} -> userID
   - 写入反向映射:  SET user_token:{userID} -> newToken
2. JWT 生成（仅含 user_id，不含 PII）
3. 登录成功返回 token

登出：
- Lua 脚本 CAS 原子删除双向映射（对比 token 防并发竞态）

效果：新登录自动踢掉旧设备，旧 token 在网关层被 Redis 校验拦截返回 401
```

### 雪花算法 ID 生成

- 基于 etcd 分布式事务锁自动分配 node_id（0-1023），无需手动配置
- 租约机制（30s TTL）：实例下线或崩溃后 node_id 自动释放
- 后台 goroutine 续约保活，KeepAlive 错误时立即禁用 GenID 并自动重试抢锁

### 安全防护

| 层面 | 措施 |
|------|------|
| **认证** | JWT HS256 + issuer 校验 + Redis 双重校验，fail-closed |
| **限流** | Redis Lua 滑动窗口，按 IP + 路径限流（登录 5次/分，注册 3次/分） |
| **密码** | bcrypt 加密，强度校验（8-72位，含大小写/数字），timing-safe dummy hash |
| **SQL 注入** | 全参数化查询，LIKE 通配符转义，ORDER BY 白名单 |
| **HTTP 安全** | X-Content-Type-Options, X-Frame-Options, CSP, HSTS 等 8 个 Header |
| **CORS** | 白名单机制，Vary: Origin，禁止通配符 * |
| **Slowloris** | Gateway ReadHeaderTimeout 10s，Health server ReadHeaderTimeout 5s |
| **请求体限制** | Gateway 限制 10MB |
| **gRPC 超时** | Per-request 30s context timeout |
| **Metrics** | Prometheus 端点 Bearer token 认证保护 |
| **容器安全** | 非 root 用户运行（appuser:10001），多阶段构建 |

### 熔断降级

Gateway 对后端 gRPC 调用实现服务级熔断（gobreaker）：

- **按服务名隔离**：每个 gRPC 服务独立熔断器
- **三态切换**：关闭 -> 打开 -> 半开
- **可配置阈值**：失败率 0-1、最小请求数、熔断时长、半开探测并发
- **gRPC 方法重试**：指数退避重试 UNAVAILABLE 错误

## 依赖环境

| 工具 | 版本要求 | 用途 |
|------|----------|------|
| Go | >= 1.21 | 编译运行 |
| protoc | - | 编译 .proto 文件 |
| protoc-gen-go | latest | 生成 Go protobuf 代码 |
| protoc-gen-go-grpc | latest | 生成 gRPC 代码 |
| protoc-gen-grpc-gateway | latest | 生成 HTTP 网关代码 |
| wire | latest | 编译时依赖注入 |
| etcd | >= 3.5 | 服务注册与发现 |
| MySQL | >= 5.7 | 数据持久化 |
| Redis | >= 6.0 | token 校验 + 限流 |
| Jaeger | - | 链路追踪（OTLP gRPC :4317） |

**安装 protoc 插件**

```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@latest
go install github.com/google/wire/cmd/wire@latest
```

## 快速启动

### 方式一：本地开发

```bash
# 1. 启动依赖服务（etcd, MySQL, Redis）
docker compose up -d mysql redis etcd

# 2. 修改各服务的 configs/config.yaml，填入本地连接地址

# 3. 启动 user 服务
cd user && go run ./cmd/user/ -conf configs/config.yaml

# 4. 启动 district 服务
cd district && go run ./cmd/district/ -conf configs/config.yaml

# 5. 启动 gateway（最后启动，依赖其他服务注册到 etcd）
cd gateway && go run ./cmd/gateway/ -conf configs/config.yaml
```

### 方式二：Docker Compose 一键部署

```bash
# 1. 修改 docker-compose.yml 中的密码和密钥（YAML 锚点 x-password, x-jwt-secret）
# 2. 启动全部服务（docker-compose 依赖 service_healthy 确保启动顺序）
docker compose up -d --build

# 3. 查看状态
docker compose ps
docker compose logs -f gateway
```

### 导入数据库

```bash
# 本地
mysql -u root -p store_micro < store_micro.sql

# Docker
docker exec -i storemicro-mysql mysql -u root -p'密码' < store_micro.sql
```

## 添加新服务

以添加 `order`（订单）服务为例，gRPC 端口 `:6003`，健康检查端口 `:9104`。

### 第一步：创建服务目录结构

```bash
mkdir -p order/api/order/v1
mkdir -p order/cmd/order
mkdir -p order/internal/{biz,service,data,server,conf}
mkdir -p order/configs
```

目录结构：

```
order/
+-- api/order/v1/
|   +-- order.proto              # gRPC 接口定义
+-- cmd/order/
|   +-- main.go                  # 启动入口
|   +-- wire.go                  # Wire 声明
|   +-- wire_gen.go              # Wire 生成（自动）
+-- configs/
|   +-- config.yaml              # 服务配置
+-- internal/
|   +-- biz/
|   |   +-- order.go             # 领域模型 + Repo 接口 + UseCase + ProviderSet
|   +-- service/
|   |   +-- order.go             # gRPC 方法实现 + 错误码映射 + ProviderSet
|   +-- data/
|   |   +-- data.go              # MySQL 连接池初始化 + ProviderSet
|   |   +-- order.go             # Repo 接口实现
|   +-- server/
|   |   +-- grpc.go              # gRPC Server + 中间件链
|   +-- conf/
|       +-- conf.proto           # 配置结构定义
|       +-- conf.pb.go           # 配置结构生成
+-- Dockerfile
+-- go.mod
+-- go.sum
```

### 第二步：初始化 Go Module

```bash
cd order
go mod init order
```

如果需要使用共享 pkg，编辑 `../go.work` 添加：

```go
use (
    ./order
    ./pkg/dbutil        # 如果需要 MySQL
    ./pkg/tracing       # 如果需要链路追踪
    ./pkg/zaplogger     # 如果需要日志
    ./pkg/cmdutil       # 如果需要配置工具
    ./pkg/health        # 如果需要健康检查
)
```

### 第三步：编写 Proto 定义

编辑 `order/api/order/v1/order.proto`：

```protobuf
syntax = "proto3";

package api.order.v1;

option go_package = "order/api/order/v1;orderv1";

import "google/api/annotations.proto";

service Order {
  // 创建订单
  rpc CreateOrder (CreateOrderReq) returns (CreateOrderResp) {
    option (google.api.http) = {
      post: "/api/v1/order"
      body: "*"
    };
  }
  // 获取订单
  rpc GetOrder (GetOrderReq) returns (GetOrderResp) {
    option (google.api.http) = {
      get: "/api/v1/order/{id}"
    };
  }
  // 订单列表
  rpc ListOrders (ListOrdersReq) returns (ListOrdersResp) {
    option (google.api.http) = {
      get: "/api/v1/order/list"
    };
  }
}

message CreateOrderReq {
  int64 user_id = 1;
  // ...其他字段
}

message CreateOrderResp {
  int32 code = 1;
  string message = 2;
  OrderInfo data = 3;
}

// ...其他消息定义
```

### 第四步：生成代码

```bash
cd order
protoc \
  --proto_path=. \
  --proto_path=../third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  api/order/v1/order.proto
```

### 第五步：实现各层代码

以 user 服务为模板，逐层实现：

#### 5.1 biz 层（业务逻辑）

```go
// order/internal/biz/order.go

// 1. 定义领域模型
type Order struct {
    ID        int64
    UserID    int64
    // ...
}

// 2. 定义 Repo 接口（依赖倒置）
type OrderRepo interface {
    CreateOrder(ctx context.Context, order *Order) (*Order, error)
    GetOrder(ctx context.Context, id int64) (*Order, error)
    ListOrders(ctx context.Context, userID int64, page, pageSize int32) ([]*Order, int64, error)
}

// 3. 定义 UseCase
type OrderUseCase struct {
    repo OrderRepo
    log  *zap.Logger
}

func NewOrderUseCase(repo OrderRepo, logger log.Logger) *OrderUseCase {
    return &OrderUseCase{repo: repo, log: logger.(*zap.Logger)}
}

// 4. 实现业务方法
func (uc *OrderUseCase) CreateOrder(ctx context.Context, order *Order) (*Order, error) {
    return uc.repo.CreateOrder(ctx, order)
}

// 5. Wire ProviderSet
var ProviderSet = wire.NewSet(NewOrderUseCase)
```

#### 5.2 data 层（数据访问）

```go
// order/internal/data/data.go
// MySQL 连接池初始化（参考 user/internal/data/data.go）
// 注意 MaxIdleConns <= MaxOpenConns 防御性校验

// order/internal/data/order.go
// 实现 biz.OrderRepo 接口
// 全部使用参数化查询防止 SQL 注入
// 需要 MySQL 事务时使用 tx，参考 user 的 RegisterTx/UpdateTx

var ProviderSet = wire.NewSet(NewData, NewOrderRepo)
```

#### 5.3 service 层（gRPC 接口）

```go
// order/internal/service/order.go
// 1. 实现 proto 定义的 OrderServer 接口
// 2. proto message <-> biz 领域模型转换
// 3. biz error -> gRPC status code 映射
// 4. 成功响应统一 Code: 200

var ProviderSet = wire.NewSet(NewOrderService)
```

#### 5.4 server 层（gRPC 服务器）

```go
// order/internal/server/grpc.go

func NewGRPCServer(
    order *orderService.OrderService,
) *grpc.Server {
    srv := grpc.NewServer(
        grpc.ChainUnaryInterceptor(
            recovery.Recovery(),
            tracing.Server(),
            logging.Server(logger),
        ),
    )
    orderv1.RegisterOrderServer(srv, order)
    return srv
}

var ProviderSet = wire.NewSet(NewGRPCServer)
```

#### 5.5 Wire 依赖注入

```go
// order/cmd/order/wire.go
// +build wireinject

func initApp(*conf.Data, *conf.Registry, log.Logger) (*kratos.App, error) {
    panic(wire.Build(
        data.ProviderSet,
        biz.ProviderSet,
        service.ProviderSet,
        server.ProviderSet,
        NewApp,
    ))
}
```

```bash
cd order/cmd/order && wire
```

#### 5.6 启动入口

```go
// order/cmd/order/main.go
// 参考 user/cmd/user/main.go：
// 1. 加载配置（envexpand + config.yaml）
// 2. 初始化 zap logger
// 3. 初始化 OTLP tracer
// 4. 连接 etcd → 注册服务名 "order"（或 "storemicro-order"）
// 5. Wire 注入 → 启动 gRPC Server (:6003)
// 6. 启动 health sidecar (:9104)
// 7. 监听 SIGINT/SIGTERM → 优雅退出
```

### 第六步：编写配置文件

```yaml
# order/configs/config.yaml
name: storemicro-order
version: 1.0.0

server:
  grpc:
    addr: 0.0.0.0:6003
    timeout: 10s

data:
  database:
    driver: mysql
    source: "${DB_USER:root}:${DB_PASSWORD:}@tcp(${DB_HOST:mysql:3306})/${DB_DATABASE:store_micro}?parseTime=true&loc=Asia%2FShanghai"
    max_open_conns: 100
    max_idle_conns: 25
    conn_max_lifetime: 3600s

registry:
  etcd:
    endpoints:
      - "${ETCD_ENDPOINT:10.211.55.5:2379}"
    timeout: 10s

jwt:
  secret: "${JWT_SECRET:change-me-to-a-random-string-at-least-32-chars}"
  expire: 86400s

logger:
  level: info
  mode: prod

tracing:
  endpoint: "${OTLP_ENDPOINT:}"
  sample_rate: 0.1
```

### 第七步：编写 Dockerfile

```dockerfile
FROM golang:1.26-alpine AS builder

# Go module proxy
ENV GOPROXY=https://goproxy.cn,https://goproxy.io,https://proxy.golang.org,direct
ENV GOSUMDB=sum.golang.org
ENV GONOSUMDB=off
ENV CGO_ENABLED=0

WORKDIR /app

# 依赖 + go.work
COPY order/go.mod order/go.sum ./
COPY pkg/ ./pkg/
RUN printf 'go 1.26.1\nuse (\n  .\n  ./pkg/dbutil\n  ./pkg/tracing\n  ./pkg/zaplogger\n  ./pkg/cmdutil\n)\n' > go.work
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

# 源码
COPY order/ ./

# 编译
RUN --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags "-s -w -buildid=" -o /order ./cmd/order/

FROM alpine:3.20
RUN apk add --no-cache ca-certificates wget

USER appuser
WORKDIR /app
COPY --from=builder /order .

EXPOSE 6003 9104

HEALTHCHECK --interval=10s --timeout=5s --retries=3 --start-period=30s \
    CMD ["wget", "-qO-", "http://localhost:9104/readyz"]

ENTRYPOINT ["/order"]
```

### 第八步：接入 Gateway

#### 8.1 复制 proto 到 gateway

```bash
# 复制 proto 和生成的 Go 文件
mkdir -p gateway/api/order/v1
cp order/api/order/v1/order.proto gateway/api/order/v1/
cp order/api/order/v1/order.pb.go gateway/api/order/v1/
cp order/api/order/v1/order_grpc.pb.go gateway/api/order/v1/
```

#### 8.2 在 gateway 生成 grpc-gateway 代码

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

#### 8.3 在 gateway 注册服务

编辑 `gateway/cmd/gateway/registry.go`，在 `services` 切片中添加一行：

```go
var services = []ServiceRegistration{
    {
        Name:            "user",
        RegisterHandler: userv1.RegisterUserHandler,
    },
    {
        Name:            "district",
        RegisterHandler: districtv1.RegisterDistrictHandler,
    },
    // 新增：
    {
        Name:            "order",
        RegisterHandler: orderv1.RegisterOrderHandler,
    },
}
```

**不需要修改 `proxy.go`**。`NewGatewayProxy` 自动循环 `services` 注册所有服务路由，etcd 连接、gRPC 连接、熔断器全部自动创建。

### 第九步：更新 docker-compose.yml

在 `services` 下添加 order 服务：

```yaml
  order:
    build:
      context: .
      dockerfile: order/Dockerfile
    container_name: storemicro-order
    ports:
      - "6003:6003"
      - "9104:9104"
    environment:
      TZ: *timezone
      DB_PASSWORD: *password
      DB_HOST: mysql
      DB_PORT: "3306"
      ETCD_ENDPOINT: etcd:2379
      JWT_SECRET: *jwt-secret
      METRICS_PASSWORD: *metrics-password
      LOG_LEVEL: info
      LOG_MODE: prod
      GOGC: "100"
      GOMEMLIMIT: "400MiB"
    tmpfs:
      - /tmp:rw,noexec,nosuid,size=64m
    depends_on:
      mysql:
        condition: service_healthy
      etcd:
        condition: service_healthy
    restart: always
    stop_grace_period: *stop-grace
    deploy:
      resources:
        limits:
          cpus: "1"
          memory: 512M
          pids: 64
        reservations:
          memory: 128M
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:9104/readyz"]
      interval: 10s
      timeout: 5s
      retries: 3
      start_period: 30s
    logging: *logging
    ulimits:
      nofile:
        soft: 65536
        hard: 65536
    networks:
      - app
      - internal
```

### 第十步：更新 Makefile

在 `generate` target 中添加 proto 同步：

```makefile
sync-proto:
	cp user/api/user/v1/user.proto gateway/api/user/v1/
	cp district/api/district/v1/district.proto gateway/api/district/v1/
	cp order/api/order/v1/order.proto gateway/api/order/v1/  # 新增
```

### 命名替换速查

| 位置 | user | order（替换后） |
|------|------|---------------|
| go.mod module | `user` | `order` |
| go_package | `user/api/user/v1` | `order/api/order/v1` |
| etcd 注册名 | `storemicro-user` | `storemicro-order` |
| gRPC 端口 | `:6001` | `:6003` |
| health 端口 | `:9101` | `:9104` |
| conf.proto package | `user.conf` | `order.conf` |

## 常用命令速查

```bash
# === 编译全部 ===
make build

# === Proto 全量生成 ===
make generate

# === Wire 全量生成 ===
make wire

# === Docker ===
make docker-up       # docker compose up -d
make docker-down     # docker compose down

# === 其他 ===
make lint            # go vet
make clean           # 清理 bin/
make test            # 运行测试
```

## 共享包说明

| 包 | 说明 |
|---|------|
| `pkg/tracing` | OTLP gRPC exporter + TracerProvider 初始化，endpoint 为空时使用 no-op |
| `pkg/zaplogger` | 共享 zap logger，Kratos log 桥接，dev(Console)/prod(JSON) 模式 |
| `pkg/retry` | 通用重试（指数退避 + Equal Jitter），context 取消立即停止 |
| `pkg/dbutil` | MySQL 连接池，DSN 格式校验，MaxIdleConns <= MaxOpenConns 防御 |
| `pkg/cmdutil` | ResolveConfDir + MetricsBearerAuth + LoadDotenv |
| `pkg/envexpand` | Kratos config.Source，YAML 中 ${VAR:default} 单次正则展开 |
| `pkg/health` | 通用健康检查 HTTP server（/healthz, /readyz, /metrics） |
| `pkg/strutil` | 手机号/邮箱脱敏（138\*\*\*\*5678, u\*\*\*@example.com） |
