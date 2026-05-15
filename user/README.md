# user — 用户服务

基于 Kratos v2 框架的 gRPC 用户服务，提供用户注册、登录、查询、登出等能力，通过 etcd 注册服务实例，供 gateway 发现调用。

## 目录

- [接口说明](#接口说明)
- [目录结构](#目录结构)
- [配置文件](#配置文件)
- [启动方式](#启动方式)
- [数据库表结构](#数据库表结构)
- [扩展：添加新接口（完整步骤）](#扩展添加新接口完整步骤)
- [常用命令](#常用命令)

---

## 生成步骤（重要：先看这里）

### ⚠️ 各服务需要的生成文件不同

| 服务 | 需要生成的文件 | 是否需要 --grpc-gateway_out |
|------|---------------|------------------------------|
| **user / district**（后端 gRPC 服务） | `*.pb.go` + `*_grpc.pb.go` | ❌ 不需要 |
| **gateway**（HTTP 网关） | `*.pb.go` + `*_grpc.pb.go` + `*.pb.gw.go` | ✅ 必须加 |

---

### 第一步：生成 user 服务自身代码（在 user/ 目录下执行）

```bash
cd user

protoc \
  --proto_path=. \
  --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  api/user/v1/user.proto
```

生成后，`api/user/v1/` 下应有：
- ✅ `user.pb.go`
- ✅ `user_grpc.pb.go`
- ❌ **不需要** `user.pb.gw.go`

---

### 第二步：复制 proto 到 gateway（如果需要对外暴露 HTTP 接口）

```bash
mkdir -p gateway/api/user/v1
cp api/user/v1/user.proto gateway/api/user/v1/user.proto
```

---

### 第三步：在 gateway 目录下生成完整代码（含 .pb.gw.go）

```bash
cd gateway

protoc \
  --proto_path=. \
  --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=. --grpc-gateway_opt=paths=source_relative \
  api/user/v1/user.proto
```

生成后，`gateway/api/user/v1/` 下应有：
- ✅ `user.pb.go`
- ✅ `user_grpc.pb.go`
- ✅ `user.pb.gw.go` ← 只在 gateway 里生成

---

### 第四步：重新生成 Wire（修改了接口后才需要）

```bash
cd user/cmd/user
wire
```

---

## 接口说明

服务名：`storemicro-user`，gRPC 监听 `:9001`

### Register — 用户注册

**gRPC 方法**：`User/Register`  
**HTTP 路由（gateway 转发后）**：`POST /api/v1/user/register`

请求体：

```json
{
  "phone": "13800138000",
  "password": "123456",
  "email": "test@example.com",
  "realName": "张三",
  "gender": 1,
  "age": 25
}
```

响应体：

```json
{
  "id": 1,
  "message": "registered"
}
```

**业务规则**：
- `phone` 不能为空，且全局唯一
- `password` 不能为空，服务端用 bcrypt 加密存储
- `email` 如果提供，必须全局唯一

---

### Login — 用户登录

**gRPC 方法**：`User/Login`  
**HTTP 路由**：`POST /api/v1/user/login`

请求体：

```json
{
  "phone": "13800138000",
  "password": "123456"
}
```

响应体：

```json
{
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "user": {
    "id": 1,
    "phone": "13800138000",
    "realName": "张三",
    "email": "test@example.com"
  }
}
```

**说明**：
- 登录成功返回 JWT token，有效期 24 小时
- 支持单设备登录：重复登录会使旧 token 失效
- token 同时写入 Redis（`storemicro:token:{token}` → userID），gateway 层会校验

---

### GetUser — 查询用户

**gRPC 方法**：`User/GetUser`  
**HTTP 路由**：`GET /api/v1/user/{id}`

响应体：

```json
{
  "user": {
    "id": 1,
    "phone": "13800138000",
    "realName": "张三",
    "avatar": "",
    "gender": 1,
    "age": 25,
    "email": "test@example.com",
    "createTime": "2026-01-01 12:00:00",
    "updateTime": "2026-01-01 12:00:00"
  }
}
```

---

### ListUsers — 用户列表（分页 + 动态搜索）

**gRPC 方法**：`User/ListUsers`  
**HTTP 路由**：`GET /api/v1/user/list`

查询参数：

| 参数 | 类型 | 说明 |
|------|------|------|
| `page` | int32 | 页码，默认 1 |
| `pageSize` | int32 | 每页数量，默认 10，最大 100 |
| `phone` | string | 可选，按手机号前缀模糊搜索 |
| `name` | string | 可选，按真实姓名前缀模糊搜索 |

请求示例：

```bash
curl "http://localhost:8080/api/v1/user/list?page=1&pageSize=10&phone=138"
```

响应体：

```json
{
  "users": [
    {
      "id": 1,
      "phone": "13800138000",
      "realName": "张三",
      "email": "test@example.com"
    }
  ],
  "total": 1,
  "page": 1,
  "pageSize": 10
}
```

---

### Logout — 用户登出

**gRPC 方法**：`User/Logout`  
**HTTP 路由**：`POST /api/v1/user/logout`

请求体：

```json
{
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."
}
```

响应体：

```json
{
  "message": "logged out"
}
```

**说明**：
- 从 Redis 删除 token 双映射（`storemicro:token:{token}` 和 `storemicro:user_token:{userID}`）
- token 已不存在时视为已登出，不报错
- 登出后该 token 在 gateway 层会被拒绝（401）

---

### Ping — 健康检查

**gRPC 方法**：`User/Ping`  
**HTTP 路由**：`POST /api/v1/user/ping`

响应体：`{"message": "pong"}`

---

## 目录结构

```
user/
├── api/user/v1/
│   ├── user.proto             # 接口定义（源文件）
│   ├── user.pb.go             # protobuf 消息结构（生成）
│   ├── user_grpc.pb.go        # gRPC 服务端/客户端接口（生成）
│   └── user.pb.gw.go          # grpc-gateway HTTP 路由（生成）
│
├── cmd/user/
│   ├── main.go                # 程序入口：加载配置、连接 etcd、初始化 tracing、启动应用
│   ├── wire.go                # Wire 注入声明
│   └── wire_gen.go            # Wire 生成的依赖注入代码（勿手动修改）
│
├── internal/
│   ├── biz/
│   │   └── user.go            # 领域模型 User、UserRepo 接口、UserUsecase（含 Register/Login/Logout/ListUsers）
│   ├── service/
│   │   └── user.go            # gRPC 服务实现，调用 UserUsecase，错误码映射
│   ├── data/
│   │   ├── data.go            # MySQL 连接池初始化、Redis 初始化
│   │   ├── user.go            # UserRepo 的 MySQL 实现（含动态查询）
│   │   └── jwt.go             # JWT 生成/解析
│   ├── server/
│   │   └── grpc.go            # Kratos gRPC 服务器（tracing + logging 中间件）
│   ├── conf/
│   │   ├── conf.proto         # 配置结构定义
│   │   └── conf.pb.go         # 配置结构（生成）
│   └── pkg/
│       ├── redis/
│       │   └── redis.go        # Redis 封装（token 双映射，支持单设备登录）
│       ├── jwt/
│       │   └── jwt.go          # JWT 工具封装
│       ├── snowflake/
│       │   └── snowflake.go    # 雪花算法 ID 生成（基于 etcd 自动分配 node_id）
│       └── log/
│           └── zap.go          # zap logger 封装
│
├── configs/
│   └── config.yaml            # 服务配置文件
├── third_party/google/         # proto HTTP 注解依赖（含 protobuf/descriptor.proto）
└── go.mod
```

### 各层职责

```
service 层（service/user.go）
    ↓  参数转换、错误码映射
biz 层（biz/user.go）
    ↓  业务规则、密码加密、token 管理
data 层（data/user.go + data/jwt.go）
    ↓  MySQL CRUD + Redis token 存储
数据库（user 表）+ Redis
```

---

## 配置文件

`configs/config.yaml`

```yaml
server:
  grpc:
    addr: 0.0.0.0:9001   # gRPC 监听地址
    timeout: 10s

data:
  database:
    driver: mysql
    source: root:admin@zzp@tcp(10.211.55.5:3306)/store_micro?charset=utf8mb4&parseTime=true&loc=Local

logger:
  level: info             # debug | info | warn | error

registry:
  etcd:
    endpoints:
      - 10.211.55.5:2379
    timeout: 10

jwt:
  secret: "store-micro-jwt-secret-2026"
  expire_hours: 24

redis:
  addr: 10.211.55.5:6379
  password: "admin@zzp"
  db: 0
```

---

## 启动方式

### 开发模式

```bash
cd user
go run ./cmd/user/ -conf configs/config.yaml
```

### 编译后运行

```bash
cd user
go build -o bin/user ./cmd/user/
./bin/user -conf configs/config.yaml
```

启动成功日志：

```
INFO  service initializing  name=storemicro-user  grpc_addr=0.0.0.0:9001
INFO  etcd registry connected  endpoints=[10.211.55.5:2379]
INFO  OTLP tracer initialized  service=storemicro-user endpoint=10.211.55.5:4317
INFO  service started  name=storemicro-user
```

---

## 数据库表结构

```sql
CREATE TABLE `user` (
    id          BIGINT       NOT NULL PRIMARY KEY,
    password    VARCHAR(255) NOT NULL,
    real_name   VARCHAR(64)  DEFAULT '',
    avatar      VARCHAR(255) DEFAULT '',
    gender      INT          DEFAULT 0,
    age         INT          DEFAULT 0,
    phone       VARCHAR(20)  NOT NULL,
    email       VARCHAR(128) DEFAULT '',
    create_time DATETIME     DEFAULT CURRENT_TIMESTAMP,
    update_time DATETIME     DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    remark      VARCHAR(255) DEFAULT '',
    deleted_at  DATETIME     DEFAULT NULL
);
```

> 注意：id 由雪花算法生成，不是 AUTO_INCREMENT。

---

## 扩展：添加新接口（完整步骤）

以添加 **UpdateUser** 接口为例：

### 第一步：修改 proto 文件

编辑 `api/user/v1/user.proto`，添加新方法和消息定义，并配置 HTTP 路由注解。

### 第二步：重新生成代码

在 `user/` 目录下执行：

```bash
protoc \
  --proto_path=. \
  --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  api/user/v1/user.proto
```

### 第三步：实现 biz 层

在 `internal/biz/user.go` 中添加业务逻辑。

### 第四步：实现 service 层

在 `internal/service/user.go` 中实现 gRPC 方法，调用 biz 层并做错误码映射。

### 第五步：同步到 gateway

```bash
cp api/user/v1/*.go ../../gateway/api/user/v1/
cd ../../gateway
protoc \
  --proto_path=. \
  --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=. --grpc-gateway_opt=paths=source_relative \
  api/user/v1/user.proto
```

### 第六步：验证

```bash
curl -X POST http://localhost:8080/api/v1/user/xxx \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer {token}" \
  -d '{...}'
```

---

## 常用命令

```bash
# 运行
go run ./cmd/user/ -conf configs/config.yaml

# 编译
go build -o bin/user ./cmd/user/

# 重新生成 proto（user 服务，不需要 grpc-gateway）
protoc \
  --proto_path=. \
  --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  api/user/v1/user.proto

# 重新生成 Wire 依赖注入代码
cd cmd/user && wire

# 整理依赖
go mod tidy

# 运行测试
go test ./...
```
