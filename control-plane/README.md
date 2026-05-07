# control-plane

xDS 控制平面服务，基于 [go-control-plane](https://github.com/envoyproxy/go-control-plane) 实现，为 Envoy 网关提供动态配置下发能力。

## 架构

```
                         ┌─────────────────────┐
                         │    control-plane     │
                         │    (ADS gRPC :18000) │
                         └──────┬──────────┬────┘
                    xDS 推送   │          │  Watch
                ┌──────────────┘          └─────────────┐
                ▼                                         ▼
        ┌─────────────┐                           ┌───────────┐
        │    Envoy     │                           │   etcd    │
        │  (HTTP :8080)│                           │  :2379    │
        └─────────────┘                           └─────┬─────┘
                                                 注册   │
                                               ┌────────┘
                                               ▼
                                       ┌──────────────┐
                                       │ user / product│
                                       │  Kratos 服务   │
                                       └──────────────┘
```

## 目录结构

```
control-plane/
├── main.go        # 入口，加载配置、启动 ADS gRPC 服务
├── config.go      # 配置结构体定义 + YAML 加载 + Logger 构建
├── config.yaml    # 运行配置（服务、端口、etcd、日志）
├── watcher.go     # etcd 服务注册监听
├── snapshot.go    # xDS 快照生成与推送
├── resources.go   # Envoy xDS 资源生成（LDS/RDS/CDS/EDS）
├── ads.go         # ADS gRPC 服务注册
├── go.mod         # Go 模块依赖
└── go.sum
```

## 快速开始

### 前置条件

- Go 1.21+
- etcd 已运行
- Envoy 已安装

### 编译

```bash
cd control-plane
go build -o control-plane .
```

### 运行

```bash
# 使用默认配置
./control-plane

# 指定配置文件
./control-plane -config /path/to/config.yaml
```

## 配置说明

完整配置文件 `config.yaml`：

```yaml
# HTTP Listener 端口（Envoy 对外监听）
listener:
  port: 8080

# gRPC-JSON Transcoder
transcoder:
  proto_descriptor: "/root/object/storemicro/gateway/descriptor.pb"

# 日志配置
log:
  level: "info"       # debug | info | warn | error
  file: ""            # 日志文件路径，空则仅 stdout
  max_age: 7          # 日志保留天数（需 file 非空）
  max_size: 100       # 单个日志文件最大 MB

# etcd 连接
etcd:
  endpoints:
    - "10.211.55.5:2379"
  prefix: "/kratos/registry/"

# ADS 服务端口
ads:
  port: 18000

# 服务路由配置
services:
  - name: user
    grpc_service: "api.user.v1.User"
    route_prefix: "/api/v1/user"
  - name: product
    grpc_service: "api.product.v1.Product"
    route_prefix: "/api/v1/product"
```

### 添加新服务

只需在 `services` 列表下追加条目，无需改代码：

```yaml
services:
  # ... 已有服务 ...

  - name: order
    grpc_service: "api.order.v1.Order"
    route_prefix: "/api/v1/order"
```

字段说明：

| 字段 | 说明 |
|------|------|
| `name` | 服务名，需与 etcd 中的 Kratos 注册名一致 |
| `grpc_service` | proto 中定义的 gRPC 服务全名（包名.服务名） |
| `route_prefix` | HTTP 路由前缀，匹配的请求会转发到该服务 |

修改后重启 control-plane 即可生效。

## 日志

使用 [uber-go/zap](https://github.com/uber-go/zap) 结构化日志，JSON 格式输出。

### 配置项

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `level` | string | `info` | 日志级别：debug / info / warn / error |
| `file` | string | `""` | 日志文件路径，为空则仅输出到 stdout |
| `max_age` | int | `7` | 日志文件保留天数 |
| `max_size` | int | `100` | 单个日志文件最大大小（MB） |

### 日志输出示例

```json
{"level":"INFO","ts":"2026-05-07T14:30:00.000+0800","caller":"main.go:34","msg":"control-plane starting","services":2,"listener_port":8080,"ads_port":18000,"etcd_endpoints":["10.211.55.5:2379"]}
{"level":"INFO","ts":"2026-05-07T14:30:00.100+0800","caller":"watcher.go:83","msg":"watching etcd prefix","prefix":"/kratos/registry/"}
{"level":"INFO","ts":"2026-05-07T14:30:00.200+0800","caller":"snapshot.go:42","msg":"generating snapshot","version":"v1","services":["user","product"]}
{"level":"INFO","ts":"2026-05-07T14:30:00.210+0800","caller":"snapshot.go:67","msg":"snapshot pushed","version":"v1","node":"storemicro-gateway"}
```

### 日志组件

每个模块使用 `zap.Named()` 创建带前缀的子 logger：

| Logger 名 | 组件 | 说明 |
|-----------|------|------|
| _(root)_ | main | 启动信息、配置加载 |
| `watcher` | EtcdWatcher | etcd 监听事件、实例解析 |
| `snapshot` | SnapshotManager | xDS 快照生成与推送 |
| `resources` | resources.go | 端点地址解析 |

### 同时输出到文件和终端

```yaml
log:
  level: "debug"
  file: "/var/log/control-plane/control-plane.log"
```

## 工作流程

1. **启动时**：从 `config.yaml` 加载配置 → 连接 etcd → 加载已注册的服务实例 → 生成初始 xDS 快照 → 等待 Envoy 连接
2. **运行时**：Watch etcd 注册前缀 → 检测到服务实例变化（上线/下线）→ 重新生成 xDS 快照 → 推送给所有已连接的 Envoy 节点
3. **Envoy 连接**：Envoy 通过 ADS (gRPC) 连接到 control-plane → 获取 LDS/RDS/CDS/EDS 配置 → 配置变更时自动接收增量更新

## xDS 资源

| 类型 | 说明 | 数量 |
|------|------|------|
| LDS | HTTP Listener（:8080），含 HealthCheck + gRPC-JSON Transcoder + Router | 1 |
| RDS | 路由配置，按 `route_prefix` 前缀匹配转发到对应 cluster | 1 |
| CDS | 每个 service 对应一个 EDS cluster，启用 gRPC 健康检查 | N 个服务 |
| EDS | ClusterLoadAssignment，从 etcd 服务实例动态填充 | N 个服务 |

## 依赖

| 依赖 | 版本 | 说明 |
|------|------|------|
| `go-control-plane` | v0.14.0 | Envoy xDS 控制面库 |
| `go-control-plane/envoy` | v1.37.0 | Envoy protobuf 定义 |
| `etcd/client/v3` | v3.6.11 | etcd 客户端 |
| `zap` | v1.28.0 | 结构化日志 |
| `yaml.v3` | v3.0.1 | YAML 配置解析 |
| `grpc` | v1.81.0 | gRPC 框架 |

## 命令行参数

```
./control-plane -config <path>   指定配置文件路径（默认 config.yaml）
```
