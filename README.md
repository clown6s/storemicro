# storemicro — 微服务电商后端

基于 **Go + Kratos v2 + gRPC + etcd** 构建的微服务项目，网关统一认证，内部服务专注业务。

## 目录

- [架构概览](#架构概览)
- [技术栈](#技术栈)
- [项目结构](#项目结构)
- [依赖环境](#依赖环境)
- [快速启动](#快速启动)
- [添加新服务（完整流程）](#添加新服务完整流程)
- [常用命令速查](#常用命令速查)

---

## 架构概览

```
HTTP Client
    │
    │  REST HTTP :8080
    ▼
┌──────────────────────────────────────┐
│              gateway (:8080)          │
│  grpc-gateway + JWT 认证 + Redis     │
│  OpenTelemetry HTTP/gRPC tracing     │
└──────┬──────────┬──────────┬─────────┘
       │ gRPC     │ gRPC     │ gRPC
  ┌────▼───┐ ┌────▼────┐ ┌──▼────────┐
  │  user  │ │ product │ │ district  │
  │ :9001  │ │  :9002  │ │  :9003    │
  └────┬───┘ └────┬────┘ └─────┬─────┘
       │          │            │
       └────┬─────┴─────┬──────┘
            │           │
     ┌──────▼──┐  ┌─────▼─────┐
     │  MySQL  │  │   Redis   │
     │store_micro│ │ token校验  │
     └─────────┘  └───────────┘
            │
     ┌──────▼──────┐
     │    etcd     │
     │ 服务注册发现 │
     └─────────────┘
```

| 模块 | 职责 | 监听端口 | 协议 |
|------|------|----------|------|
| `gateway` | HTTP→gRPC 反向代理，JWT 认证，Redis token 校验，链路追踪 | `:8080` | HTTP |
| `user` | 用户注册/登录/登出/查询，Redis 单设备登录 | `:9001` | gRPC |
| `product` | 商品 CRUD 业务服务 | `:9002` | gRPC |
| `district` | 区域/地址业务服务 | `:9003` | gRPC |

---

## 技术栈

| 组件 | 技术 | 说明 |
|------|------|------|
| 框架 | Kratos v2 | gRPC 服务框架，中间件链 |
| 协议 | gRPC + Protobuf | 内部服务间通信 |
| 网关 | grpc-gateway | REST JSON ↔ gRPC 自动转换 |
| 服务发现 | etcd | 服务注册与发现，雪花算法 node_id 分配 |
| 数据库 | MySQL | 数据持久化 |
| 缓存 | Redis | token 双映射，单设备登录 |
| 认证 | JWT (HS256) | 网关统一鉴权，白名单放行 |
| ID 生成 | 雪花算法 | 基于 etcd 自动分配 node_id |
| 链路追踪 | OpenTelemetry + OTLP gRPC → Jaeger | 全链路 trace_id 传递 |
| 日志 | zap | 结构化日志，Kratos log 桥接 |
| 依赖注入 | Wire | 编译时依赖注入 |

---

## 项目结构

```
storemicro/
├── pkg/tracing/            # 公共链路追踪包（独立 go module）
│   ├── tracing.go          # OTLP gRPC exporter + TracerProvider 初始化
│   └── go.mod
│
├── gateway/                # HTTP 网关
│   ├── api/                # protobuf 生成代码
│   │   ├── product/v1/
│   │   ├── user/v1/
│   │   └── district/v1/
│   ├── cmd/gateway/
│   │   ├── main.go         # 启动、tracing 初始化、信号处理
│   │   ├── proxy.go        # GatewayProxy：etcd + gRPC + Redis 连接
│   │   ├── registry.go     # services 注册表（新增服务加一行）
│   │   ├── auth.go         # JWT 认证中间件 + Redis token 校验
│   │   ├── appconfig.go    # 配置加载
│   │   └── logger.go       # zap 日志
│   ├── configs/config.yaml
│   └── third_party/        # protobuf 依赖（google/api + google/protobuf）
│
├── user/                   # 用户服务
│   ├── api/user/v1/        # protobuf 生成代码
│   ├── cmd/user/           # main + wire + wire_gen
│   ├── internal/
│   │   ├── biz/            # 领域模型 + Usecase
│   │   ├── service/        # gRPC 服务实现
│   │   ├── data/           # MySQL 实现 + Redis/JWT 封装
│   │   ├── server/         # Kratos gRPC 服务器
│   │   ├── conf/           # 配置结构（protobuf 生成）
│   │   └── pkg/            # snowflake, log, jwt, redis
│   ├── configs/config.yaml
│   └── third_party/
│
├── product/                # 商品服务（结构同 user）
├── district/               # 区域服务（结构同 user）
└── README.md
```

### Kratos 三层架构

```
service 层   ← gRPC 接口实现，参数转换，错误码映射
    │
biz 层       ← 业务逻辑，领域规则，Usecase
    │
data 层      ← 数据库操作，实现 biz 定义的 Repo 接口
```

### 请求链路

```
Client → gateway(JWT认证) → gateway(grpc-gateway) → etcd(服务发现) → user/product(gRPC)
                ↓                                          ↓
         Redis(token校验)                           MySQL(数据存储)
```

---

## 依赖环境

| 工具 | 用途 |
|------|------|
| Go >= 1.21 | 编译运行 |
| protoc | 编译 `.proto` 文件 |
| protoc-gen-go | 生成 Go protobuf 代码 |
| protoc-gen-go-grpc | 生成 gRPC 代码 |
| protoc-gen-grpc-gateway | 生成 HTTP 网关代码（仅 gateway 需要） |
| wire | 编译时依赖注入代码生成 |
| etcd >= 3.5 | 服务注册与发现 |
| MySQL >= 5.7 | 数据持久化 |
| Redis | token 校验 |
| Jaeger | 链路追踪（OTLP gRPC :4317，UI :16686） |

**安装 protoc 插件**

```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@latest
go install github.com/google/wire/cmd/wire@latest
```

---

## 快速启动

> 确保 etcd、MySQL、Redis 均已启动并可访问。

```bash
# 1. 启动 user 服务
cd user && go run ./cmd/user/ -conf configs/config.yaml

# 2. 启动 product 服务
cd product && go run ./cmd/product/ -conf configs/config.yaml

# 3. 启动 district 服务
cd district && go run ./cmd/district/ -conf configs/config.yaml

# 4. 启动 gateway（最后启动，依赖其他服务注册到 etcd）
cd gateway && go run ./cmd/gateway/ -conf configs/config.yaml

# 5. 验证
curl -X POST http://localhost:8080/api/v1/user/ping
curl -X POST http://localhost:8080/api/v1/product/ping
```

---

## 添加新服务（完整流程）

以添加 `order`（订单）服务为例。

### 1. 复制目录骨架

```bash
cp -r product order
cd order
# 修改 go.mod: module order
# 修改 internal/ 下的包名引用: product → order
# 修改 cmd/ 下的包名引用
```

### 2. 编写 Proto 文件

编辑 `api/order/v1/order.proto`，定义 service + message，配置 `google.api.http` 注解。

### 3. 生成代码

```bash
# 业务 proto
protoc \
  --proto_path=. --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  api/order/v1/order.proto

# 配置 proto
protoc \
  --proto_path=. --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  internal/conf/conf.proto
```

### 4. 实现业务代码

按 Kratos 三层结构依次实现：

1. `internal/biz/order.go` — 领域模型 + Repo 接口 + Usecase
2. `internal/data/order.go` — Repo 接口的 MySQL 实现
3. `internal/service/order.go` — gRPC 服务实现
4. `internal/server/grpc.go` — 注册 OrderServer
5. 各层的 `ProviderSet` 加入新构造函数

### 5. Wire 依赖注入

编辑 `cmd/order/wire.go`，声明依赖关系，然后生成：

```bash
cd cmd/order && wire
```

### 6. 修改配置

`configs/config.yaml` 中修改 gRPC 端口（避免冲突），添加 tracing endpoint。

### 7. 接入 gateway

**7.1** 复制 proto 文件到 gateway：

```bash
cp api/order/v1/order.proto ../gateway/api/order/v1/
```

**7.2** 在 gateway 目录生成代码：

```bash
cd ../gateway
protoc \
  --proto_path=. --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=. --grpc-gateway_opt=paths=source_relative \
  api/order/v1/order.proto
```

**7.3** 修改 `gateway/cmd/gateway/registry.go`，新增一行：

```go
var services = []ServiceRegistration{
    {Name: "user", RegisterHandler: userv1.RegisterUserHandler},
    {Name: "product", RegisterHandler: productv1.RegisterProductHandler},
    {Name: "order", RegisterHandler: orderv1.RegisterOrderHandler},  // 新增
}
```

> 不需要改 `proxy.go`，它会自动循环 `services` 注册。

**7.4** 如果 order 的 ping 不需要认证，在 `auth.go` 的 `noAuthPaths` 中添加白名单。

### 8. 启动验证

```bash
cd order && go run ./cmd/order/ -conf configs/config.yaml
cd gateway && go run ./cmd/gateway/ -conf configs/config.yaml
curl -X POST http://localhost:8080/api/v1/order/ping
```

---

## 常用命令速查

### 编译与运行

```bash
go build -o bin/user ./cmd/user/           # 编译
go run ./cmd/user/ -conf configs/config.yaml  # 运行
```

### Proto 代码生成

```bash
# 业务服务（user/product/district）
protoc --proto_path=. --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  api/user/v1/user.proto

# gateway（需要额外生成 grpc-gateway）
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
cd cmd/user && wire     # 在 cmd/<service> 目录下执行
```

### 依赖管理

```bash
go mod tidy
go mod download
```

### etcd 调试

```bash
etcdctl --endpoints=10.211.55.5:2379 get / --prefix --keys-only
```

### Jaeger 链路追踪

- OTLP gRPC: `10.211.55.5:4317`
- Jaeger UI: `http://10.211.55.5:16686`
- 服务名: `storemicro-gateway`、`storemicro-user`、`storemicro-product`

---

## 关键设计

### 网关统一认证

- JWT 签名校验 + Redis token 存在性校验
- 白名单路径（register/login/ping）免认证
- 内部 gRPC 服务不重复鉴权，信任 gateway 转发

### 单设备登录

- Redis 双映射：`storemicro:token:{token} → userID` + `storemicro:user_token:{userID} → token`
- 重复登录时删除旧 token，新 token 生效
- 被踢的旧 token 在 gateway 层被 Redis 校验拦截返回 401

### 雪花算法 ID

- 基于 etcd 分布式锁自动分配 node_id
- 租约续约保活，实例下线自动释放
- 无需配置文件指定 node_id
