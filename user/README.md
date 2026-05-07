# user — 用户服务

基于 Kratos 框架的 gRPC 用户服务，提供用户的创建与查询能力，通过 etcd 注册服务实例，供 gateway 发现调用。

## 目录

- [接口说明](#接口说明)
- [目录结构](#目录结构)
- [配置文件](#配置文件)
- [启动方式](#启动方式)
- [数据库表结构](#数据库表结构)
- [扩展：添加新接口（完整步骤）](#扩展添加新接口完整步骤)
- [扩展：添加 gRPC 中间件](#扩展添加-grpc-中间件)
- [常用命令](#常用命令)

---

## 接口说明

服务名：`user`，gRPC 监听 `:9001`

### CreateUser — 创建用户

**gRPC 方法**：`User/CreateUser`  
**HTTP 路由（gateway 转发后）**：`POST /api/v1/user`

请求体：

```json
{
  "name": "张三",
  "email": "zhangsan@example.com"
}
```

响应体：

```json
{
  "id": 1,
  "message": "created"
}
```

**业务规则**：
- `name` 不能为空
- `email` 不能为空
- `email` 必须在 `users` 表中唯一，重复注册返回 `ALREADY_EXISTS`

**错误码对照**：

| 场景 | gRPC 状态码 | HTTP 状态码 |
|------|-------------|-------------|
| 参数缺失 | `INVALID_ARGUMENT` | 400 |
| 邮箱已存在 | `ALREADY_EXISTS` | 409 |
| 服务内部错误 | `INTERNAL` | 500 |

---

### GetUser — 查询用户

**gRPC 方法**：`User/GetUser`  
**HTTP 路由（gateway 转发后）**：`GET /api/v1/user/{id}`

请求参数：路径中的 `{id}` 为用户 ID（字符串形式，服务内部转为 int64）

响应体：

```json
{
  "id": 1,
  "name": "张三",
  "email": "zhangsan@example.com"
}
```

**错误码对照**：

| 场景 | gRPC 状态码 | HTTP 状态码 |
|------|-------------|-------------|
| ID 格式错误 | `INVALID_ARGUMENT` | 400 |
| 用户不存在 | `NOT_FOUND` | 404 |

---

### Ping — 健康检查

**gRPC 方法**：`User/Ping`  
**HTTP 路由（gateway 转发后）**：`POST /api/v1/user/ping`

响应体：

```json
{"message": "pong"}
```

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
│   ├── main.go                # 程序入口：加载配置、连接 etcd、启动应用
│   ├── wire.go                # Wire 注入声明（编译时忽略）
│   └── wire_gen.go            # Wire 生成的依赖注入代码（勿手动修改）
│
├── internal/
│   ├── biz/
│   │   └── user.go            # 领域模型 User、UserRepo 接口、UserUsecase
│   ├── service/
│   │   └── user.go            # gRPC 服务实现，调用 UserUsecase
│   ├── data/
│   │   ├── data.go            # MySQL 连接池初始化
│   │   └── user.go            # UserRepo 的 MySQL 实现
│   ├── server/
│   │   └── grpc.go            # Kratos gRPC 服务器，注册 UserServer
│   ├── conf/
│   │   ├── conf.proto         # 配置结构定义
│   │   └── conf.pb.go         # 配置结构（生成）
│   └── pkg/log/
│       └── zap.go             # zap logger 封装
│
├── configs/
│   └── config.yaml            # 服务配置文件
├── third_party/google/api/    # proto HTTP 注解依赖
└── go.mod
```

### 各层职责

```
service 层（service/user.go）
    ↓  参数转换、错误码映射
biz 层（biz/user.go）
    ↓  业务规则、邮箱唯一性检查
data 层（data/user.go）
    ↓  MySQL CRUD
数据库（users 表）
```

---

## 配置文件

`configs/config.yaml`

```yaml
server:
  grpc:
    addr: 0.0.0.0:9001   # gRPC 监听地址
    timeout: 10s          # 请求超时时间

data:
  database:
    driver: mysql
    source: root:password@tcp(10.211.55.5:3306)/store_micro?charset=utf8mb4&parseTime=true&loc=Local

logger:
  level: info             # debug | info | warn | error

registry:
  etcd:
    endpoints:
      - 10.211.55.5:2379  # etcd 地址
    timeout: 10           # 连接超时（秒）
```

---

## 启动方式

### 开发模式

```bash
cd user
go run ./cmd/user/... -conf configs/config.yaml
```

### 编译后运行

```bash
cd user
go build -o bin/user ./cmd/user/...
./bin/user -conf configs/config.yaml
```

启动成功后可以看到类似日志：

```
INFO  service initializing  name=user grpc_addr=0.0.0.0:9001
INFO  etcd registry connected  endpoints=[10.211.55.5:2379]
INFO  service started  name=user
```

---

## 数据库表结构

```sql
CREATE TABLE users (
    id         BIGINT       AUTO_INCREMENT PRIMARY KEY,
    name       VARCHAR(64)  NOT NULL,
    email      VARCHAR(128) NOT NULL UNIQUE,
    created_at DATETIME     DEFAULT CURRENT_TIMESTAMP
);
```

---

## 扩展：添加新接口（完整步骤）

以添加 **UpdateUser（更新用户）** 接口为例，演示完整的扩展流程。

### 第一步：修改 proto 文件

编辑 `api/user/v1/user.proto`，添加新方法和消息定义：

```protobuf
service User {
  // ... 已有方法 ...

  rpc UpdateUser (UpdateUserReq) returns (UpdateUserResp) {
    option (google.api.http) = {
      put: "/api/v1/user/{id}"
      body: "*"
    };
  }
}

message UpdateUserReq {
  string id    = 1;  // 路径参数
  string name  = 2;  // 可选，为空则不更新
  string email = 3;  // 可选，为空则不更新
}

message UpdateUserResp {
  string message = 1;
}
```

### 第二步：重新生成 gRPC 代码

```bash
# 在 user 目录下执行
protoc \
  --proto_path=. \
  --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=. --grpc-gateway_opt=paths=source_relative \
  api/user/v1/user.proto
```

### 第三步：biz 层已有 Update 方法，确认接口签名

`internal/biz/user.go` 中已有：

```go
type UserRepo interface {
    // ...
    Update(ctx context.Context, user *User) error
}

func (uc *UserUsecase) Update(ctx context.Context, user *User) error { ... }
```

若 biz 层尚无对应方法，在此文件中添加业务逻辑。

### 第四步：data 层实现（已有，确认 SQL）

`internal/data/user.go` 中 `Update` 方法已实现：使用 `COALESCE(NULLIF(?, ''), name)` 实现可选更新。

### 第五步：在 service 层实现 gRPC 方法

编辑 `internal/service/user.go`：

```go
func (s *UserService) UpdateUser(ctx context.Context, req *userv1.UpdateUserReq) (*userv1.UpdateUserResp, error) {
    zap.L().Info("UpdateUser called", zap.String("id", req.Id))

    id, err := strconv.ParseInt(req.Id, 10, 64)
    if err != nil {
        return nil, status.Errorf(codes.InvalidArgument, "invalid user id: %s", req.Id)
    }

    err = s.uc.Update(ctx, &biz.User{
        ID:    id,
        Name:  req.Name,
        Email: req.Email,
    })
    if err != nil {
        return nil, s.mapError(err)
    }
    return &userv1.UpdateUserResp{Message: "updated"}, nil
}
```

同时在 `mapError` 中补充新的错误分支（如果有新错误类型）。

### 第六步：将新接口同步到 gateway

将更新后的 `api/user/v1/*.go` 文件同步到 `gateway/api/user/v1/`，然后重启 gateway。

```bash
cp user/api/user/v1/*.go gateway/api/user/v1/
```

### 第七步：验证

```bash
curl -X PUT http://localhost:8080/api/v1/user/1 \
  -H "Content-Type: application/json" \
  -d '{"name":"李四"}'
```

---

## 扩展：添加 gRPC 中间件

编辑 `internal/server/grpc.go`：

```go
import (
    "github.com/go-kratos/kratos/v2/middleware/ratelimit"
    "github.com/go-kratos/kratos/v2/middleware/tracing"
)

func NewGRPCServer(c *conf.Server, logger log.Logger, svc *service.UserService) *grpc.Server {
    opts := []grpc.ServerOption{
        grpc.Address(c.Grpc.Addr),
        grpc.Middleware(
            recovery.Recovery(),          // panic 恢复
            logging.Server(logger),       // 请求日志
            ratelimit.Server(),           // 限流（新增）
            tracing.Server(),             // 链路追踪（新增）
        ),
    }
    srv := grpc.NewServer(opts...)
    userv1.RegisterUserServer(srv, svc)
    return srv
}
```

---

## 常用命令

```bash
# 运行
go run ./cmd/user/... -conf configs/config.yaml

# 编译
go build -o bin/user ./cmd/user/...

# 重新生成 proto
protoc \
  --proto_path=. \
  --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=. --grpc-gateway_opt=paths=source_relative \
  api/user/v1/user.proto

# 重新生成 Wire 依赖注入代码
cd cmd/user && wire

# 整理依赖
go mod tidy

# 运行测试
go test ./...
go test -v -cover ./internal/biz/...
```
