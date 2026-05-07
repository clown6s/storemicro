# gateway

Envoy 网关配置目录，为 storemicro 微服务提供统一的 HTTP 入口和 gRPC 协议转换。

## 架构

```
Client (HTTP)
    │
    ▼
┌──────────────┐
│   Envoy      │  :8080 (HTTP)
│  (网关)      │
└──┬───────────┘
   │  ADS (gRPC :18000)
   ▼
┌──────────────┐
│ control-plane│  xDS 动态配置下发
│              │  监听 etcd 服务变化
└──┬───────────┘
   │  Watch /kratos/registry/
   ▼
┌──────────────┐
│    etcd      │  :2379
└──┬───────────┘
   │  服务注册
   ▼
┌──────────────┐  ┌──────────────┐
│  user svc   │  │ product svc │
│  (gRPC :9001)│  │(gRPC :9002)│
└──────────────┘  └──────────────┘
```

## 目录结构

```
gateway/
├── envoy.yaml          # Envoy 主配置（动态 xDS 模式）
├── descriptor.pb     # proto 描述符（gRPC-JSON 转码用）
├── protos/           # 构建描述符所需的 proto 依赖
│   └── google/api/
│       ├── annotations.proto
│       └── http.proto
└── logs/             # 服务运行日志目录
```

## 快速开始

### 前置条件

- etcd 已运行（默认 `10.211.55.5:2379`）
- user / product 服务已启动（注册到 etcd）
- control-plane 已编译并启动（gRPC `:18000`）

### 启动 Envoy

```bash
# 使用系统安装的 envoy 二进制
envoy -c D:/project/storemicro/gateway/envoy.yaml

# 指定日志路径
envoy -c D:/project/storemicro/gateway/envoy.yaml --log-path D:/project/storemicro/gateway/logs/envoy.log
```

### 验证

```bash
# 查看 Envoy 管理接口
curl http://localhost:9901/ready          # 就绪检查
curl http://localhost:9901/config_dump    # 查看当前 xDS 配置

# 测试用户服务
curl http://localhost:8080/api/v1/user/1

# 测试商品服务
curl http://localhost:8080/api/v1/product/1
```

## 配置文件说明

### envoy.yaml

```yaml
# 动态资源配置（通过 ADS 从 control-plane 获取 LDS/CDS）
dynamic_resources:
  lds_config: { ads: {} }
  cds_config: { ads: {} }
  ads_config:
    api_type: GRPC
    transport_api_version: V3
    grpc_services:
      - envoy_grpc: { cluster_name: xds_control_plane }

# 静态资源（control-plane 连接信息）
static_resources:
  clusters:
    - name: xds_control_plane
      type: STATIC
      load_assignment:
        cluster_name: xds_control_plane
        endpoints:
          - lb_endpoints:
              - endpoint:
                  address:
                    socket_address:
                      address: 127.0.0.1
                      port_value: 18000
      typed_extension_protocol_options:
        envoy.extensions.upstreams.http.v3.HttpProtocolOptions:
          "@type": type.googleapis.com/envoy.extensions.upstreams.http.v3.HttpProtocolOptions
          explicit_http_config:
            http2_protocol_options: {}

# 管理接口
admin:
  address:
    socket_address:
      address: 0.0.0.0
      port_value: 9901
```

### descriptor.pb

由 `protoc` 生成的 proto 描述符文件，供 Envoy 的 `grpc_json_transcoder` 过滤器使用，实现 HTTP → gRPC 自动转码。

**重新生成：**

```bash
# 需要 protoc v3.21+ 和 google/api proto 文件
protoc \
  --proto_path=D:/project/storemicro/user/api \
  --proto_path=D:/project/storemicro/product/api \
  --proto_path=D:/project/storemicro/gateway/protos \
  --proto_path=D:/project/storemicro/user/third_party \
  --proto_path=D:/project/storemicro/product/third_party \
  --include_imports \
  -o D:/project/storemicro/gateway/descriptor.pb \
  D:/project/storemicro/user/api/user/v1/user.proto \
  D:/project/storemicro/product/api/product/v1/product.proto
```

## gRPC-JSON 转码

Envoy 通过 `grpc_json_transcoder` 过滤器将 HTTP/JSON 请求自动转换为后端 gRPC 调用。

### 工作原理

```
HTTP Request (JSON)
    │
    ▼
┌─────────────────────┐
│  grpc_json_transcoder │  ← 读取 descriptor.pb
│                      │     路由到 api.user.v1.User
└────────┬────────────┘
         ▼
    gRPC Request (protobuf)
         │
         ▼
┌─────────────────────┐
│   user gRPC :9001    │
└─────────────────────┘
```

### 后端 gRPC 服务要求

1. proto 文件中定义了 `google.api.http` 注解，例如：

```protobuf
service User {
  rpc GetUser(GetUserRequest) returns (GetUserResponse) {
    option (google.api.http) = {
      get: "/api/v1/user/{id}"
    };
  }
}
```

2. `descriptor.pb` 必须通过 `--include_imports` 生成，否则 Envoy 无法解析 `google.api.http` 注解。

## xDS 动态配置

Envoy 不再使用静态 upstream 配置，所有路由和集群都通过 ADS（Aggregated Discovery Service）从 control-plane 动态获取。

### 配置下发流程

```
control-plane                    Envoy
    │                              │
    │◄────── ADS Stream ──────────┤
    │                              │
    │──── LDS 快照（listener）────►│
    │──── RDS 快照（route）───────►│
    │──── CDS 快照（cluster）──────►│
    │──── EDS 快照（endpoint）─────►│
    │                              │
    │  [etcd 实例变化]             │
    │       │                      │
    │       ▼                      │
    │  重新生成快照                 │
    │       │                      │
    │──── 增量推送 ──────────────►│
```

### 各 xDS 资源说明

| 类型 | 说明 | 来源 |
|------|------|------|
| LDS | Listener `http_listener`，监听 `:8080`，挂载 HTTP Connection Manager | control-plane 生成 |
| RDS | Route `local_route`，按 `route_prefix` 前缀匹配转发到对应 cluster | control-plane 从 config.yaml 读取 |
| CDS | 每个 service 一个 cluster，类型为 EDS，启用 gRPC 健康检查 | control-plane 从 config.yaml 读取 |
| EDS | ClusterLoadAssignment，endpoints 从 etcd 服务实例动态填充 | control-plane Watch etcd 实时更新 |

## 运维

### Envoy 管理接口

Envoy 在 `:9901` 提供管理 HTTP 接口：

| 接口 | 说明 |
|------|------|
| `/ready` | 就绪状态 |
| `/config_dump` | 当前生效的 xDS 配置（调试必用） |
| `/stats` | 运行时统计指标 |
| `/logging` | 动态修改日志级别 |
| `/healthcheck/fail` | 主动标记为不健康（运维场景） |
| `/healthcheck/ok` | 恢复为健康 |

### 查看当前生效的路由配置

```bash
# 查看完整配置
curl -s http://localhost:9901/config_dump | jq '.configs[].dynamicRouteConfigs'

# 只看 route 配置
curl -s http://localhost:9901/config_dump?resource=dynamic_route_configs | jq
```

### 日志级别

```bash
# 开启 debug 日志
curl -X POST http://localhost:9901/logging?level=debug

# 只看 upstream 日志
curl -X POST http://localhost:9901/logging?upstream=debug
```

## 排错指南

### Envoy 启动失败

```bash
# 检查配置文件语法
envoy --mode validate -c D:/project/storemicro/gateway/envoy.yaml

# 查看详细错误日志
envoy -c D:/project/storemicro/gateway/envoy.yaml --log-level debug
```

### 路由 404

1. 检查 control-plane 是否正常推送 xDS 配置：`curl localhost:9901/config_dump`
2. 检查 `config.yaml` 中 `route_prefix` 是否正确
3. 检查 etcd 中服务实例是否注册成功

### gRPC 转码失败（HTTP 415 / 未定义路径）

1. 确认 `descriptor.pb` 是用 `--include_imports` 生成的
2. 确认 proto 文件中写了 `google.api.http` 注解
3. 检查 `config.yaml` 中 `transcoder.proto_descriptor` 路径是否正确（Envoy 容器内的路径）

## 常用命令

```bash
# 查看 Envoy 版本
envoy --version

# 验证配置文件
envoy --mode validate -c gateway/envoy.yaml

# 前台运行（调试用）
envoy -c gateway/envoy.yaml --log-level debug

# 查看 xDS 配置
curl -s http://localhost:9901/config_dump | jq

# 测试接口
curl http://localhost:8080/api/v1/user/1
curl -X POST http://localhost:8080/api/v1/user -d '{"name":"test","email":"test@test.com","password":"123456"}' -H "Content-Type: application/json"
```
