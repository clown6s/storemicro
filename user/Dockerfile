# Build stage
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app

# Copy go.mod files first for dependency caching
COPY go.mod go.sum ./
COPY configs/ ./configs/

# Copy shared packages (replace with your actual module path structure)
COPY pkg/ /pkg/

# Copy service source
COPY . .

# Build
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/user ./cmd/user

# Runtime stage
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata
RUN adduser -D -u 1000 appuser
ENV TZ=Asia/Shanghai

WORKDIR /app
COPY --from=builder /bin/user .
COPY --from=builder configs/ ./configs/

USER appuser

EXPOSE 9001 9101

ENTRYPOINT ["/app/user"]
CMD ["-conf", "configs"]
