# User — 用户微服务

基于 **Kratos v2.9 + gRPC + etcd** 的用户服务，监听 gRPC `:6001`，健康检查 `:9101`。负责注册、登录、登出、用户管理等核心认证业务。通过 etcd 注册服务实例，供 gateway 发现调用。内部服务不做认证，认证统一由 gateway 完成。

## 目录

- [架构设计](#架构设计)
- [API 接口与请求流程](#api-接口与请求流程)
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
┌──────────────────────────────────────────────┐
│  Gateway (:7000)                              │
│  JWT 认证 → 限流 → grpc-gateway → gRPC 调用   │
└──────────────┬───────────────────────────────┘
               │ gRPC :6001
┌──────────────▼───────────────────────────────┐
│  server/grpc.go                               │
│  gRPC Server + 中间件链                       │
│  customRecovery → rateLimit → tracing → log  │
├──────────────────────────────────────────────┤
│  service/user.go                              │
│  proto message ↔ biz 领域模型转换 + 错误码映射│
├──────────────────────────────────────────────┤
│  biz/user.go                                  │
│  领域模型 User + UserRepo 接口 + UserUsecase  │
│  注册校验 / 密码强度 / 登录验证 / 分页        │
├──────────────────────────────────────────────┤
│  data/user.go                                 │
│  UserRepo MySQL 实现                          │
│  参数化查询 + 事务 + LIKE 转义                │
├──────────────────────────────────────────────┤
│  MySQL / Redis / etcd                         │
└──────────────────────────────────────────────┘
```

各层职责：

| 层 | 文件 | 职责 |
|---|------|------|
| **server** | `server/grpc.go` | gRPC Server + 中间件链（customRecovery、rateLimit、tracing、logging） |
| **service** | `service/user.go` | gRPC 方法实现，proto 消息与领域模型转换，biz error → gRPC status 映射 |
| **biz** | `biz/user.go` | 领域模型 `User`、`UserRepo` 接口定义、`UserUsecase` 业务逻辑 |
| **data** | `data/user.go` | `UserRepo` MySQL 实现，参数化查询，事务支持 |
| **data** | `data/data.go` | MySQL + Redis 连接池，带重试初始化 |

---

## API 接口与请求流程

### RPC 方法定义

```protobuf
service User {
  rpc GetUser           (GetUserReq)          returns (GetUserResp)
  rpc Register          (RegisterReq)          returns (RegisterResp)
  rpc Login             (LoginReq)             returns (LoginResp)
  rpc RefreshToken      (RefreshTokenReq)      returns (RefreshTokenResp)
  rpc ListUsers         (ListUsersReq)         returns (ListUsersResp)
  rpc Logout            (LogoutReq)            returns (LogoutResp)
  rpc ChangePassword    (ChangePasswordReq)    returns (ChangePasswordResp)
  rpc UpdateUser        (UpdateUserReq)        returns (UpdateUserResp)
  rpc DeleteUser        (DeleteUserReq)        returns (DeleteUserResp)
}
```

### 各接口请求流程详解

#### 1. Register（用户注册）

```
Gateway 白名单放行 + 限流 3次/分/IP
    ↓
gRPC → User 服务 :6001
    ↓
gRPC interceptor: customRecovery → rateLimit(10次/秒/IP) → tracing → logging
    ↓
service.Register()
    ↓
biz.UserUsecase.Register()
    ├─ ① 手机号校验：^1[3-9]\d{9}$
    ├─ ② 密码强度校验：8-72 位，必须含大写+小写+数字
    ├─ ③ 邮箱格式校验（选填）：^[^\s@]+@[^\s@]+\.[^\s@]+$
    ├─ ④ 性别校验：0(未知) / 1(男) / 2(女)
    ├─ ⑤ 年龄校验：0-150
    ├─ ⑥ bcrypt.GenerateFromPassword(cost=10) 加密密码
    └─ ⑦ data.CreateOrderTx()（MySQL 事务）：
        ├─ SELECT id FROM users WHERE phone=? FOR UPDATE（唯一性检查）
        ├─ 已存在 → 返回 ErrPhoneAlreadyExists
        └─ INSERT INTO users(...) VALUES(...)
            ├─ 雪花算法生成 ID（etcd 租约保证分布式唯一）
            └─ 成功 → 返回用户信息（手机号脱敏 138****5678）
```

#### 2. Login（用户登录）

```
Gateway 白名单放行 + 限流 5次/分/IP
    ↓
gRPC → User 服务 :6001
    ↓
biz.UserUsecase.Login()
    ├─ ① 手机号校验
    ├─ ② data.GetByPhone(phone)
    │   └─ SELECT * FROM users WHERE phone=? AND deleted_at IS NULL
    ├─ ③ 用户不存在？
    │   └─ 执行 dummy bcrypt.CompareHash（时间恒定，防用户名枚举）
    │       → 返回 ErrInvalidCredentials
    ├─ ④ bcrypt.CompareHashAndPassword（密码比对）
    │   └─ 不匹配 → 返回 ErrInvalidCredentials
    ├─ ⑤ JWT.GenerateToken(userID)
    │   ├─ access_token: HS256, exp=24h, claims={user_id}
    │   └─ refresh_token: HS256, exp=7d, claims={user_id, type=refresh}
    └─ ⑥ Redis Lua 原子操作（单设备登录）：
        ├─ GET user_token:{userID} → oldToken
        ├─ DEL token:{oldToken}（踢掉旧设备）
        ├─ SET token:{newAccessToken} → userID
        └─ SET user_token:{userID} → newAccessToken
    返回 {token, refreshToken, data: {userInfo}}
```

#### 3. RefreshToken（刷新 Token）

```
Gateway 白名单放行（无限流）
    ↓
biz.UserUsecase.RefreshToken()
    ├─ ① JWT.ParseToken(refreshToken)
    │   ├─ 验证签名、过期时间
    │   └─ 检查 claims.type == "refresh"
    ├─ ② Redis GET token:{refreshToken} → userID
    │   └─ 不存在 → 返回 ErrInvalidToken
    ├─ ③ JWT.GenerateToken(userID) 生成新的 access_token
    └─ ④ Redis SET token:{newAccessToken} → userID
    返回 {token, refreshToken, data: {userInfo}}
```

#### 4. GetUser（获取用户信息）

```
Gateway JWT 认证 → 提取 user_id → 注入 metadata
    ↓
biz.UserUsecase.GetUser()
    ├─ ① data.GetByID(id)
    │   └─ SELECT * FROM users WHERE id=? AND deleted_at IS NULL
    └─ ② 返回用户信息（手机号脱敏）
```

#### 5. ListUsers（用户列表）

```
Gateway JWT 认证
    ↓
biz.UserUsecase.ListUsers()
    ├─ ① 分页参数标准化
    │   ├─ page < 1 → page = 1
    │   ├─ pageSize < 1 → pageSize = 1
    │   └─ pageSize > 100 → pageSize = 100
    ├─ ② data.List(page, pageSize)
    │   ├─ SELECT * FROM users WHERE deleted_at IS NULL
    │   │   ORDER BY created_at DESC
    │   │   LIMIT ? OFFSET ?
    │   └─ SELECT COUNT(*) FROM users WHERE deleted_at IS NULL
    └─ ③ 返回 {list, total}（手机号脱敏）
```

#### 6. Logout（登出）

```
Gateway JWT 认证 → user_id from metadata
    ↓
biz.UserUsecase.Logout()
    ├─ ① 从 metadata 获取 token 和 userID
    └─ ② Redis Lua 原子删除（防并发竞态）：
        ├─ GET token:{token} → 验证 userID 一致
        ├─ DEL token:{token}
        └─ DEL user_token:{userID}
```

#### 7. ChangePassword（修改密码）

```
Gateway JWT 认证 → user_id from metadata
    ↓
biz.UserUsecase.ChangePassword()
    ├─ ① 新密码强度校验（8-72位，大小写+数字）
    ├─ ② data.GetByID(userID) → 获取用户
    ├─ ③ bcrypt.CompareHashAndPassword 旧密码验证
    │   └─ 不匹配 → ErrInvalidPassword
    ├─ ④ bcrypt.GenerateFromPassword 新密码加密
    ├─ ⑤ data.UpdatePasswordTx()（事务）：
    │   └─ UPDATE users SET password_hash=? WHERE id=?
    └─ ⑥ 删除该用户所有 Redis token（强制所有设备重新登录）
        └─ Redis Lua: SCAN + DEL token:* + DEL user_token:{userID}
```

#### 8. UpdateUser（更新用户信息）

```
Gateway JWT 认证 → user_id from metadata
    ↓
biz.UserUsecase.Update()
    ├─ ① 邮箱格式校验（如果提供）
    ├─ ② gender/age 范围校验
    ├─ ③ 检查是否至少有一个字段需要更新
    │   └─ 全部为空 → ErrNothingToUpdate
    ├─ ④ data.UpdateTx()（事务）：
    │   ├─ SELECT FOR UPDATE（加行锁）
    │   ├─ 如果更新邮箱 → 检查唯一性（非自身用户）
    │   └─ UPDATE users SET ... WHERE id=?
    └─ ⑤ 返回更新后的用户信息（手机号脱敏）
```

#### 9. DeleteUser（删除用户 — 软删除）

```
Gateway JWT 认证 → user_id from metadata
    ↓
biz.UserUsecase.Delete()
    ├─ ① data.Delete(id)
    │   └─ UPDATE users SET deleted_at=NOW() WHERE id=? AND deleted_at IS NULL
    ├─ ② RowsAffected == 0 → 返回 nil（不报错，防 ID 枚举探测）
    └─ ③ 删除 Redis token（强制下线）
```

### gRPC 服务端中间件链

```
gRPC 请求到达 :6001
    ↓
① customRecovery（自定义 panic 恢复，记录完整堆栈到 zap）
    ↓
② rateLimitMiddleware（基于 IP 令牌桶限流）
    ├─ 仅 Login/Register 生效
    ├─ 速率：10次/秒，突发容量 30
    └─ 其他 RPC 直接放行
    ↓
③ tracing.Server()（Kratos OTel，创建 gRPC span）
    ↓
④ logging.Server(logger)（Kratos 结构化日志，记录方法/耗时/错误码）
    ↓
⑤ service 层方法处理
```

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

- `internal/pkg/snowflake/snowflake.go` 实现
- 通过 etcd 事务保证多实例间 node_id 不冲突
- 租约 30s TTL + keep-alive，实例宕机后自动释放
- `GenID()` 在 `nodeReady=false` 时直接返回错误

### 2. 单设备登录（Redis 双映射 + Lua 原子操作）

```
登录 → Lua 脚本原子执行：
  1. 读取旧 token（storemicro:user_token:{userID}）
  2. 删除旧正向映射（storemicro:token:{oldToken}）
  3. 写入新正向映射（storemicro:token:{newToken} → userID）
  4. 写入反向映射（storemicro:user_token:{userID} → newToken）

结果：旧 token 的两条 Redis key 被清除，gateway 校验时返回 401
```

### 3. 安全设计

| 措施 | 实现位置 |
|------|---------|
| bcrypt 密码哈希（cost=10） | `biz/user.go` Register |
| 密码复杂度校验 | `biz/user.go` |
| 手机号正则 `^1[3-9]\d{9}$` | `biz/user.go` |
| JWT 仅含 user_id（不含 PII） | `internal/pkg/jwt/jwt.go` |
| 登录失败时间恒定（dummy hash） | `biz/user.go` Login |
| 手机号脱敏 `138****5678` | `service/user.go` |
| 参数化查询 + LIKE 转义 | `data/user.go` |
| 事务 + SELECT FOR UPDATE | `data/user.go` |
| 软删除 deleted_at | `data/user.go` |
| 0 rows affected 不报错 | `data/user.go` Delete |

### 4. 输入校验规则

| 字段 | 规则 |
|------|------|
| phone | 必填，`^1[3-9]\d{9}$` |
| password | 必填，8-72 字符，大写+小写+数字 |
| email | 选填，`^[^\s@]+@[^\s@]+\.[^\s@]+$` |
| realName | 选填，最长 64 字符 |
| gender | 0(未知) / 1(男) / 2(女) |
| age | 0-150 |

### 5. 依赖注入（Wire）

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
│   ├── user.pb.go              # protobuf 消息（protoc 生成）
│   ├── user_grpc.pb.go         # gRPC 服务端/客户端接口（protoc 生成）
│
├── cmd/user/
│   ├── main.go                 # 入口：配置→logger→tracer→etcd→snowflake→wire→启动
│   ├── wire.go                 # Wire 依赖注入声明
│   └── wire_gen.go             # Wire 生成的注入代码（勿手动修改）
│
├── internal/
│   ├── biz/
│   │   └── user.go             # 领域模型 User + UserRepo 接口 + UserUsecase
│   ├── service/
│   │   └── user.go             # gRPC 方法实现 + proto↔biz 转换 + 错误码映射
│   ├── data/
│   │   ├── data.go             # MySQL/Redis 连接池初始化
│   │   └── user.go             # UserRepo MySQL 实现
│   ├── server/
│   │   └── grpc.go             # gRPC Server + 中间件链
│   ├── conf/
│   │   ├── conf.proto          # 配置结构定义
│   │   └── conf.pb.go          # 配置结构（protoc 生成）
│   └── pkg/
│       ├── jwt/jwt.go          # JWT HS256（生成/解析，防 none 算法攻击）
│       ├── redis/redis.go      # Redis Lua CAS 原子脚本
│       ├── snowflake/snowflake.go  # 分布式雪花 ID（etcd 租约）
│       └── log/zap.go          # Kratos log.Logger → zap 桥接
│
├── configs/config.yaml         # 服务配置
├── Dockerfile                  # 多阶段构建
├── go.mod
└── go.sum
```

---

## 配置管理

| 配置块 | 说明 |
|--------|------|
| `server.grpc` | gRPC 监听 `:6001`，timeout 10s |
| `data.database` | MySQL 连接（store_micro，maxOpen 100，maxIdle 25，lifetime 3600s） |
| `registry.etcd` | etcd 注册 `storemicro-user` |
| `jwt` | 密钥 `>=32字符`，expire 24h |
| `redis` | 连接 + pool 20 |
| `logger` | dev/prod 模式 |
| `tracing` | OTLP endpoint + sampleRate 0.1 |

---

## 启动方式

```bash
cd user
go run ./cmd/user/ -conf configs/config.yaml
```

启动流程：

1. envexpand 加载配置（展开 `${VAR:default}`）
2. 初始化 zap logger
3. 初始化 OTLP tracer
4. 连接 etcd → 注册服务 `storemicro-user`
5. 初始化雪花 ID（etcd 租约分配 node_id）
6. Wire 注入 → 启动 gRPC Server (:6001)
7. 启动 health sidecar (:9101)

---

## 健康检查与可观测性

| 端点 | 端口 | 说明 |
|------|------|------|
| `GET /healthz` | `:9101` | 存活探针 |
| `GET /readyz` | `:9101` | 就绪探针（MySQL + Redis 连通性） |
| `GET /metrics` | `:9101` | Prometheus 指标 |

---

## 常用命令

```bash
# === 运行 ===
go run ./cmd/user/ -conf configs/config.yaml

# === Proto 生成（user 不需要 grpc-gateway） ===
protoc --proto_path=. --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  api/user/v1/user.proto

# === Wire ===
cd cmd/user && wire
```

---

## 扩展：添加新接口

以添加 `UpdateUser` 为例：

1. 修改 `api/user/v1/user.proto`，添加 RPC 方法和消息
2. `protoc` 生成代码
3. `biz/user.go`：UserRepo 接口添加方法 + UserUsecase 实现业务逻辑
4. `data/user.go`：实现 repo 方法（参数化查询 + 事务）
5. `service/user.go`：实现 gRPC 方法 + 错误映射
6. `cd cmd/user && wire`（修改了接口才需要）
7. 同步 proto 到 gateway + 生成 `.pb.gw.go`
