# District — 行政区划微服务

基于 **Kratos v2.9 + gRPC + etcd** 的行政区划服务，提供中国省/市/区三级树形数据查询。通过 etcd 注册服务实例，供 gateway 发现调用。无认证、无 Redis、无 JWT，职责单一——只做行政区划查询。

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

遵循 Kratos **Clean Architecture** 分层，与 user 服务完全一致的模式：

```
┌─────────────────────────────────────────┐
│  server/grpc.go                         │
│  传输层：gRPC Server + 中间件链         │
│  Recovery → Tracing → Logging → Validate│
├─────────────────────────────────────────┤
│  service/district.go                    │
│  适配层：proto ↔ biz 转换 + 树形构建   │
├─────────────────────────────────────────┤
│  biz/district.go                        │
│  业务层：领域模型 District + 参数校验   │
│  定义 DistrictRepo 接口（依赖倒置）     │
├─────────────────────────────────────────┤
│  data/district.go                       │
│  数据层：递归 CTE 查询 + 扁平→树转换   │
├─────────────────────────────────────────┤
│  MySQL / etcd                           │
└─────────────────────────────────────────┘
```

与 user 服务的区别：

| 特性 | user | district |
|------|------|----------|
| Redis | 有（token 管理） | 无 |
| JWT | 有（生成/解析） | 无 |
| Snowflake ID | 有（etcd 租约） | 无 |
| 认证 | gateway 层 | gateway 层 |
| 职责 | 用户 CRUD + 认证 | 行政区划查询（只读） |

---

## 核心设计

### 1. 递归 CTE 树形查询

查询某个行政区划及其所有下级，使用 MySQL 递归 CTE（Common Table Expression）：

```sql
-- 向上查找：从目标节点走到省级行政区（根节点）
WITH RECURSIVE ancestors AS (
    SELECT * FROM district WHERE ad_code = '110101'     -- 起始节点
    UNION ALL
    SELECT d.* FROM district d
    JOIN ancestors a ON d.ad_code = a.parent_ad_code   -- 向上递归
    WHERE a.level != 'province'                          -- 到省级停止
)

-- 向下查找：从省级根节点遍历整棵子树
, descendants AS (
    SELECT * FROM ancestors WHERE level = 'province'     -- 从根开始
    UNION ALL
    SELECT d.* FROM district d
    JOIN descendants desc ON d.parent_ad_code = desc.ad_code  -- 向下递归
)
SELECT * FROM descendants WHERE level != 'country';       -- 排除国家级
```

设计要点：
- **先上后下**：先通过 `parent_ad_code` 向上回溯到省级根节点，再从根向下遍历整棵子树
- **深度限制**：递归深度硬限制 20 层，防止循环数据导致资源耗尽
- **排除国家级**：`level != 'country'` 过滤掉顶层国家记录

### 2. 扁平→树形转换

数据库返回的是扁平记录列表，service 层负责构建树：

```
[                                                              [
  {adCode:"110000", parent:"100000", name:"北京市"},             {adCode:"110000", name:"北京市",
  {adCode:"110100", parent:"110000", name:"北京市"},    →          children: [
  {adCode:"110101", parent:"110100", name:"东城区"},              {adCode:"110100", name:"北京市",
]                                                                   children: [
                                                                      {adCode:"110101", name:"东城区"}
                                                                    ]
                                                                  }
                                                                ]
                                                              }
]
```

构建算法：
1. 第一遍遍历建立 `map[adCode]*node` 索引
2. 第二遍遍历挂载 children（通过 parent_ad_code 查找父节点）
3. 根节点检测：`parent_ad_code = "100000"` 或父节点不在结果集中
4. children 按 adCode 排序保证稳定输出

### 3. 参数校验

| 校验 | 规则 |
|------|------|
| adCode 格式 | `^\d{6}$`（6位纯数字） |
| 空 adCode | 查询全部（排除国家级），上限 50000 条 |
| adCode 不存在 | 返回空列表，不报错 |

### 4. 依赖注入（Wire）

```
NewData (MySQL)
    ↓
NewDistrictRepo (实现 biz.DistrictRepo 接口)
    ↓
NewDistrictUseCase (biz.DistrictRepo)
    ↓
NewDistrictService (biz.DistrictUseCase)
    ↓
NewGRPCServer (DistrictService)
    ↓
NewApp (GRPCServer)
```

---

## 目录结构

```
district/
├── api/district/v1/
│   ├── district.proto           # gRPC 接口定义（源文件）
│   ├── district.pb.go           # protobuf 消息结构（protoc 生成）
│   └── district_grpc.pb.go      # gRPC 服务端/客户端接口（protoc 生成）
│
├── cmd/district/
│   ├── main.go                  # 入口：配置 → logger → tracer → etcd → wireApp → 启动
│   ├── wire.go                  # Wire 依赖注入声明
│   └── wire_gen.go              # Wire 生成的注入代码（勿手动修改）
│
├── internal/
│   ├── biz/
│   │   └── district.go          # 领域模型 District + DistrictRepo 接口 + DistrictUseCase
│   ├── service/
│   │   └── district.go          # gRPC 方法实现，扁平→树形转换，adCode 校验
│   ├── data/
│   │   ├── data.go              # MySQL 连接池初始化（使用 pkg/dbutil 共享模块）
│   │   └── district.go          # DistrictRepo 实现（递归 CTE + 深度限制）
│   ├── server/
│   │   └── grpc.go              # gRPC Server + 中间件链
│   ├── conf/
│   │   ├── conf.proto           # 配置结构定义（无 JWT、无 Redis）
│   │   └── conf.pb.go           # 配置结构（protoc 生成）
│   └── pkg/
│       └── log/
│           └── zap.go           # Kratos log.Logger → zap 桥接
│
├── configs/
│   └── config.yaml              # 服务配置
├── Dockerfile                   # 多阶段构建
├── third_party/google/          # proto HTTP 注解依赖
├── go.mod
└── go.sum
```

与 user 服务对比，district 没有 `redis/`、`jwt/`、`snowflake/` pkg，因为职责单一。

---

## 配置管理

### 配置加载流程

与 user/gateway 一致：`envexpand` 单次正则展开 `${VAR:default}` → 加载 `config.yaml`。

### 配置项

| 配置块 | 说明 |
|--------|------|
| `server.grpc` | gRPC 监听地址（`:9003`）和超时 |
| `data.database` | MySQL 连接（driver、DSN、连接池参数） |
| `registry.etcd` | etcd 服务注册（endpoints、timeout） |
| `logger` | 日志级别 |
| `tracing` | OTLP 追踪端点和采样率 |

注意：district 配置中没有 `jwt` 和 `redis` 块，因为不需要认证和会话管理。

---

## 启动方式

### 本地开发

```bash
cd district
go run ./cmd/district/ -conf configs/config.yaml
```

启动流程：

1. 通过 `envexpand` 加载配置
3. 初始化 zap logger
4. 初始化 OTLP tracer
5. 连接 etcd（带重试）→ 注册服务 `storemicro-district`
6. Wire 注入 → 启动 gRPC Server
7. 启动健康检查 HTTP Server（`:9103`）

### 编译运行

```bash
cd district
go build -o bin/district ./cmd/district/
./bin/district -conf configs/config.yaml
```

### Docker

```bash
docker build -t storemicro-district .
docker run -p 9003:9003 -p 9103:9103 \
  -e DB_PASSWORD=xxx \
  storemicro-district
```

---

## 健康检查与可观测性

健康检查服务监听 `:9103`（HTTP）：

| 端点 | 说明 |
|------|------|
| `GET /healthz` | 存活探针 |
| `GET /readyz` | 就绪探针，检查 MySQL 连通性（无 Redis 检查） |
| `GET /metrics` | Prometheus 指标 |

追踪：OTLP gRPC → Jaeger，服务名 `storemicro-district`。

---

## 常用命令

```bash
# === 运行 ===
go run ./cmd/district/ -conf configs/config.yaml

# === 编译 ===
go build -o bin/district ./cmd/district/

# === Proto 代码生成（district 服务，不需要 grpc-gateway） ===
cd district
protoc \
  --proto_path=. \
  --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  api/district/v1/district.proto

# === Wire 依赖注入生成 ===
cd cmd/district && wire

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

### 1. 修改 proto

编辑 `api/district/v1/district.proto`。

### 2. 生成代码

```bash
cd district
protoc \
  --proto_path=. \
  --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  api/district/v1/district.proto
```

### 3. 实现各层

- `internal/biz/district.go`：接口声明 + 业务逻辑
- `internal/data/district.go`：repo 实现（递归 CTE / 普通查询）
- `internal/service/district.go`：gRPC 方法 + proto↔biz 转换

### 4. 同步到 gateway

```bash
cp api/district/v1/district.proto ../../gateway/api/district/v1/
cp api/district/v1/district.pb.go ../../gateway/api/district/v1/
cp api/district/v1/district_grpc.pb.go ../../gateway/api/district/v1/

cd ../../gateway
protoc \
  --proto_path=. \
  --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=. --grpc-gateway_opt=paths=source_relative \
  api/district/v1/district.proto
```

### 5. 重新生成 Wire（修改了接口才需要）

```bash
cd district/cmd/district && wire
```
