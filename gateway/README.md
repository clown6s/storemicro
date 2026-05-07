# gateway — HTTP 网关服务

将外部 REST HTTP 请求转发为内部 gRPC 调用，同时通过 etcd 动态发现后端服务实例。

## 目录

- [职责](#职责)
- [目录结构](#目录结构)
- [配置文件](#配置文件)
- [启动方式](#启动方式)
- [对外 REST 接口](#对外-rest-接口)
- [请求流转过程](#请求流转过程)
- [扩展：接入新服务](#扩展接入新服务)
- [扩展：添加中间件](#扩展添加中间件)
- [常用命令](#常用命令)

---

## 职责

| 能力 | 说明 |
|------|------|
| 协议转换 | REST JSON ↔ gRPC Protobuf（由 grpc-gateway 生成代码驱动） |
| 服务发现 | 通过 etcd + Kratos discovery resolver 动态解析后端地址 |
| 负载均衡 | `round_robin` 策略，自动轮询多个后端实例 |
| 优雅退出 | 监听 SIGINT/SIGTERM，等待当前请求完成后关闭 |

---

## 目录结构

```
gateway/
├── api/
│   ├── product/v1/
│   │   ├── product.proto          # 接口定义（源文件）
│   │   ├── product.pb.go          # protobuf 消息结构（生成）
│   │   ├── product_grpc.pb.go     # gRPC 客户端/服务端接口（生成）
│   │   └── product.pb.gw.go      # grpc-gateway HTTP 路由（生成）
│   └── user/v1/
│       ├── user.proto
│       ├── user.pb.go
│       ├── user_grpc.pb.go
│       └── user.pb.gw.go
├── cmd/gateway/
│   ├── main.go        # 程序入口，HTTP 服务器生命周期
│   ├── proxy.go       # GatewayProxy：etcd 连接、gRPC 连接、路由注册
│   ├── appconfig.go   # YAML 配置加载，DefaultAppConfig
│   ├── logger.go      # zap 日志，Kratos log.Logger 桥接
│   └── flag.go        # -conf 命令行参数
├── configs/
│   └── config.yaml    # 配置文件
├── third_party/
│   └── google/api/    # protobuf HTTP 注解依赖
└── go.mod
```

---

## 配置文件

`configs/config.yaml`

```yaml
name: storemicro
version: v1

# HTTP 服务器
server:
  addr: :8080          # 监听地址，可改为 0.0.0.0:8080

# etcd 服务发现
etcd:
  endpoints:
    - 10.211.55.5:2379  # etcd 集群地址，多节点用多行列出
  dial_timeout: 5s       # 连接超时

# 日志级别：debug | info | warn | error
log:
  level: info
```

---

## 启动方式

### 开发模式（直接运行源码）

```bash
cd gateway
go run ./cmd/gateway/... -conf configs/config.yaml
```

### 编译后运行

```bash
cd gateway
go build -o bin/gateway ./cmd/gateway/...
./bin/gateway -conf configs/config.yaml
```

### 使用默认配置路径

如果不传 `-conf` 参数，程序会按以下顺序自动查找配置文件目录：

1. 可执行文件同级目录下的 `configs/`
2. 可执行文件上级目录下的 `configs/`
3. 当前工作目录下的 `configs/`

---

## 对外 REST 接口

所有路由由 proto 文件中的 `google.api.http` 注解驱动，grpc-gateway 自动生成，**不需要手写路由代码**。

### 用户服务

| 方法 | 路径 | 后端 gRPC | 说明 |
|------|------|-----------|------|
| `POST` | `/api/v1/user` | `User/CreateUser` | 创建用户 |
| `GET` | `/api/v1/user/{id}` | `User/GetUser` | 按 ID 查询用户 |
| `POST` | `/api/v1/user/ping` | `User/Ping` | 健康检查 |

**创建用户请求示例**

```bash
curl -X POST http://localhost:8080/api/v1/user \
  -H "Content-Type: application/json" \
  -d '{"name":"张三","email":"zhangsan@example.com"}'
```

响应：

```json
{"id": 1, "message": "created"}
```

**查询用户示例**

```bash
curl http://localhost:8080/api/v1/user/1
```

响应：

```json
{"id": 1, "name": "张三", "email": "zhangsan@example.com"}
```

---

### 商品服务

| 方法 | 路径 | 后端 gRPC | 说明 |
|------|------|-----------|------|
| `GET` | `/api/v1/product/{id}` | `Product/GetProduct` | 按 ID 查询商品 |
| `GET` | `/api/v1/product/list` | `Product/ListProducts` | 商品列表（分页） |
| `POST` | `/api/v1/product/ping` | `Product/Ping` | 健康检查 |

**查询商品示例**

```bash
curl http://localhost:8080/api/v1/product/1
```

**分页列表示例**

```bash
curl "http://localhost:8080/api/v1/product/list?page=1&page_size=10"
```

响应：

```json
{
  "items": [
    {"id":1,"name":"商品A","description":"描述","price":9900,"stock":100}
  ],
  "total": 1
}
```

---

## 请求流转过程

```
curl POST /api/v1/user
    │
    │ HTTP JSON
    ▼
gateway (grpc-gateway ServeMux)
    │  反序列化 JSON → CreateUserReq protobuf
    │
    │ gRPC (discovery:///user)
    ▼
etcd discovery resolver
    │  查询 etcd 获取 user 服务实例地址
    │
    │ gRPC TCP
    ▼
user service :9001
    │
    │ gRPC protobuf 响应
    ▼
gateway
    │  序列化 protobuf → JSON
    │
    │ HTTP JSON
    ▼
curl
```

---

## 扩展：接入新服务

以接入 `order`（订单）服务为例：

### 第一步：复制 proto 生成文件

将 order 服务的 proto 生成文件（`*.pb.go`、`*_grpc.pb.go`、`*.pb.gw.go`）复制到：

```
gateway/api/order/v1/
```

或者直接在此目录下重新执行 protoc 生成。

### 第二步：修改 `proxy.go`

**a. 添加 import**

```go
import orderv1 "gateway/api/order/v1"
```

**b. 在 `GatewayProxy` 结构体中添加字段**

```go
type GatewayProxy struct {
    mux         *runtime.ServeMux
    userConn    *grpc.ClientConn
    productConn *grpc.ClientConn
    orderConn   *grpc.ClientConn   // 新增
    etcdClient  *clientv3.Client
}
```

**c. 在 `NewGatewayProxy` 中建立连接并注册路由**

```go
// 建立 gRPC 连接
proxy.orderConn, err = grpc.NewClient("discovery:///order", dialOpts...)
if err != nil {
    proxy.Close()
    return nil, fmt.Errorf("failed to connect order service: %w", err)
}

// 注册 HTTP 路由
if err := orderv1.RegisterOrderHandler(ctx, proxy.mux, proxy.orderConn); err != nil {
    proxy.Close()
    return nil, fmt.Errorf("failed to register order handler: %w", err)
}
```

**d. 在 `Close()` 中释放连接**

```go
if p.orderConn != nil {
    p.orderConn.Close()
}
```

### 第三步：更新 `go.mod` 依赖

```bash
go mod tidy
```

### 第四步：重启 gateway

```bash
go run ./cmd/gateway/... -conf configs/config.yaml
```

---

## 扩展：添加中间件

在 `proxy.go` 的 `runtime.NewServeMux(...)` 中添加选项，可对所有请求生效：

### 添加请求日志

```go
proxy.mux = runtime.NewServeMux(
    runtime.WithErrorHandler(runtime.DefaultHTTPErrorHandler),
    runtime.WithMiddlewares(func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            zapLog.Info("incoming request",
                zap.String("method", r.Method),
                zap.String("path", r.URL.Path),
            )
            next.ServeHTTP(w, r)
        })
    }),
)
```

### 添加认证拦截

```go
runtime.WithMetadata(func(ctx context.Context, r *http.Request) metadata.MD {
    token := r.Header.Get("Authorization")
    return metadata.Pairs("authorization", token)
}),
```

在后端 gRPC 服务中从 context 读取 metadata 即可获得 token。

---

## 常用命令

```bash
# 运行
go run ./cmd/gateway/... -conf configs/config.yaml

# 编译
go build -o bin/gateway ./cmd/gateway/...

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
