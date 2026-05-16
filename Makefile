.PHONY: build test generate wire docker-up docker-down lint clean

# 构建所有服务
build:
	go build -o bin/user.exe ./user/cmd/user/
	go build -o bin/district.exe ./district/cmd/district/
	go build -o bin/gateway.exe ./gateway/cmd/gateway/

# 运行测试
test:
	go test ./user/... ./district/... ./gateway/... ./pkg/...

# protobuf 代码生成
generate:
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
		gateway/api/user/v1/user.proto
	protoc --proto_path=third_party --proto_path=gateway/api/district/v1 \
		--go_out=gateway/api/district/v1 --go_opt=paths=source_relative \
		--go-grpc_out=gateway/api/district/v1 --go-grpc_opt=paths=source_relative \
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

# 静态检查
lint:
	go vet ./...

# 清理构建产物
clean:
	rm -rf bin/
