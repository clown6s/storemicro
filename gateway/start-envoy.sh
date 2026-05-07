#!/bin/bash
# 启动 Envoy 网关
# 用法: bash start-envoy.sh

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

echo "==> 启动 Envoy 网关 (HTTP :8080 → gRPC 后端)"
echo "==> 管理接口: http://localhost:9901"
echo "==> 配置文件: $SCRIPT_DIR/envoy.yaml"
echo ""

envoy -c "$SCRIPT_DIR/envoy.yaml"
