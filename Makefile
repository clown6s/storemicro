.PHONY: build test generate sync-proto wire docker-up docker-down lint clean

# 构建所有服务
build:
	go build -o bin/user.exe ./user/cmd/user/
	go build -o bin/district.exe ./district/cmd/district/
	go build -o bin/gateway.exe ./gateway/cmd/gateway/

# 运行测试（启用 race detector）
test:
	go test -race -count=1 ./user/... ./district/... ./gateway/... ./pkg/...

# 同步 proto 文件：从服务源目录复制到 gateway（proto 的单一源头在 user/ 和 district/）
sync-proto:
	cp user/api/user/v1/user.proto gateway/api/user/v1/user.proto
	cp district/api/district/v1/district.proto gateway/api/district/v1/district.proto

# protobuf 代码生成（含 grpc-gateway HTTP 反向代理代码）
generate: sync-proto
	protoc --proto_path=third_party --proto_path=user/api/user/v1 \
		--go_out=user/api/user/v1 --go_opt=paths=source_relative \
		--go-grpc_out=user/api/user/v1 --go-grpc_opt=paths=source_relative \
		user/api/user/v1/user.proto
	protoc --proto_path=third_party --proto_path=district/api/district/v1 \
		--go_out=district/api/district/v1 --go_opt=paths=source_relative \
		--go-grpc_out=district/api/district/v1 --go-grpc_opt=paths=source_relative \
		district/api/district/v1/district.proto
	protoc --proto_path=third_party --proto_path=gateway/api/user/v1 \
		--go_out=gateway/api/user/v1 --go_opt=paths=source_relative \
		--go-grpc_out=gateway/api/user/v1 --go-grpc_opt=paths=source_relative \
		--grpc-gateway_out=gateway/api/user/v1 --grpc-gateway_opt=paths=source_relative \
		gateway/api/user/v1/user.proto
	protoc --proto_path=third_party --proto_path=gateway/api/district/v1 \
		--go_out=gateway/api/district/v1 --go_opt=paths=source_relative \
		--go-grpc_out=gateway/api/district/v1 --go-grpc_opt=paths=source_relative \
		--grpc-gateway_out=gateway/api/district/v1 --grpc-gateway_opt=paths=source_relative \
		gateway/api/district/v1/district.proto

# Wire 依赖注入代码生成
wire:
	cd user/cmd/user && wire
	cd district/cmd/district && wire

# Docker Compose
docker-up:
	docker compose up -d

docker-down:
	docker compose down

# 静态检查（golangci-lint 优先，回退 go vet）
lint:
	@if command -v golangci-lint > /dev/null 2>&1; then \
		golangci-lint run ./user/... ./district/... ./gateway/... ./pkg/...; \
	else \
		echo "golangci-lint not found, falling back to go vet"; \
		go vet ./user/... ./district/... ./gateway/... ./pkg/...; \
	fi

# 清理构建产物
clean:
	rm -rf bin/
