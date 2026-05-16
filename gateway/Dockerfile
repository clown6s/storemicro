# Build stage
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app

# Copy go.mod first for dependency caching
COPY go.mod go.sum ./
RUN go mod download

# Copy shared packages and service source
COPY pkg/ /pkg/
COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/gateway ./cmd/gateway

# Runtime stage
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata
RUN adduser -D -u 1000 appuser
ENV TZ=Asia/Shanghai

WORKDIR /app
COPY --from=builder /bin/gateway .
COPY --from=builder configs/ ./configs/

USER appuser

EXPOSE 8080

ENTRYPOINT ["/app/gateway"]
CMD ["-conf", "configs"]
