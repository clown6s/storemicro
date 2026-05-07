# district — 行政区划服务

基于 Kratos v2 框架的 gRPC 服务，提供中国行政区划（省/市/区）树形数据查询。

## 目录
- [接口说明](#接口说明)
- [目录结构](#目录结构)
- [生成步骤](#生成步骤)
- [配置文件](#配置文件)
- [启动方式](#启动方式)
- [接入 gateway](#接入-gateway)

---

## 接口说明

服务名：`storemicro-district`，gRPC 监听端口见配置文件。

### GetProvinceTree — 获取行政区划树

**gRPC 方法**：`District/GetProvinceTree`  
**HTTP 路由（gateway 转发后）**：`GET /api/v1/district/provinceTree`

请求体：无

响应体：

```json
{
  "data": [
    {
      "id": 1,
      "parentAdCode": "",
      "adCode": "110000",
      "name": "北京市",
      "level": "province",
      "center": "116.407526,39.904030",
      "children": [
        {
          "id": 2,
          "parentAdCode": "110000",
          "adCode": "110100",
          "name": "北京市",
          "level": "city",
          "center": "116.407526,39.904030",
          "children": [{"id": 3, "adCode": "110101", "name": "东城区", ...}]
        }
      ]
    }
  ]
}
```

---

## 目录结构

```
district/
├── api/district/v1/
│   ├── district.proto         # 接口定义（源文件，手动修改）
│   ├── district.pb.go         # protobuf 消息结构（自动生成，勿手动修改）
│   └── district_grpc.pb.go   # gRPC 服务端/客户端接口（自动生成，勿手动修改）
│
├── cmd/district/
│   ├── main.go               # 程序入口
│   ├── wire.go               # Wire 注入声明
│   └── wire_gen.go           # Wire 生成的依赖注入代码（自动生成）
│
├── internal/
│   ├── biz/
│   │   └── district.go       # 业务逻辑
│   ├── service/
│   │   └── district.go       # gRPC 服务实现
│   ├── data/
│   │   └── district.go       # 数据层（MySQL 查询）
│   └── server/
│       └── grpc.go           # gRPC 服务器
│
├── configs/
│   └── config.yaml            # 配置文件
├── third_party/
│   └── google/               # proto 依赖（含 api/annotations.proto）
└── go.mod
```

---

## 生成步骤

### ⚠️ 重要：每个服务需要生成的文件不同

| 服务类型 | 需要生成的文件 | 生成命令 |
|---------|--------------|---------|
| **district / user / product**（后端 gRPC 服务） | `*.pb.go` + `*_grpc.pb.go` | 不需要 `--grpc-gateway_out` |
| **gateway**（HTTP 网关） | `*.pb.go` + `*_grpc.pb.go` + `*.pb.gw.go` | **必须**加 `--grpc-gateway_out` |

---

### 第一步：生成 district 服务自身的代码（不需要 .pb.gw.go）

在 `district/` 目录下执行：

```bash
cd district

protoc \
  --proto_path=. \
  --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  api/district/v1/district.proto
```

生成后，`api/district/v1/` 下应有：
- ✅ `district.pb.go`
- ✅ `district_grpc.pb.go`
- ❌ **不需要** `district.pb.gw.go`（这个文件只在 gateway 里生成）

---

### 第二步：复制 proto 源文件到 gateway

```bash
# 创建目录（如果不存在）
mkdir -p gateway/api/district/v1

# 复制 proto 源文件
cp district/api/district/v1/district.proto gateway/api/district/v1/district.proto
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
  api/district/v1/district.proto
```

生成后，`gateway/api/district/v1/` 下应有：
- ✅ `district.pb.go`
- ✅ `district_grpc.pb.go`
- ✅ `district.pb.gw.go` ← 这个只在 gateway 里生成

---

### 第四步：重新生成 Wire 依赖注入（如果修改了接口）

```bash
cd district/cmd/district
wire
```

---

## 配置文件

`configs/config.yaml`

```yaml
server:
  grpc:
    addr: 0.0.0.0:9003   # gRPC 监听地址（district 服务端口）
    timeout: 10s

data:
  database:
    driver: mysql
    source: root:password@tcp(127.0.0.1:3306)/store_micro?charset=utf8mb4&parseTime=true&loc=Local

logger:
  level: info

registry:
  etcd:
    endpoints:
      - 10.211.55.5:2379
    timeout: 10

jaeger:
  endpoint: 10.211.55.5:4317
```

---

## 启动方式

### 开发模式

```bash
cd district
go run ./cmd/district/ -conf configs/config.yaml
```

### 编译后运行

```bash
cd district
go build -o bin/district ./cmd/district/
./bin/district -conf configs/config.yaml
```

---

## 接入 gateway

### 第一步：修改 `gateway/cmd/gateway/registry.go`

在 `services` 切片中添加 district 服务：

```go
var services = []ServiceRegistration{
    {
        Name:            "user",
        RegisterHandler: userv1.RegisterUserHandler,
    },
    {
        Name:            "product",
        RegisterHandler: productv1.RegisterProductHandler,
    },
    // 新增 district 服务：
    {
        Name:            "district",
        RegisterHandler: districtv1.RegisterDistrictHandler,
    },
}
```

### 第二步：重启 gateway

```bash
cd gateway
go run ./cmd/gateway/ -conf configs/config.yaml
```

---

## 常用命令

```bash
# 运行
go run ./cmd/district/ -conf configs/config.yaml

# 编译
go build -o bin/district ./cmd/district/

# 重新生成 proto（district 服务，不需要 grpc-gateway）
protoc \
  --proto_path=. \
  --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  api/district/v1/district.proto

# 重新生成 Wire 依赖注入
cd cmd/district && wire

# 整理依赖
go mod tidy
```
