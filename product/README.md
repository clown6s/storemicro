# product — 商品服务

基于 Kratos 框架的 gRPC 商品服务，提供商品的完整 CRUD 能力，通过 etcd 注册服务实例，供 gateway 发现调用。

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

服务名：`product`，gRPC 监听 `:9002`

### GetProduct — 查询商品

**gRPC 方法**：`Product/GetProduct`  
**HTTP 路由（gateway 转发后）**：`GET /api/v1/product/{id}`

请求参数：路径中的 `{id}` 为商品 ID（字符串形式，服务内部转为 int64）

响应体：

```json
{
  "id": 1,
  "name": "iPhone 15",
  "description": "最新款苹果手机",
  "price": 599900,
  "stock": 50
}
```

> `price` 字段单位为**分**，例如 `599900` 表示 5999.00 元。

**错误码对照**：

| 场景 | gRPC 状态码 | HTTP 状态码 |
|------|-------------|-------------|
| ID 格式错误 | `INVALID_ARGUMENT` | 400 |
| 商品不存在 | `NOT_FOUND` | 404 |

---

### ListProducts — 商品列表

**gRPC 方法**：`Product/ListProducts`  
**HTTP 路由（gateway 转发后）**：`GET /api/v1/product/list`

请求参数（Query String）：

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `page` | int32 | 1 | 页码，从 1 开始 |
| `page_size` | int32 | 10 | 每页数量，最大 100 |

```bash
curl "http://localhost:8080/api/v1/product/list?page=1&page_size=20"
```

响应体：

```json
{
  "items": [
    {"id":1,"name":"iPhone 15","description":"最新款苹果手机","price":599900,"stock":50},
    {"id":2,"name":"AirPods Pro","description":"降噪耳机","price":179900,"stock":200}
  ],
  "total": 2
}
```

**分页规则**：
- `page <= 0` 自动修正为 `1`
- `page_size <= 0 || page_size > 100` 自动修正为 `10`

---

### CreateProduct — 创建商品

> 该方法当前仅通过 gRPC 直接调用，若需通过 HTTP 暴露，请参考[扩展：添加新接口](#扩展添加新接口完整步骤)在 proto 中添加 HTTP 注解。

**gRPC 方法**：暂无（需通过扩展添加）

**业务规则**：
- `name` 不能为空
- `price` 必须 >= 0
- `stock` 必须 >= 0

---

### UpdateProduct — 更新商品

**SQL 策略**：使用 `COALESCE(NULLIF(?, ''), field)` 实现字段可选更新，传空值则保留原值。

---

### DeleteProduct — 删除商品

物理删除，执行 `DELETE FROM products WHERE id = ?`。

---

### Ping — 健康检查

**gRPC 方法**：`Product/Ping`  
**HTTP 路由（gateway 转发后）**：`POST /api/v1/product/ping`

```bash
curl -X POST http://localhost:8080/api/v1/product/ping
```

响应：`{"message":"pong"}`

---

## 目录结构

```
product/
├── api/product/v1/
│   ├── product.proto            # 接口定义（源文件）
│   ├── product.pb.go            # protobuf 消息结构（生成）
│   ├── product_grpc.pb.go       # gRPC 服务端/客户端接口（生成）
│   └── product.pb.gw.go         # grpc-gateway HTTP 路由（生成）
│
├── cmd/product/
│   ├── main.go                  # 程序入口：加载配置、连接 etcd、启动应用
│   ├── wire.go                  # Wire 注入声明（编译时忽略）
│   └── wire_gen.go              # Wire 生成的依赖注入代码（勿手动修改）
│
├── internal/
│   ├── biz/
│   │   └── product.go           # 领域模型 Product、ProductRepo 接口、ProductUsecase
│   ├── service/
│   │   └── product.go           # gRPC 服务实现，调用 ProductUsecase
│   ├── data/
│   │   ├── data.go              # MySQL 连接池初始化
│   │   └── product.go           # ProductRepo 的 MySQL 实现
│   ├── server/
│   │   └── grpc.go              # Kratos gRPC 服务器，注册 ProductServer
│   ├── conf/
│   │   ├── conf.proto           # 配置结构定义
│   │   └── conf.pb.go           # 配置结构（生成）
│   └── pkg/log/
│       └── zap.go               # zap logger 封装
│
├── configs/
│   └── config.yaml              # 服务配置文件
├── third_party/google/api/      # proto HTTP 注解依赖
└── go.mod
```

### 各层职责

```
service 层（service/product.go）
    ↓  参数转换（string ID → int64）、错误码映射
biz 层（biz/product.go）
    ↓  业务规则（name/price/stock 校验）、Usecase 方法
data 层（data/product.go）
    ↓  MySQL CRUD（Get/List/Create/Update/Delete）
数据库（products 表）
```

---

## 配置文件

`configs/config.yaml`

```yaml
server:
  grpc:
    addr: 0.0.0.0:9002   # gRPC 监听地址
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
      - 10.211.55.5:2379
    timeout: 10           # 连接超时（秒）
```

---

## 启动方式

### 开发模式

```bash
cd product
go run ./cmd/product/... -conf configs/config.yaml
```

### 编译后运行

```bash
cd product
go build -o bin/product ./cmd/product/...
./bin/product -conf configs/config.yaml
```

---

## 数据库表结构

```sql
CREATE TABLE products (
    id          BIGINT       AUTO_INCREMENT PRIMARY KEY,
    name        VARCHAR(128) NOT NULL,
    description TEXT,
    price       BIGINT       NOT NULL DEFAULT 0,  -- 单位：分
    stock       INT          NOT NULL DEFAULT 0,
    created_at  DATETIME     DEFAULT CURRENT_TIMESTAMP
);
```

---

## 扩展：添加新接口（完整步骤）

以添加 **CreateProduct（创建商品，HTTP 暴露）** 接口为例，演示完整扩展流程。

### 第一步：修改 proto 文件

编辑 `api/product/v1/product.proto`，补充创建接口的 HTTP 注解：

```protobuf
service Product {
  // ... 已有方法 ...

  rpc CreateProduct (CreateProductReq) returns (CreateProductResp) {
    option (google.api.http) = {
      post: "/api/v1/product"
      body: "*"
    };
  }
}

message CreateProductReq {
  string name        = 1;
  string description = 2;
  int64  price       = 3;  // 单位：分
  int32  stock       = 4;
}

message CreateProductResp {
  int64  id      = 1;
  string message = 2;
}
```

### 第二步：重新生成 gRPC 代码

```bash
# 在 product 目录下执行
protoc \
  --proto_path=. \
  --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=. --grpc-gateway_opt=paths=source_relative \
  api/product/v1/district.proto
```

### 第三步：biz 层已有 Create 方法，确认签名

`internal/biz/product.go` 中已有：

```go
type ProductRepo interface {
    Create(ctx context.Context, p *Product) (int64, error)
    // ...
}

func (uc *ProductUsecase) Create(ctx context.Context, p *Product) (*Product, error) { ... }
```

若需要新增业务规则，在此文件中修改即可。

### 第四步：在 service 层实现 gRPC 方法

编辑 `internal/service/product.go`：

```go
func (s *ProductService) CreateProduct(ctx context.Context, req *productv1.CreateProductReq) (*productv1.CreateProductResp, error) {
    zap.L().Info("CreateProduct called", zap.String("name", req.Name))

    p := &biz.Product{
        Name:        req.Name,
        Description: req.Description,
        Price:       req.Price,
        Stock:       req.Stock,
    }

    created, err := s.uc.Create(ctx, p)
    if err != nil {
        return nil, s.mapError(err)
    }
    return &productv1.CreateProductResp{
        Id:      created.ID,
        Message: "created",
    }, nil
}
```

### 第五步：将新接口同步到 gateway

```bash
# 将更新后的 proto 生成文件复制到 gateway
cp product/api/product/v1/*.go gateway/api/product/v1/
```

然后重启 gateway。

### 第六步：验证

```bash
curl -X POST http://localhost:8080/api/v1/product \
  -H "Content-Type: application/json" \
  -d '{"name":"MacBook Pro","description":"苹果笔记本","price":1499900,"stock":30}'
```

期望响应：

```json
{"id": 3, "message": "created"}
```

---

## 扩展示例：添加搜索接口

如果需要按名称搜索商品：

**proto 定义**：

```protobuf
rpc SearchProducts (SearchProductsReq) returns (ListProductsResp) {
  option (google.api.http) = {
    get: "/api/v1/product/search"
  };
}

message SearchProductsReq {
  string keyword  = 1;
  int32  page     = 2;
  int32  page_size = 3;
}
```

**biz 层扩展 ProductRepo 接口**：

```go
type ProductRepo interface {
    // ... 已有方法 ...
    Search(ctx context.Context, keyword string, page, pageSize int32) ([]*Product, int32, error)
}
```

**data 层实现**：

```go
func (r *productRepo) Search(ctx context.Context, keyword string, page, pageSize int32) ([]*biz.Product, int32, error) {
    like := "%" + keyword + "%"
    var total int32
    _ = r.data.db.QueryRowContext(ctx,
        "SELECT COUNT(*) FROM products WHERE name LIKE ?", like,
    ).Scan(&total)

    offset := (page - 1) * pageSize
    rows, err := r.data.db.QueryContext(ctx,
        "SELECT id, name, description, price, stock FROM products WHERE name LIKE ? LIMIT ? OFFSET ?",
        like, pageSize, offset,
    )
    // ... 处理 rows ...
}
```

---

## 扩展：添加 gRPC 中间件

编辑 `internal/server/grpc.go`：

```go
func NewGRPCServer(c *conf.Server, logger log.Logger, svc *service.ProductService) *grpc.Server {
    opts := []grpc.ServerOption{
        grpc.Address(c.Grpc.Addr),
        grpc.Middleware(
            recovery.Recovery(),       // panic 恢复（已有）
            logging.Server(logger),    // 请求日志（已有）
            ratelimit.Server(),        // 限流（新增）
        ),
    }
    srv := grpc.NewServer(opts...)
    productv1.RegisterProductServer(srv, svc)
    return srv
}
```

---

## 常用命令

```bash
# 运行
go run ./cmd/product/... -conf configs/config.yaml

# 编译
go build -o bin/product ./cmd/product/...

# 重新生成 proto
protoc \
  --proto_path=. \
  --proto_path=./third_party \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=. --grpc-gateway_opt=paths=source_relative \
  api/product/v1/district.proto

# 重新生成配置结构
protoc \
  --proto_path=. \
  --go_out=. --go_opt=paths=source_relative \
  internal/conf/conf.proto

# 重新生成 Wire 依赖注入代码
cd cmd/product && wire

# 整理依赖
go mod tidy

# 运行测试
go test ./...
go test -v -cover ./internal/biz/...
```
