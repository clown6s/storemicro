# Build stage
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app

# Copy go.mod files first for dependency caching
COPY district/go.mod district/go.sum ./
RUN go mod download

# Copy shared packages
COPY pkg/ ./pkg/

# Copy service source (configs only needed at runtime)
COPY district/ .

RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/district ./cmd/district

# Runtime stage
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata curl
RUN adduser -D -u 1000 appuser
ENV TZ=Asia/Shanghai

WORKDIR /app
COPY --from=builder /bin/district .
COPY configs/ ./configs/

USER appuser

EXPOSE 9003 9103

# Health check is defined in docker-compose.yml

ENTRYPOINT ["/app/district"]
CMD ["-conf", "configs/config.yaml"]
