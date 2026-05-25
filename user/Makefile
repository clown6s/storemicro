.PHONY: build test generate wire lint clean

# 构建
build:
	go build -o bin/user.exe ./cmd/user/

# 运行测试（启用 race detector）
test:
	go test -race -count=1 ./...

# protobuf 代码生成
generate:
	protoc --proto_path=../../third_party --proto_path=api/user/v1 \
		--go_out=api/user/v1 --go_opt=paths=source_relative \
		--go-grpc_out=api/user/v1 --go-grpc_opt=paths=source_relative \
		api/user/v1/user.proto

# Wire 依赖注入代码生成
wire:
	cd cmd/user && wire

# 静态检查（golangci-lint 优先，回退 go vet）
lint:
	@if command -v golangci-lint > /dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not found, falling back to go vet"; \
		go vet ./...; \
	fi

# 清理构建产物
clean:
	rm -rf bin/
