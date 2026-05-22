# District — 行政区划微服务

基于 **Kratos v2.9 + gRPC + etcd** 的行政区划服务，监听 gRPC `:6002`，健康检查 `:9103`。提供中国省/市/区三级树形数据查询。通过 etcd 注册服务实例，供 gateway 发现调用。

职责单一——只做行政区划查询。无认证、无 Redis、无 JWT、无雪花 ID。

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

遵循 Kratos **Clean Architecture** 分层，与 user 服务模式一致但更轻量：

```
┌──────────────────────────────────────────┐
│  Gateway (:7000)                          │
│  JWT 认证 → grpc-gateway → gRPC 调用     │
└──────────────┬───────────────────────────┘
               │ gRPC :6002
┌──────────────▼───────────────────────────┐
│  server/grpc.go                           │
│  gRPC Server + 中间件链                   │
│  Recovery → tracing → logging             │
├──────────────────────────────────────────┤
│  service/district.go                      │
│  proto ↔ biz 转换 + 扁平→树形构建        │
├──────────────────────────────────────────┤
│  biz/district.go                          │
│  领域模型 District + DistrictRepo 接口    │
│  参数校验                                 │
├──────────────────────────────────────────┤
│  data/district.go                         │
│  DistrictRepo MySQL 实现                  │
│  递归 CTE + 深度限制                      │
├──────────────────────────────────────────┤
│  MySQL / etcd                             │
└──────────────────────────────────────────┘
```

与 user 服务的区别：

| 特性 | user | district |
|------|------|----------|
| Redis | 有（token 管理） | 无 |
| JWT | 有（生成/解析） | 无 |
| Snowflake ID | 有（etcd 租约） | 无 |
| 认证 | gateway 层 | gateway 层 |
| 职责 | 用户 CRUD + 认证 | 行政区划查询（只读） |
| gRPC 端口 | `:6001` | `:6002` |
| health 端口 | `:9101` | `:9103` |

---

## API 接口与请求流程

### RPC 方法定义

```protobuf
service District {
  rpc GetProvinceTree (GetProvinceTreeReq) returns (GetProvinceTreeResp) {
    option (google.api.http) = {
      get: "/api/v1/district/provinceTree"
    };
  }
}

message GetProvinceTreeReq {
  string ad_code = 1;  // 可选，6位行政区划代码
}

message GetProvinceTreeResp {
  int32 code = 1;
  string message = 2;
  repeated DistrictTree data = 3;
}

message DistrictTree {
  int64 id = 1;
  string parent_ad_code = 2;
  string ad_code = 3;
  string name = 4;
  string level = 5;        // province / city / district
  string center = 6;
  repeated DistrictTree children = 7;
}
```

### GetProvinceTree 请求流程

```
Gateway JWT 认证 → user_id 注入 metadata
    ↓
GET /api/v1/district/provinceTree?ad_code=110101
    ↓
grpc-gateway (JSON → Protobuf)
    ↓
gRPC → District 服务 :6002
    ↓
gRPC interceptor: Recovery → tracing → logging
    ↓
service.GetProvinceTree()
    ├─ ① 参数校验
    │   ├─ ad_code 为空 → 查询全部省市区（排除国家级）
    │   │   └─ LIMIT 50000 防数据量过大
    │   ├─ ad_code 非 6 位纯数字 → 返回参数错误
    │   └─ ad_code 合法 → 查询指定区域及其完整子树
    │
    ├─ ② biz.DistrictUseCase.GetTree()
    │   └─ data.GetByAdCode(adCode)
    │
    ├─ ③ data 层：递归 CTE 查询
    │   ```sql
    │   -- 先上：从目标节点走到省级行政区
    │   WITH RECURSIVE ancestors AS (
    │       SELECT * FROM district WHERE ad_code = '110101'
    │       UNION ALL
    │       SELECT d.* FROM district d
    │       JOIN ancestors a ON d.ad_code = a.parent_ad_code
    │       WHERE a.level != 'province'  -- 到省级停止
    │   )
    │   -- 再下：从省级根节点遍历整棵子树
    │   , descendants AS (
    │       SELECT * FROM ancestors WHERE level = 'province'
    │       UNION ALL
    │       SELECT d.* FROM district d
    │       JOIN descendants desc ON d.parent_ad_code = desc.ad_code
    │   )
    │   SELECT DISTINCT * FROM descendants
    │   WHERE level != 'country'
    │   LIMIT 10000  -- 深度限制防递归爆炸
    │   ```
    │
    ├─ ④ service 层：扁平列表 → 树形结构
    │   ├─ 第一遍遍历建立 map[adCode]*node 索引
    │   ├─ 第二遍遍历通过 parent_ad_code 挂载 children
    │   ├─ 根节点检测：parent_ad_code = "100000" 或无父节点
    │   └─ children 按 ad_code 排序保证稳定输出
    │
    └─ ⑤ 返回树形 JSON
        {
          "code": 200,
          "data": [
            {
              "adCode": "110000",
              "name": "北京市",
              "level": "province",
              "children": [
                {
                  "adCode": "110100",
                  "name": "北京市",
                  "level": "city",
                  "children": [
                    {"adCode": "110101", "name": "东城区", "level": "district"}
                  ]
                }
              ]
            }
          ]
        }
```

### gRPC 服务端中间件链

```
gRPC 请求到达 :6002
    ↓
① recovery.Recovery()（Kratos 内置 panic 恢复）
    ↓
② tracing.Server()（Kratos OTel，创建 gRPC span）
    ↓
③ logging.Server(logger)（Kratos 结构化日志）
    ↓
④ service 层方法处理
```

注意：district 没有 `rateLimitMiddleware`，因为所有请求都经过 Gateway JWT 认证。

---

## 核心设计

### 1. 递归 CTE 树形查询

- **先上后下**：先通过 `parent_ad_code` 向上回溯到省级根节点，再从根向下遍历整棵子树
- **DISTINCT**：防止重复 adcode 递归导致数据重复
- **深度限制**：`LIMIT 10000` 防循环数据导致资源耗尽
- **排除国家级**：`level != 'country'` 过滤掉顶层国家记录

### 2. 扁平→树形转换算法

```
步骤 1: 建立 map[adCode]*node 索引
步骤 2: 遍历所有节点，通过 parent_ad_code 找到父节点
步骤 3: 将当前节点 append 到父节点的 children
步骤 4: 根节点 = parent_ad_code == "100000" 或 父节点不在结果集中
步骤 5: 所有 children 按 adCode 排序
```

### 3. 环检测

`service/district.go` 中 `hasCycle` 函数：沿 parent 链遍历，如果回到起点则存在环，返回错误。

### 4. 参数校验

| 校验 | 规则 |
|------|------|
| adCode 格式 | `^\d{6}$`（6 位纯数字） |
| 空 adCode | 查询全部（排除国家级），上限 50000 条 |
| adCode 不存在 | 返回空列表，不报错 |

### 5. 依赖注入（Wire）

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
│   ├── district.pb.go           # protobuf 消息（protoc 生成）
│   └── district_grpc.pb.go      # gRPC 服务端/客户端接口（protoc 生成）
│
├── cmd/district/
│   ├── main.go                  # 入口：配置→logger→tracer→etcd→wire→启动
│   ├── wire.go                  # Wire 依赖注入声明
│   └── wire_gen.go              # Wire 生成的注入代码
│
├── internal/
│   ├── biz/
│   │   └── district.go          # 领域模型 District + DistrictRepo 接口 + UseCase
│   ├── service/
│   │   └── district.go          # gRPC 方法 + 扁平→树形转换 + 环检测
│   ├── data/
│   │   ├── data.go              # MySQL 连接池初始化
│   │   └── district.go          # 递归 CTE 查询
│   ├── server/
│   │   └── grpc.go              # gRPC Server + 中间件链
│   └── conf/
│       ├── conf.proto           # 配置结构定义（无 JWT、无 Redis）
│       └── conf.pb.go           # 配置结构（protoc 生成）
│
├── configs/config.yaml          # 服务配置
├── Dockerfile                   # 多阶段构建
├── go.mod
└── go.sum
```

---

## 配置管理

| 配置块 | 说明 |
|--------|------|
| `server.grpc` | gRPC 监听 `:6002`，timeout 10s |
| `data.database` | MySQL 连接（store_micro，maxOpen 100，maxIdle 25） |
| `registry.etcd` | etcd 注册 `storemicro-district` |
| `logger` | dev/prod 模式 |
| `tracing` | OTLP endpoint + sampleRate |

注意：district 配置中没有 `jwt` 和 `redis` 块。

---

## 启动方式

```bash
cd district
go run ./cmd/district/ -conf configs/config.yaml
```

启动流程：

1. envexpand 加载配置
2. 初始化 zap logger
3. 初始化 OTLP tracer
4. 连接 etcd → 注册服务 `storemicro-district`
5. Wire 注入 → 启动 gRPC Server (:6002)
6. 启动 health sidecar (:9103)

---

## 健康检查与可观测性

| 端点 | 端口 | 说明 |
|------|------|------|
| `GET /healthz` | `:9103` | 存活探针 |
| `GET /readyz` | `:9103` | 就绪探针（仅 MySQL 连通性，无 Redis 检查） |
| `GET /metrics` | `:9103` | Prometheus 指标 |

---

## 常用命令

```bash
# === 运行 ===
go run ./cmd/district/ -conf configs/config.yaml

# === Proto 生成（district 不需要 grpc-gateway） ===
protoc --proto_path=. --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  api/district/v1/district.proto

# === Wire ===
cd cmd/district && wire
```

---

## 扩展：添加新接口

### 1. 修改 proto

编辑 `api/district/v1/district.proto`，添加 RPC 和消息定义。

### 2. 生成代码

```bash
protoc --proto_path=. --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  api/district/v1/district.proto
```

### 3. 实现各层

- `biz/district.go`：接口声明 + 业务逻辑
- `data/district.go`：repo 实现
- `service/district.go`：gRPC 方法 + proto↔biz 转换

### 4. 同步到 gateway

```bash
cp api/district/v1/district.proto ../../gateway/api/district/v1/
cp api/district/v1/district.pb.go ../../gateway/api/district/v1/
cp api/district/v1/district_grpc.pb.go ../../gateway/api/district/v1/

cd ../../gateway
protoc --proto_path=. --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=. --grpc-gateway_opt=paths=source_relative \
  api/district/v1/district.proto
```

### 5. 重新生成 Wire（修改了接口才需要）

```bash
cd cmd/district && wire
```
