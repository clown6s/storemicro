# User — 用户微服务

基于 **Kratos v2.9 + gRPC + etcd** 的用户服务，负责注册、登录、用户管理等核心认证业务。通过 etcd 注册服务实例，供 gateway 发现调用。内部服务不做认证，认证统一由 gateway 完成。

## 目录

- [架构设计](#架构设计)
- [核心设计](#核心设计)
- [目录结构](#目录结构)
- [配置管理](#配置管理)
- [启动方式](#启动方式)
- [健康检查与可观测性](#健康检查与可观测性)
- [常用命令](#常用命令)
- [扩展：添加新接口](#扩展添加新接口)

---

## 架构设计

遵循 Kratos **Clean Architecture** 分层，严格依赖倒置：

```
┌─────────────────────────────────────────┐
│  server/grpc.go                         │
│  传输层：gRPC Server + 中间件链         │
│  Recovery → Tracing → Logging → Validate│
├─────────────────────────────────────────┤
│  service/user.go                        │
│  适配层：proto ↔ biz 转换 + 错误码映射  │
├─────────────────────────────────────────┤
│  biz/user.go                            │
│  业务层：领域模型、规则校验、UseCase     │
│  定义 UserRepo 接口（依赖倒置）         │
├─────────────────────────────────────────┤
│  data/user.go                           │
│  数据层：UserRepo 的 MySQL 实现         │
│  参数化查询 + 事务 + LIKE 转义          │
├─────────────────────────────────────────┤
│  MySQL / Redis / etcd                   │
└─────────────────────────────────────────┘
```

各层职责：

| 层 | 文件 | 职责 |
|---|------|------|
| **server** | `server/grpc.go` | gRPC Server 创建，中间件链（Recovery、Tracing、Logging、ProtoValidate） |
| **service** | `service/user.go` | gRPC 方法实现，调用 biz UseCase，proto 消息与领域模型转换，biz error → gRPC status 映射 |
| **biz** | `biz/user.go` | 领域模型 `User`、`UserRepo` 接口定义、`UserUsecase` 业务逻辑（注册、登录、登出、查询、列表） |
| **data** | `data/user.go` | `UserRepo` 的 MySQL 实现，参数化查询防止 SQL 注入，事务支持唯一性校验 |
| **data** | `data/data.go` | MySQL 连接池 + Redis 连接，带重试的初始化 |

---

## 核心设计

### 1. 分布式雪花 ID（etcd 租约机制）

```
启动 → 遍历 node_id 0~1023 → etcd 事务原子抢占（Compare CreateRevision=0）
                                        ↓
                               创建 30s TTL 租约
                                        ↓
                               后台 goroutine 续约
                                        ↓
                               续约失败 → nodeReady=false → 拒绝生成 ID（安全优先）
```

- **`internal/pkg/snowflake/snowflake.go`** 实现
- 通过 etcd 事务保证多实例间 node_id 不冲突
- 租约 30s TTL + keep-alive，实例宕机后自动释放
- `GenID()` 在 `nodeReady=false` 时直接返回错误，宁可拒绝也不能生成重复 ID
- `Shutdown()` 取消 context，停止所有 keep-alive goroutine

### 2. 单设备登录（Redis 双映射 + Lua 原子操作）

```
登录 → Lua 脚本原子执行：
  1. 读取旧 token（storemicro:user_token:{userID}）
  2. 删除旧正向映射（storemicro:token:{oldToken}）
  3. 写入新正向映射（storemicro:token:{newToken} → userID）
  4. 写入反向映射（storemicro:user_token:{userID} → newToken）

结果：旧 token 的两条 Redis key 被清除，gateway 校验时返回 401
```

- **`internal/pkg/redis/redis.go`** 实现
- Lua 脚本保证原子性，避免并发登录的竞态条件
- Gateway 认证中间件查正向映射校验 token 有效性

### 3. 安全设计

| 措施 | 实现位置 |
|------|---------|
| bcrypt 密码哈希 | `biz/user.go` Register 方法 |
| 密码复杂度校验（8-72位、大小写+数字） | `biz/user.go` 22+ sentinel error |
| 手机号正则校验 `^1[3-9]\d{9}$` | `biz/user.go` |
| JWT 仅含 user_id（不含 PII） | `internal/pkg/jwt/jwt.go` |
| 防算法篡改（校验 signing algorithm） | `jwt.go` ParseToken 方法 |
| 登录失败时间恒定（dummy bcrypt hash） | `biz/user.go` Login 方法 |
| 手机号脱敏 `138****5678` | `service/user.go` toUserInfo，`pkg/strutil` |
| 参数化查询 + LIKE 通配符转义 | `data/user.go` escapeLike 函数 |
| 事务 + SELECT FOR UPDATE 唯一性校验 | `data/user.go` RegisterTx / UpdateTx |
| 软删除（deleted_at），防 ID 枚举 | `data/user.go` Delete（0行影响不报错） |

### 4. 输入校验规则

| 字段 | 规则 |
|------|------|
| phone | 必填，`^1[3-9]\d{9}$` |
| password | 必填，8-72 字符，必须包含大写+小写+数字 |
| email | 选填，`^[^\s@]+@[^\s@]+\.[^\s@]+$` |
| realName | 选填，最长 64 字符 |
| gender | 0（未知）/ 1（男）/ 2（女） |
| age | 0-150 |

### 5. 依赖注入（Wire）

```bash
cmd/user/wire.go  →  声明 ProviderSet
cmd/user/wire_gen.go  →  自动生成（勿手动修改）
```

Wire 依赖链：

```
NewData (MySQL + Redis)
    ↓
NewUserRepo (实现 biz.UserRepo 接口)
    ↓
NewUserUsecase (biz.UserRepo + JWT + Redis)
    ↓
NewUserService (biz.UserUsecase)
    ↓
NewGRPCServer (UserService)
    ↓
NewApp (GRPCServer)
```

---

## 目录结构

```
user/
├── api/user/v1/
│   ├── user.proto              # gRPC 接口定义（源文件）
│   ├── user.pb.go              # protobuf 消息结构（protoc 生成）
│   ├── user_grpc.pb.go         # gRPC 服务端/客户端接口（protoc 生成）
│   └── user.pb.gw.go           # grpc-gateway HTTP 路由（仅 gateway 生成）
│
├── cmd/user/
│   ├── main.go                 # 入口：配置 → logger → tracer → etcd → snowflake → wireApp → 启动
│   ├── wire.go                 # Wire 依赖注入声明
│   └── wire_gen.go             # Wire 生成的注入代码（勿手动修改）
│
├── internal/
│   ├── biz/
│   │   └── user.go             # 领域模型 User + UserRepo 接口 + UserUsecase（22+ sentinel error）
│   ├── service/
│   │   └── user.go             # gRPC 方法实现，proto↔biz 转换 + 错误码映射
│   ├── data/
│   │   ├── data.go             # MySQL/Redis 连接池初始化，带重试
│   │   └── user.go             # UserRepo MySQL 实现（参数化查询、事务、软删除）
│   ├── server/
│   │   └── grpc.go             # gRPC Server + 中间件链
│   ├── conf/
│   │   ├── conf.proto          # 配置结构定义（proto source of truth）
│   │   └── conf.pb.go          # 配置结构（protoc 生成）
│   └── pkg/
│       ├── jwt/
│       │   └── jwt.go          # JWT HS256 工具（生成/解析，防 none 算法攻击）
│       ├── redis/
│       │   └── redis.go        # Redis 封装（Lua 原子脚本，单设备登录双映射）
│       ├── snowflake/
│       │   └── snowflake.go    # 分布式雪花 ID（etcd 租约 + 原子抢占 node_id）
│       └── log/
│           └── zap.go          # Kratos log.Logger → zap 桥接（委托 pkg/zaplogger）
│
├── configs/
│   └── config.yaml             # 服务配置（密钥通过 ${VAR} 环境变量注入）
├── Dockerfile                  # 多阶段构建
├── third_party/google/         # proto HTTP 注解依赖
├── go.mod
└── go.sum
```

---

## 配置管理

### 配置加载流程

```
envexpand    →  Kratos config.Source，YAML 中 ${VAR:default} 单次正则展开
       ↓
config.yaml  →  所有配置直接写入，docker-compose environment 覆盖敏感值
```

### 配置项

配置通过 `configs/config.yaml` 管理，容器部署时由 docker-compose environment 覆盖敏感值：

| 配置块 | 说明 |
|--------|------|
| `server.grpc` | gRPC 监听地址（`:9001`）和超时 |
| `data.database` | MySQL 连接（driver、DSN、连接池参数） |
| `registry.etcd` | etcd 服务注册（endpoints、timeout） |
| `jwt` | JWT 密钥（≥32字符）和过期时间 |
| `redis` | Redis 连接（addr、password、db） |
| `logger` | 日志级别（dev/prod 模式） |
| `tracing` | OTLP 追踪端点和采样率 |

---

## 启动方式

### 本地开发

```bash
cd user
go run ./cmd/user/ -conf configs/config.yaml
```

启动流程：

1. 通过 `envexpand` 加载配置（自动展开 `${VAR:default}`）
3. 初始化 zap logger
4. 初始化 OTLP tracer
5. 连接 etcd（带重试）→ 注册服务 `storemicro-user`
6. 初始化雪花 ID（etcd 租约分配 node_id）
7. Wire 注入 → 启动 gRPC Server
8. 启动健康检查 HTTP Server（`:9101`）

### 编译运行

```bash
cd user
go build -o bin/user ./cmd/user/
./bin/user -conf configs/config.yaml
```

### Docker

```bash
docker build -t storemicro-user .
docker run -p 9001:9001 -p 9101:9101 \
  -e DB_PASSWORD=xxx -e REDIS_PASSWORD=xxx -e JWT_SECRET=xxx \
  storemicro-user
```

---

## 健康检查与可观测性

健康检查服务监听 `:9101`（HTTP）：

| 端点 | 说明 |
|------|------|
| `GET /healthz` | 存活探针，始终返回 200 |
| `GET /readyz` | 就绪探针，检查 MySQL + Redis 连通性 |
| `GET /metrics` | Prometheus 指标（可选 Bearer token 认证） |

追踪：OTLP gRPC → Jaeger，服务名 `storemicro-user`。

---

## 常用命令

```bash
# === 运行 ===
go run ./cmd/user/ -conf configs/config.yaml

# === 编译 ===
go build -o bin/user ./cmd/user/

# === Proto 代码生成（user 服务，不需要 grpc-gateway） ===
cd user
protoc \
  --proto_path=. \
  --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  api/user/v1/user.proto

# === Wire 依赖注入生成 ===
cd cmd/user && wire

# === Proto 配置结构生成 ===
protoc \
  --proto_path=. \
  --go_out=. --go_opt=paths=source_relative \
  internal/conf/conf.proto

# === 其他 ===
go mod tidy          # 整理依赖
go test ./...        # 运行测试
go vet ./...         # 静态检查
```

---

## 扩展：添加新接口

以添加 `UpdateUser` 为例：

### 1. 修改 proto

编辑 `api/user/v1/user.proto`，添加 RPC 方法和消息定义。

### 2. 生成代码

```bash
cd user
protoc \
  --proto_path=. \
  --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  api/user/v1/user.proto
```

### 3. 实现业务层

`internal/biz/user.go`：在 `UserRepo` 接口添加方法声明，`UserUsecase` 添加业务逻辑。

### 4. 实现数据层

`internal/data/user.go`：实现 repo 接口方法（参数化查询 + 事务）。

### 5. 实现 service 层

`internal/service/user.go`：实现 gRPC 方法，调用 UseCase + 错误映射。

### 6. 同步到 gateway

```bash
# 复制 proto 和生成的 Go 文件
cp api/user/v1/user.proto ../../gateway/api/user/v1/
cp api/user/v1/user.pb.go ../../gateway/api/user/v1/
cp api/user/v1/user_grpc.pb.go ../../gateway/api/user/v1/

# 在 gateway 生成 .pb.gw.go
cd ../../gateway
protoc \
  --proto_path=. \
  --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=. --grpc-gateway_opt=paths=source_relative \
  api/user/v1/user.proto
```

### 7. 重新生成 Wire（修改了接口才需要）

```bash
cd user/cmd/user && wire
```
