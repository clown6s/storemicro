# Build stage
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git

# Go module proxy（构建时可通过 --build-arg GOPROXY=... 覆盖）
ARG GOPROXY=https://goproxy.cn,https://goproxy.io,https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
ENV GOSUMDB=sum.golang.org
ENV GONOSUMDB=off

WORKDIR /app

# Copy go.mod files for dependency caching
COPY district/go.mod district/go.sum ./
COPY pkg/ ./pkg/

# Generate go.work for Docker build (host go.work is excluded by .dockerignore)
RUN printf 'go 1.26.1\nuse (\n  .\n  ./pkg/cmdutil\n  ./pkg/dbutil\n  ./pkg/envexpand\n  ./pkg/health\n  ./pkg/retry\n  ./pkg/tracing\n  ./pkg/zaplogger\n)\n' > go.work

# Download dependencies — cache mounted so subsequent builds reuse downloaded modules
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

# Copy service source
COPY district/ .

# Production build: strip debug info + remove file paths
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux \
    go build -trimpath \
    -ldflags "-s -w -buildid=" \
    -o /bin/district ./cmd/district

# Runtime stage — minimal attack surface
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 1000 -s /sbin/nologin appuser && \
    mkdir -p /app/configs && \
    chown appuser:appuser /app/configs && \
    chmod 700 /app/configs

ENV TZ=Asia/Shanghai

WORKDIR /app
COPY --from=builder /bin/district .
COPY --from=builder /app/configs/ ./configs/

USER appuser

EXPOSE 6002 9103

HEALTHCHECK --interval=10s --timeout=5s --retries=3 --start-period=30s \
    CMD ["wget", "-qO-", "http://localhost:9103/readyz"]

ENTRYPOINT ["/app/district"]
CMD ["-conf", "configs/config.yaml"]
