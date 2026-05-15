# StoreMicro - Go 微服务电商后端

基于 **Go 1.26 + Kratos v2.9 + gRPC + etcd** 构建的微服务电商系统，采用 API Gateway 模式统一入口认证，内部服务通过 gRPC 通信，etcd 做服务注册发现。

## 架构总览

```
                        HTTP Client
                            │
                     REST JSON :8080
                            │
                   ┌────────▼────────┐
                   │     Gateway     │
                   │  :8080 (HTTP)   │
                   │                 │
                   │  · grpc-gateway │
                   │  · JWT 认证     │
                   │  · Redis 限流   │
                   │  · 熔断降级     │
                   │  · OTel 追踪    │
                   │  · CORS/Safety  │
                   └──┬──────────┬───┘
                      │ gRPC     │ gRPC
              ┌───────▼───┐ ┌───▼──────────┐
              │   User    │ │   District   │
              │  :9001    │ │   :9003      │
              │           │ │              │
              │ · 注册/登录│ │ · 行政区查询  │
              │ · 登出    │ │ · 省市区树    │
              │ · 用户管理 │ │              │
              └───┬───┬───┘ └──────┬───────┘
                  │   │            │
        ┌─────────┘   └────────────┘
        │
   ┌────▼─────┐  ┌──────────┐  ┌──────────┐
   │  MySQL   │  │  Redis   │  │   etcd   │
   │store_micro│ │token 校验│  │服务注册   │
   │          │  │单设备登录│  │雪花 node  │
   └──────────┘  └──────────┘  └────┬─────┘
                                   │
                            ┌──────▼──────┐
                            │   Jaeger    │
                            │ OTLP gRPC   │
                            │  链路追踪    │
                            └─────────────┘
```

| 模块 | 职责 | 端口 | 协议 |
|------|------|------|------|
| **gateway** | HTTP → gRPC 反向代理，JWT 统一认证，Redis 限流，熔断降级，链路追踪 | `:8080` | HTTP |
| **user** | 用户注册、登录（手机号+密码）、登出、查询、列表（分页） | `:9001` | gRPC |
| **district** | 行政区划查询，省市区树形结构 | `:9003` | gRPC |

## 技术栈

| 组件 | 技术 | 说明 |
|------|------|------|
| 语言 | Go 1.26 | |
| 微服务框架 | Kratos v2.9 | gRPC 服务框架，中间件链，Wire 依赖注入 |
| 内部通信 | gRPC + Protobuf v3 | 服务间高性能 RPC |
| 网关 | grpc-gateway v2 | REST JSON ↔ gRPC 自动转换，proto 注解驱动路由 |
| 服务发现 | etcd clientv3 | 注册、发现、雪花算法 node_id 分配 |
| 数据库 | MySQL 8.0 | 数据持久化，事务支持，软删除 |
| 缓存 | Redis 7 | token 双映射（单设备登录），Lua 原子脚本 |
| 认证 | JWT HS256 + Redis | 网关统一鉴权，Redis token 校验实现主动失效 |
| ID 生成 | 雪花算法 | 基于 etcd 分布式锁自动分配 node_id（最多 1024 节点） |
| 限流 | Redis + Lua 滑动窗口 | 按 IP + 路径限流，防暴力破解 |
| 熔断 | gobreaker | 按服务名隔离，半开探测，可配置阈值 |
| 链路追踪 | OpenTelemetry + OTLP gRPC | 全链路 trace_id 传递，Jaeger 可视化 |
| 日志 | zap + lumberjack | 结构化日志，dev/prod 模式，文件轮转 |
| 监控 | Prometheus | `/metrics` 端点，Bearer token 认证保护 |
| 容器化 | Docker + docker-compose | 多阶段构建，非 root 运行 |
| 依赖注入 | Wire | 编译时依赖注入，类型安全 |

## 项目结构

```
storemicro/                          # Go workspace (go.work)
├── gateway/                         # API 网关（纯 Go 结构体配置，非 proto）
│   ├── cmd/gateway/
│   │   ├── main.go                  # 启动入口，优雅关闭，.env 加载
│   │   ├── proxy.go                 # grpc-gateway 代理，etcd 服务发现，gRPC 连接池
│   │   ├── registry.go              # 服务注册表（新增服务加一行）
│   │   ├── auth.go                  # JWT 认证中间件 + Redis token 校验 + 限流 + CORS
│   │   ├── appconfig.go             # 配置加载 + 环境变量展开（${VAR:default}）
│   │   ├── circuitbreaker.go        # gRPC 客户端熔断拦截器（按服务隔离）
│   │   ├── logger.go                # （已在 pkg/zaplogger 中）
│   │   └── ...
│   ├── api/                         # proto 生成代码（user/v1, district/v1）
│   ├── configs/config.yaml          # 网关配置（所有密钥通过环境变量注入）
│   ├── .env.example                 # 环境变量模板
│   └── Dockerfile                   # 多阶段构建
│
├── user/                            # 用户服务（Kratos 标准结构）
│   ├── cmd/user/
│   │   ├── main.go                  # 启动入口，etcd 注册，雪花算法初始化，健康检查
│   │   ├── wire.go / wire_gen.go    # Wire 依赖注入
│   │   └── ...
│   ├── api/user/v1/                 # proto 定义 + 生成代码
│   ├── internal/
│   │   ├── biz/user.go              # 领域模型 + Usecase（注册/登录/登出/CRUD/校验）
│   │   ├── service/user.go          # gRPC 接口实现，错误码映射
│   │   ├── data/
│   │   │   ├── data.go              # MySQL 连接池初始化
│   │   │   └── user.go              # Repository 实现（事务注册/更新，参数化查询）
│   │   ├── server/grpc.go           # gRPC 服务器（中间件链）
│   │   ├── conf/                    # 配置结构（proto 生成）
│   │   └── pkg/
│   │       ├── snowflake/snowflake.go  # 雪花 ID 生成器（etcd 分布式锁，租约续约）
│   │       ├── jwt/jwt.go              # JWT 工具（HS256，标准 claims）
│   │       ├── redis/redis.go          # Redis 客户端封装 + Lua 原子脚本
│   │       └── log/zap.go              # Kratos log → zap 桥接
│   ├── configs/config.yaml
│   └── Dockerfile
│
├── district/                        # 行政区服务（结构同 user）
│   ├── internal/
│   │   ├── biz/district.go          # 行政区领域模型
│   │   ├── data/district.go         # 递归 CTE 查询省市区树
│   │   ├── service/district.go      # 树构建 + 排序
│   │   └── ...
│   └── Dockerfile
│
├── pkg/                             # 共享工具包（独立 Go modules）
│   ├── tracing/tracing.go           # OTLP gRPC exporter + TracerProvider
│   ├── zaplogger/zaplogger.go       # 共享 zap logger（Kratos 桥接 + dev/prod 模式）
│   ├── retry/retry.go               # 通用重试（指数退避 + 随机抖动）
│   ├── dbutil/dbutil.go             # 共享数据库连接池配置
│   ├── envexpand/envexpand.go       # Kratos config.Source（环境变量展开）
│   └── strutil/mask.go              # 手机号脱敏工具
│
├── third_party/                     # protobuf 依赖（google/api + google/protobuf）
├── docker-compose.yml               # 一键部署（MySQL + Redis + etcd + 三服务）
├── go.work                          # Go workspace 定义
└── .gitignore
```

### Kratos 三层架构

每个业务服务遵循 **Service → Biz → Data** 三层架构：

```
┌─────────────────────────────────────────────────────────────┐
│  service/  — gRPC 接口层                                     │
│  · 实现 proto 定义的服务接口                                  │
│  · 参数转换：proto message ↔ biz 领域模型                     │
│  · 错误码映射：biz error → gRPC status code                   │
│  · 手机号脱敏输出                                            │
├─────────────────────────────────────────────────────────────┤
│  biz/  — 业务逻辑层                                          │
│  · 领域模型定义（User, District）                             │
│  · Repo 接口定义（由 data 层实现）                            │
│  · Usecase：注册校验、密码强度、登录验证、分页参数标准化         │
│  · 不依赖任何外部基础设施                                     │
├─────────────────────────────────────────────────────────────┤
│  data/  — 数据访问层                                         │
│  · 实现 biz 层定义的 Repo 接口                               │
│  · MySQL 参数化查询（防 SQL 注入）                            │
│  · 事务操作（SELECT FOR UPDATE、唯一性检查）                   │
│  · 连接池管理、重试连接                                      │
└─────────────────────────────────────────────────────────────┘
```

## 关键设计

### 网关统一认证

所有外部请求通过 Gateway 入口，内部 gRPC 服务不做重复鉴权：

1. **JWT 签名校验** — 验证签名算法（HS256）和 claims 完整性
2. **Redis token 存在性校验** — 支持 token 主动失效（踢人、改密）
3. **userID 一致性校验** — Redis 中存储的 userID 与 JWT claims 交叉验证
4. **白名单放行** — `register`、`login`、`/healthz` 免认证，但登录/注册有独立限流
5. **Fail-closed** — Redis 不可用时拒绝认证，返回 503

### 单设备登录

通过 Redis 双映射实现同一时间只允许一个设备在线：

```
登录流程：
1. Lua 脚本原子操作：
   - 读取旧 token:  user_token:{userID} → oldToken
   - 删除旧映射:    DEL token:{oldToken}
   - 写入新映射:    SET token:{newToken} → userID
   - 写入反向映射:  SET user_token:{userID} → newToken
2. JWT 生成（仅含 user_id，不含 PII）
3. 登录成功返回 token

登出流程：
- Lua 脚本原子删除双向映射

效果：新登录自动踢掉旧设备，旧 token 在网关层被 Redis 校验拦截返回 401
```

### 雪花算法 ID 生成

- 基于 etcd 分布式事务锁自动分配 node_id（0-1023），无需手动配置
- 租约机制（30s TTL）：实例下线或崩溃后 node_id 自动释放
- 后台 goroutine 续约保活，租约丢失时自动尝试重新抢锁
- 优雅关闭：通过 `context.Cancel` 通知后台 goroutine 退出，避免 `time.Sleep` 阻塞

### 安全防护

| 层面 | 措施 |
|------|------|
| **认证** | JWT HS256 + Redis 双重校验，fail-closed 策略 |
| **限流** | Redis Lua 滑动窗口，按 IP + 路径限流（登录 5次/分，注册 3次/分） |
| **密码** | bcrypt 加密，强度校验（8-72位，含大小写和数字），timing-safe dummy hash 防枚举 |
| **SQL 注入** | 全参数化查询，LIKE 通配符转义 |
| **HTTP 安全** | X-Content-Type-Options, X-Frame-Options, CSP, HSTS, Referrer-Policy, Permissions-Policy |
| **CORS** | 白名单机制，`Vary: Origin` 防缓存投毒，禁止通配符 `*` |
| **密钥管理** | 所有密钥通过环境变量注入，`.env` 文件 gitignore，config.yaml 中无明文 |
| **请求体限制** | Gateway 限制 10MB，防止 OOM |
| **敏感信息** | 手机号脱敏输出，密码 hash 不跨层传播，健康检查不暴露内部错误详情 |
| **Metrics** | Prometheus 端点 Bearer token 认证保护 |
| **容器安全** | 非 root 用户运行，多阶段构建最小镜像 |

### 熔断降级

Gateway 对后端 gRPC 调用实现服务级熔断（gobreaker）：

- **按服务名隔离**：每个 gRPC 服务独立熔断器
- **三态切换**：关闭 → 打开 → 半开
- **可配置阈值**：失败率、最小请求数、熔断时长、半开探测并发
- **gRPC 方法重试**：指数退避重试 UNAVAILABLE 错误

### 可观测性

```
请求链路追踪（OTLP gRPC → Jaeger）：

Client → Gateway → etcd(服务发现) → User/District
  │           │                        │
  └── trace_id 传递 ──────────────────┘

Jaeger UI 中可看到完整调用链：
  storemicro-gateway → storemicro-user → MySQL
  storemicro-gateway → storemicro-district → MySQL
```

- **链路追踪**：OpenTelemetry SDK + OTLP gRPC → Jaeger
- **指标监控**：Prometheus `/metrics` 端点
- **结构化日志**：zap JSON（prod）或 Console（dev），lumberjack 文件轮转
- **健康检查**：`/healthz`（存活）、`/readyz`（就绪，检查 Redis/etcd/MySQL）

### 配置管理

采用 **环境变量 + .env + YAML** 三层配置策略：

1. **config.yaml** — 结构化配置，敏感值使用 `${VAR}` 或 `${VAR:default}` 占位
2. **.env** — 本地开发用，被 `.gitignore` 排除
3. **环境变量** — 生产/Docker 部署注入（docker-compose 的 `${VAR}`）
4. **loadDotenv()** — 自动从配置目录 → 当前目录 → 上级目录加载 `.env`，不覆盖已有环境变量
5. **单次正则展开** — 自定义 `expandEnv()` 替代 `os.ExpandEnv`，避免密码含 `$` 时被双重展开截断

### 环境变量展开语法

```yaml
# 必填（环境变量不存在则保留 ${VAR} 原文）
jwt:
  secret: "${JWT_SECRET}"

# 带默认值（环境变量不存在则使用冒号后的默认值）
metrics_password: "${METRICS_PASSWORD:}"
```

## 请求处理链路

```
HTTP 请求到达 Gateway (:8080)
    │
    ├─ limitRequestBody（10MB 限制）
    │
    ├─ /healthz, /readyz, /metrics → 直接响应
    │
    └─ 业务请求 → otelhttp（OpenTelemetry HTTP 追踪）
         │
         ├─ recoverMiddleware（panic 恢复）
         │
         ├─ corsMiddleware（CORS 白名单）
         │
         ├─ securityHeadersMiddleware（安全响应头）
         │
         ├─ jwtAuth（JWT + Redis 认证）
         │     ├─ 白名单路由 → 跳过认证，执行限流检查
         │     └─ 其他路由 → JWT 解析 + Redis token 校验 + userID 一致性校验
         │
         └─ grpc-gateway mux
              │
              ├─ discovery:///{service}（etcd 服务发现）
              │
              ├─ gRPC interceptor 链：
              │     circuitBreaker（熔断）
              │     otelgrpc（gRPC 追踪）
              │     keepalive
              │     retry（UNAVAILABLE 重试）
              │
              └─ 后端 gRPC 服务
                    │
                    ├─ Kratos 中间件链：
                    │     recovery → tracing → logging → validate
                    │
                    └─ service → biz → data → MySQL/Redis
```

## 依赖环境

| 工具 | 版本要求 | 用途 |
|------|----------|------|
| Go | >= 1.21 | 编译运行 |
| protoc | - | 编译 `.proto` 文件 |
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

# 2. 复制环境变量模板并填写
cp gateway/.env.example gateway/.env
# 编辑 gateway/.env、user/.env、district/.env

# 3. 启动 user 服务
cd user && go run ./cmd/user/ -conf configs/config.yaml

# 4. 启动 district 服务
cd district && go run ./cmd/district/ -conf configs/config.yaml

# 5. 启动 gateway（最后启动，依赖其他服务注册到 etcd）
cd gateway && go run ./cmd/gateway/ -conf configs/config.yaml
```

### 方式二：Docker Compose 一键部署

```bash
# 设置环境变量
export JWT_SECRET="your-jwt-secret-at-least-32-chars"
export REDIS_PASSWORD="your-redis-password"
export DB_PASSWORD="your-mysql-password"

# 启动全部服务
docker compose up -d --build
```

## 添加新服务

以添加 `order`（订单）服务为例，gRPC 端口 `:9004`。

### 1. 创建目录结构

```
order/
├── api/order/v1/order.proto
├── cmd/order/
│   ├── main.go
│   ├── wire.go
│   └── wire_gen.go
├── configs/config.yaml
└── internal/
    ├── biz/order.go
    ├── conf/
    ├── data/
    │   ├── data.go
    │   └── order.go
    ├── pkg/log/
    ├── server/grpc.go
    └── service/order.go
```

### 2. 编写代码

以 `user` 服务为模板，替换包名和服务名：

| 文件 | 关键修改 |
|------|----------|
| `internal/biz/order.go` | Model、Repo 接口、Usecase、wire ProviderSet |
| `internal/data/data.go` | MySQL 连接池配置 |
| `internal/data/order.go` | Repo 接口实现 |
| `internal/service/order.go` | 实现 proto Server 接口，mapError 映射 |
| `internal/server/grpc.go` | 中间件：recovery + tracing + logging + validate |
| `cmd/order/main.go` | 端口配置、etcd 注册、健康检查 |

### 3. 接入 Gateway

1. 复制 proto 到 `gateway/api/order/v1/`
2. 在 gateway 目录生成代码（pb.go + _grpc.pb.go + _gw.pb.go）
3. 在 `registry.go` 的 `services` 切片中添加一行

### 4. 命名替换规则

| 位置 | 替换 |
|------|------|
| 包名 | `user` → `order` |
| go_package | `user/api/user/v1` → `order/api/order/v1` |
| etcd 注册名 | `user` → `order` |
| Jaeger 服务名 | `storemicro-user` → `storemicro-order` |

## 常用命令速查

### 编译与运行

```bash
go build -o bin/user ./cmd/user/
go run ./cmd/user/ -conf configs/config.yaml
```

### Proto 代码生成

```bash
# 业务服务（user/district）
protoc --proto_path=. --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  api/user/v1/user.proto

# gateway（额外生成 grpc-gateway）
protoc --proto_path=. --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=. --grpc-gateway_opt=paths=source_relative \
  api/user/v1/user.proto

# 配置结构
protoc --proto_path=. --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  internal/conf/conf.proto
```

### Wire 依赖注入

```bash
cd cmd/user && wire
```

### etcd 调试

```bash
etcdctl --endpoints=localhost:2379 get / --prefix --keys-only
```

## 共享包说明

| 包 | 说明 |
|---|------|
| `pkg/tracing` | OTLP gRPC exporter + TracerProvider 初始化，被 user/district/gateway 共用 |
| `pkg/zaplogger` | 共享 zap logger 实现，Kratos log 桥接，dev/prod 模式切换 |
| `pkg/retry` | 通用重试包（指数退避 + 随机抖动），用于 MySQL/Redis/etcd 连接重试 |
| `pkg/dbutil` | 共享数据库连接池配置，被 district 服务使用 |
| `pkg/envexpand` | Kratos config.Source 实现，加载 YAML 时自动展开环境变量 |
| `pkg/strutil` | 工具函数，如手机号脱敏 `MaskPhone()` |
