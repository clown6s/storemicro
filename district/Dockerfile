# Build stage
FROM golang:1.23-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app

COPY go.mod go.sum ./
COPY configs/ ./configs/
COPY pkg/ /pkg/
COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/district ./cmd/district

# Runtime stage
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata
ENV TZ=Asia/Shanghai

WORKDIR /app
COPY --from=builder /bin/district .
COPY --from=builder configs/ ./configs/

EXPOSE 9003 9103

ENTRYPOINT ["/app/district"]
CMD ["-conf", "configs"]
