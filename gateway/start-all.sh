#!/bin/bash
# storemicro 全量启动脚本
# 启动顺序: etcd → user → product → control-plane → envoy

PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
LOG_DIR="$PROJECT_DIR/logs"
mkdir -p "$LOG_DIR"

echo "=========================================="
echo "  storemicro 全量启动"
echo "  项目目录: $PROJECT_DIR"
echo "=========================================="

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
NC='\033[0m'

check_port() {
    if command -v ss &>/dev/null; then
        ss -tlnp | grep -q ":$1 " && return 0
    elif command -v netstat &>/dev/null; then
        netstat -tlnp 2>/dev/null | grep -q ":$1 " && return 0
    fi
    return 1
}

wait_for_port() {
    local port=$1
    local name=$2
    local max_wait=${3:-30}
    local i=0
    while [ $i -lt $max_wait ]; do
        if check_port $port; then
            echo -e "${GREEN}[OK]${NC} $name 已启动 (:$port)"
            return 0
        fi
        sleep 1
        i=$((i + 1))
    done
    echo -e "${RED}[FAIL]${NC} $name 启动超时 (:$port)"
    return 1
}

# 1. 检查 etcd
echo ""
echo -e "${YELLOW}[1/5]${NC} 检查 etcd (10.211.55.5:2379)..."
if ! check_port 2379; then
    echo -e "${RED}[FAIL]${NC} etcd 未运行，请先启动 etcd"
    exit 1
fi
echo -e "${GREEN}[OK]${NC} etcd 已就绪"

# 2. 启动 user 服务
echo ""
echo -e "${YELLOW}[2/5]${NC} 启动 user 服务 (gRPC :9001)..."
cd "$PROJECT_DIR/user"
nohup go run ./cmd/user -conf configs/config.yaml > "$LOG_DIR/user.log" 2>&1 &
echo $! > "$LOG_DIR/user.pid"
wait_for_port 9001 "user 服务"

# 3. 启动 product 服务
echo ""
echo -e "${YELLOW}[3/5]${NC} 启动 product 服务 (gRPC :9002)..."
cd "$PROJECT_DIR/product"
nohup go run ./cmd/product -conf configs/config.yaml > "$LOG_DIR/product.log" 2>&1 &
echo $! > "$LOG_DIR/product.pid"
wait_for_port 9002 "product 服务"

# 4. 启动 xDS 控制面
echo ""
echo -e "${YELLOW}[4/5]${NC} 启动 xDS 控制面 (gRPC :18000)..."
cd "$PROJECT_DIR/control-plane"
nohup go run . > "$LOG_DIR/control-plane.log" 2>&1 &
echo $! > "$LOG_DIR/control-plane.pid"
wait_for_port 18000 "xDS 控制面"

# 5. 启动 Envoy
echo ""
echo -e "${YELLOW}[5/5]${NC} 启动 Envoy (HTTP :8080, Admin :9901)..."
envoy -c "$PROJECT_DIR/gateway/envoy.yaml" > "$LOG_DIR/envoy.log" 2>&1 &
echo $! > "$LOG_DIR/envoy.pid"
wait_for_port 8080 "Envoy"

echo ""
echo "=========================================="
echo -e "  ${GREEN}所有服务启动完成！${NC}"
echo "=========================================="
echo ""
echo "  服务地址:"
echo "    User 服务:     127.0.0.1:9001 (gRPC)"
echo "    Product 服务:  127.0.0.1:9002 (gRPC)"
echo "    xDS 控制面:    127.0.0.1:18000 (gRPC)"
echo "    Envoy 网关:    0.0.0.0:8080 (HTTP)"
echo "    Envoy Admin:   0.0.0.0:9901"
echo ""
echo "  测试命令:"
echo "    curl http://10.211.55.5:8080/api/v1/user/1"
echo "    curl http://10.211.55.5:8080/api/v1/product/1"
echo ""
echo "  停止所有服务:"
echo "    ./gateway/stop-all.sh"
echo ""
