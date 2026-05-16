package tracing

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	tracesdk "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.uber.org/zap"
)

// Conf 链路追踪配置
type Conf struct {
	// ServiceName 服务名
	ServiceName string
	// Endpoint OTLP gRPC 端点，如 10.211.55.5:4317
	Endpoint string
	// SampleRate 采样率（0.0-1.0），默认 0.1（10%采样）
	// 生产环境建议 0.01-0.1，开发环境可设为 1.0（全采样）
	SampleRate float64
}

// InitTracer 初始化 OTLP TracerProvider 并设为全局
// 返回 shutdown 函数，调用方应在退出时调用以刷新缓冲的 span
func InitTracer(c Conf) (func(context.Context) error, error) {
	if c.SampleRate <= 0 {
		c.SampleRate = 0.1
	} else if c.SampleRate > 1 {
		c.SampleRate = 1.0
	}

	// Endpoint 为空时使用 no-op TracerProvider，避免 gRPC 连接空地址阻塞
	if c.Endpoint == "" {
		zap.L().Warn("OTLP tracer endpoint is empty, using no-op tracer")
		tp := tracesdk.NewTracerProvider()
		otel.SetTracerProvider(tp)
		return tp.Shutdown, nil
	}

	// 创建 OTLP gRPC exporter
	exp, err := otlptracegrpc.New(context.Background(),
		otlptracegrpc.WithEndpoint(c.Endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP gRPC exporter: %w", err)
	}

	// 创建 TracerProvider，使用概率采样
	tp := tracesdk.NewTracerProvider(
		tracesdk.WithSampler(tracesdk.ParentBased(tracesdk.TraceIDRatioBased(c.SampleRate))),
		tracesdk.WithBatcher(exp),
		tracesdk.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceNameKey.String(c.ServiceName),
		)),
	)

	// 设置为全局 TracerProvider
	otel.SetTracerProvider(tp)

	zap.L().Info("OTLP tracer initialized",
		zap.String("service", c.ServiceName),
		zap.String("endpoint", c.Endpoint),
		zap.Float64("sample_rate", c.SampleRate),
	)

	return tp.Shutdown, nil
}
