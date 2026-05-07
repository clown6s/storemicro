# storemicro — 微服务电商后端

基于 **Go + Kratos + gRPC + etcd** 构建的微服务项目，包含 HTTP 网关、用户服务和商品服务三个模块。

## 目录

- [架构概览](#架构概览)
- [项目结构](#项目结构)
- [依赖环境](#依赖环境)
- [快速启动](#快速启动)
- [REST API 一览](#rest-api-一览)
- [添加新服务（完整流程）](#添加新服务完整流程)
- [常用命令速查](#常用命令速查)
- [配置说明](#配置说明)

---

## 架构概览

```
HTTP Client
    │
    │  REST HTTP :8080
    ▼
┌─────────────────────────────────┐
│           gateway               │
│  grpc-gateway + etcd discovery  │
└────────────┬──────────┬─────────┘
             │ gRPC     │ gRPC
    ┌────────▼──┐   ┌───▼────────┐
    │   user    │   │  product   │
    │  :9001    │   │   :9002    │
    └────┬──────┘   └──────┬─────┘
         │                 │
         └────────┬────────┘
                  │
         ┌────────▼────────┐
         │   MySQL         │
         │  store_micro    │
         └─────────────────┘

所有服务均通过 etcd 进行服务注册与发现
```

| 模块 | 职责 | 监听端口 | 协议 |
|------|------|----------|------|
| `gateway` | HTTP→gRPC 反向代理，对外暴露 REST 接口 | `:8080` | HTTP |
| `user` | 用户 CRUD 业务服务 | `:9001` | gRPC |
| `product` | 商品 CRUD 业务服务 | `:9002` | gRPC |

---

## 项目结构

```
storemicro/
├── gateway/                # HTTP 网关
│   ├── api/                # 生成的 gRPC/HTTP 客户端代码（protobuf 产物）
│   │   ├── product/v1/
│   │   └── user/v1/
│   ├── cmd/gateway/        # 程序入口
│   │   ├── main.go         # 启动、信号处理
│   │   ├── proxy.go        # GatewayProxy：gRPC 连接 + 路由注册
│   │   ├── appconfig.go    # 配置加载（YAML）
│   │   ├── logger.go       # zap 日志 + Kratos 桥接
│   │   └── flag.go         # 命令行参数
│   ├── configs/config.yaml # 配置文件
│   ├── third_party/        # protobuf 依赖（google/api）
│   └── go.mod
│
├── user/                   # 用户服务
│   ├── api/user/v1/        # 生成的 gRPC 代码
│   ├── cmd/user/           # 程序入口（main + wire + wire_gen）
│   ├── internal/
│   │   ├── biz/user.go     # 业务逻辑层（领域模型 + Usecase）
│   │   ├── service/user.go # gRPC 服务层（协议转换）
│   │   ├── data/           # 数据层（MySQL 操作）
│   │   ├── server/grpc.go  # Kratos gRPC 服务器
│   │   ├── conf/           # 配置结构（protobuf 生成）
│   │   └── pkg/log/        # zap 日志封装
│   ├── configs/config.yaml
│   ├── third_party/
│   └── go.mod
│
├── product/                # 商品服务（结构同 user）
│   ├── api/product/v1/
│   ├── cmd/product/
│   ├── internal/
│   │   ├── biz/product.go
│   │   ├── service/product.go
│   │   ├── data/
│   │   ├── server/grpc.go
│   │   ├── conf/
│   │   └── pkg/log/
│   ├── configs/config.yaml
│   ├── third_party/
│   └── go.mod
│
└── README.md               # 本文件
```

每个业务服务均遵循 **Kratos 三层架构**：

```
HTTP/gRPC 请求
    │
service 层   ← 协议转换，参数校验，错误码映射
    │
biz 层       ← 业务逻辑，领域规则，Usecase
    │
data 层      ← 数据库操作，实现 biz 定义的 Repo 接口
```

---

## 依赖环境

| 工具 | 版本要求 | 用途 |
|------|----------|------|
| Go | >= 1.21 | 编译运行 |
| protoc | >= 25.0 | 编译 `.proto` 文件 |
| protoc-gen-go | latest | 生成 Go 代码 |
| protoc-gen-go-grpc | latest | 生成 gRPC 代码 |
| protoc-gen-grpc-gateway | latest | 生成 HTTP 网关代码 |
| wire | latest | 依赖注入代码生成 |
| etcd | >= 3.5 | 服务注册与发现 |
| MySQL | >= 5.7 | 数据持久化 |

**安装 protoc 插件**

```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@latest
go install github.com/google/wire/cmd/wire@latest
```

**数据库初始化**

```sql
CREATE DATABASE store_micro DEFAULT CHARACTER SET utf8mb4;

USE store_micro;

CREATE TABLE users (
    id    BIGINT AUTO_INCREMENT PRIMARY KEY,
    name  VARCHAR(64)  NOT NULL,
    email VARCHAR(128) NOT NULL UNIQUE,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE products (
    id          BIGINT AUTO_INCREMENT PRIMARY KEY,
    name        VARCHAR(128) NOT NULL,
    description TEXT,
    price       BIGINT  NOT NULL DEFAULT 0,  -- 单位：分
    stock       INT     NOT NULL DEFAULT 0,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

---

## 快速启动

> 确保 etcd 和 MySQL 均已启动并可访问。

### 1. 启动 user 服务

```bash
cd user
go run ./cmd/user/... -conf configs/config.yaml
```

### 2. 启动 product 服务

```bash
cd product
go run ./cmd/product/... -conf configs/config.yaml
```

### 3. 启动 gateway

```bash
cd gateway
go run ./cmd/gateway/... -conf configs/config.yaml
```

### 4. 验证

```bash
# 健康检查
curl -X POST http://localhost:8080/api/v1/user/ping
curl -X POST http://localhost:8080/api/v1/product/ping

# 创建用户
curl -X POST http://localhost:8080/api/v1/user \
  -H "Content-Type: application/json" \
  -d '{"name":"张三","email":"zhangsan@example.com"}'

# 查询用户
curl http://localhost:8080/api/v1/user/1

# 查询商品列表
curl "http://localhost:8080/api/v1/product/list?page=1&page_size=10"
```

---

## REST API 一览

### 用户服务

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/api/v1/user` | 创建用户 |
| `GET` | `/api/v1/user/{id}` | 查询用户 |
| `POST` | `/api/v1/user/ping` | 健康检查 |

### 商品服务

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/api/v1/product/{id}` | 查询商品 |
| `GET` | `/api/v1/product/list` | 商品列表（支持分页） |
| `POST` | `/api/v1/product/ping` | 健康检查 |

---

## 添加新服务（完整流程）

以添加 `order`（订单）服务为例，完整演示从零到上线的所有步骤。

### 步骤一：复制目录骨架

```bash
# 以 product 为模板复制
cp -r product order
cd order
```

修改 `go.mod`，将模块名改为 `order`：

```
module order
```

### 步骤二：编写 Proto 文件

新建 `order/api/order/v1/order.proto`：

```protobuf
syntax = "proto3";

package api.order.v1;

option go_package = "order/api/order/v1;orderv1";

import "google/api/annotations.proto";

service Order {
  rpc CreateOrder (CreateOrderReq) returns (CreateOrderResp) {
    option (google.api.http) = {
      post: "/api/v1/order"
      body: "*"
    };
  }
  rpc GetOrder (GetOrderReq) returns (GetOrderResp) {
    option (google.api.http) = {
      get: "/api/v1/order/{id}"
    };
  }
  rpc Ping (PingReq) returns (PingResp) {
    option (google.api.http) = {
      post: "/api/v1/order/ping"
      body: "*"
    };
  }
}

message PingReq {}
message PingResp { string message = 1; }

message CreateOrderReq {
  int64 user_id    = 1;
  int64 product_id = 2;
  int32 quantity   = 3;
}
message CreateOrderResp {
  int64  id      = 1;
  string message = 2;
}
message GetOrderReq  { string id = 1; }
message GetOrderResp {
  int64 id         = 1;
  int64 user_id    = 2;
  int64 product_id = 3;
  int32 quantity   = 4;
  int64 amount     = 5;
}
```

### 步骤三：生成 gRPC + HTTP 代码

```bash
# 在 order 目录下执行
protoc \
  --proto_path=. \
  --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=. --grpc-gateway_opt=paths=source_relative \
  api/order/v1/order.proto
```

### 步骤四：实现业务代码

按照 Kratos 三层结构依次实现：

1. `internal/biz/order.go` — 定义领域模型 `Order` 和 `OrderRepo` 接口，实现 `OrderUsecase`
2. `internal/data/order.go` — 实现 `OrderRepo` 接口，写 MySQL 操作
3. `internal/service/order.go` — 实现 gRPC 服务，调用 `OrderUsecase`
4. `internal/server/grpc.go` — 注册 `OrderServer`
5. `cmd/order/wire.go` — 声明 Wire 注入关系

### 步骤五：生成 Wire 依赖注入代码

```bash
cd order/cmd/order
wire
```

### 步骤六：修改配置文件

`order/configs/config.yaml` 中修改 gRPC 端口（如 `:9003`）：

```yaml
server:
  grpc:
    addr: 0.0.0.0:9003
    timeout: 10s
```

### 步骤七：将新服务接入 gateway

**7.1** 将生成的 `api/order/v1/*.go` 文件复制到 `gateway/api/order/v1/`

**7.2** 在 `gateway/go.mod` 中添加引用（或直接复制文件）

**7.3** 修改 `gateway/cmd/gateway/proxy.go`，添加 order 服务连接和路由注册：

```go
import orderv1 "gateway/api/order/v1"

// 在 NewGatewayProxy 中添加：
proxy.orderConn, err = grpc.NewClient("discovery:///order", dialOpts...)

// 注册 order handler：
if err := orderv1.RegisterOrderHandler(ctx, proxy.mux, proxy.orderConn); err != nil {
    proxy.Close()
    return nil, fmt.Errorf("failed to register order handler: %w", err)
}
```

**7.4** 同步更新 `GatewayProxy` 结构体和 `Close()` 方法。

### 步骤八：启动并验证

```bash
# 启动 order 服务
cd order
go run ./cmd/order/... -conf configs/config.yaml

# 重启 gateway（加载新路由）
cd gateway
go run ./cmd/gateway/... -conf configs/config.yaml

# 测试
curl -X POST http://localhost:8080/api/v1/order/ping
```

---

## 常用命令速查

### 编译

```bash
# 编译单个服务（在对应服务目录执行）
go build -o bin/user ./cmd/user/...
go build -o bin/product ./cmd/product/...
go build -o bin/gateway ./cmd/gateway/...
```

### 运行（开发模式）

```bash
go run ./cmd/user/...    -conf configs/config.yaml
go run ./cmd/product/... -conf configs/config.yaml
go run ./cmd/gateway/... -conf configs/config.yaml
```

### protobuf 代码生成

```bash
# 在服务目录下执行，生成全部 proto
protoc \
  --proto_path=. \
  --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=. --grpc-gateway_opt=paths=source_relative \
  api/<service>/v1/<service>.proto

# 生成配置结构（conf.proto）
protoc \
  --proto_path=. \
  --go_out=. --go_opt=paths=source_relative \
  internal/conf/conf.proto
```

### Wire 依赖注入

```bash
# 在 cmd/<service> 目录下执行
wire

# 或指定目录
wire ./cmd/user/...
```

### 依赖管理

```bash
go mod tidy          # 整理依赖
go mod download      # 下载依赖
go mod vendor        # 导出 vendor 目录
```

### 测试

```bash
go test ./...                    # 运行所有测试
go test ./internal/biz/...       # 只测试 biz 层
go test -v -run TestXxx ./...    # 运行指定测试
go test -cover ./...             # 带覆盖率
```

### etcd 操作（调试用）

```bash
# 查看已注册的服务
etcdctl --endpoints=10.211.55.5:2379 get / --prefix --keys-only

# 查看具体服务信息
etcdctl --endpoints=10.211.55.5:2379 get /microservices/user --prefix
```

---

## 配置说明

### 业务服务（user / product）配置

```yaml
server:
  grpc:
    addr: 0.0.0.0:9001   # gRPC 监听地址
    timeout: 10s          # 请求超时

data:
  database:
    driver: mysql
    source: root:password@tcp(host:3306)/store_micro?charset=utf8mb4&parseTime=true&loc=Local

logger:
  level: info             # debug | info | warn | error

registry:
  etcd:
    endpoints:
      - 10.211.55.5:2379
    timeout: 10           # 连接超时（秒）
```

### gateway 配置

```yaml
name: storemicro
version: v1

server:
  addr: :8080             # HTTP 监听地址

etcd:
  endpoints:
    - 10.211.55.5:2379
  dial_timeout: 5s

log:
  level: info
```

---

## 分层说明

```
┌──────────────────────────────────────┐
│  service 层                          │
│  - 实现 gRPC Server 接口             │
│  - 参数类型转换（proto ↔ domain）    │
│  - gRPC 错误码映射（mapError）       │
├──────────────────────────────────────┤
│  biz 层                              │
│  - 领域模型定义（struct）            │
│  - Repo 接口声明                     │
│  - 业务规则 / 参数校验               │
├──────────────────────────────────────┤
│  data 层                             │
│  - 实现 biz.Repo 接口               │
│  - 执行 SQL 操作                     │
│  - 连接池管理                        │
└──────────────────────────────────────┘
```

扩展业务时，**只需在 biz 层定义规则、在 data 层实现存储、在 service 层暴露接口**，三层互相隔离，便于单独测试和替换。
