#!/bin/bash
# 生成 Envoy grpc_json_transcoding 所需的 proto descriptor
# 用法: bash build-descriptor.sh

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

echo "==> 正在生成 proto descriptor.pb ..."

protoc \
  --proto_path="$PROJECT_DIR/user/api" \
  --proto_path="$PROJECT_DIR/product/api" \
  --proto_path="$SCRIPT_DIR/protos" \
  --proto_path="$PROJECT_DIR/user/third_party" \
  --proto_path="$PROJECT_DIR/product/third_party" \
  --include_imports \
  -o "$SCRIPT_DIR/descriptor.pb" \
  "$PROJECT_DIR/user/api/user/v1/user.proto" \
  "$PROJECT_DIR/product/api/product/v1/product.proto"

if [ $? -eq 0 ]; then
  echo "==> descriptor.pb 生成成功: $SCRIPT_DIR/descriptor.pb"
  ls -lh "$SCRIPT_DIR/descriptor.pb"
else
  echo "==> 生成失败，请检查 proto 文件"
  exit 1
fi
