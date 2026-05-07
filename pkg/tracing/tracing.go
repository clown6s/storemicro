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
}

// InitTracer 初始化 OTLP TracerProvider 并设为全局
// 返回 shutdown 函数，调用方应在退出时调用以刷新缓冲的 span
func InitTracer(c Conf) (func(context.Context) error, error) {
	// 创建 OTLP gRPC exporter
	exp, err := otlptracegrpc.New(context.Background(),
		otlptracegrpc.WithEndpoint(c.Endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP gRPC exporter: %w", err)
	}

	// 创建 TracerProvider
	tp := tracesdk.NewTracerProvider(
		tracesdk.WithSampler(tracesdk.AlwaysSample()),
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
	)

	return tp.Shutdown, nil
}
