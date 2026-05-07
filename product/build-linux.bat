@echo off
setlocal

set CGO_ENABLED=0
set GOOS=linux
set GOARCH=amd64

go build -trimpath -ldflags "-s -w" -o "%~dp0product" ./cmd/product/

if %ERRORLEVEL% neq 0 (
    echo  [ERROR] Build failed!
    exit /b 1
)

echo  [OK] product
