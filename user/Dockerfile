# Build stage
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app

# Copy go.mod files for dependency caching
COPY user/go.mod user/go.sum ./
COPY pkg/ ./pkg/

# Generate go.work for Docker build (host go.work is excluded by .dockerignore)
RUN printf 'go 1.26.1\nuse (\n  .\n  ./pkg/cmdutil\n  ./pkg/dbutil\n  ./pkg/envexpand\n  ./pkg/health\n  ./pkg/retry\n  ./pkg/strutil\n  ./pkg/tracing\n  ./pkg/zaplogger\n)\n' > go.work

RUN go mod download

# Copy service source (configs only needed at runtime)
COPY user/ .

# Production build: strip debug info + remove file paths
RUN CGO_ENABLED=0 GOOS=linux \
    go build -trimpath \
    -ldflags "-s -w -buildid=" \
    -o /bin/user ./cmd/user

# Runtime stage — minimal attack surface
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 1000 appuser && \
    mkdir -p /app/configs && \
    chown appuser:appuser /app/configs

ENV TZ=Asia/Shanghai

WORKDIR /app
COPY --from=builder /bin/user .
COPY --from=builder /app/configs/ ./configs/

USER appuser

EXPOSE 6001 9101

HEALTHCHECK --interval=10s --timeout=5s --retries=3 --start-period=30s \
    CMD ["wget", "-qO-", "http://localhost:9101/readyz"]

ENTRYPOINT ["/app/user"]
CMD ["-conf", "configs/config.yaml"]
