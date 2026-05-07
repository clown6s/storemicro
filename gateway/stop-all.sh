#!/bin/bash
# storemicro 停止脚本

PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
LOG_DIR="$PROJECT_DIR/logs"

echo "正在停止所有服务..."

for svc in envoy control-plane product user; do
    pid_file="$LOG_DIR/$svc.pid"
    if [ -f "$pid_file" ]; then
        pid=$(cat "$pid_file")
        if kill -0 "$pid" 2>/dev/null; then
            kill "$pid" 2>/dev/null
            echo "  [OK] $svc (PID $pid) 已停止"
        else
            echo "  [--] $svc (PID $pid) 已不在运行"
        fi
        rm -f "$pid_file"
    fi
done

echo "所有服务已停止。"
