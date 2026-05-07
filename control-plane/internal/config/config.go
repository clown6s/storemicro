package config

import (
	"fmt"
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/yaml.v3"
)

// Config 控制平面全局配置
type Config struct {
	Listener   ListenerConfig   `yaml:"listener"`
	Transcoder TranscoderConfig `yaml:"transcoder"`
	Log        LogConfig        `yaml:"log"`
	Etcd       EtcdConfig       `yaml:"etcd"`
	Ads        AdsConfig        `yaml:"ads"`
	Services   []ServiceConfig  `yaml:"services"`
}

// ListenerConfig HTTP Listener 配置
type ListenerConfig struct {
	Port uint32 `yaml:"port"`
}

// TranscoderConfig gRPC-JSON Transcoder 配置
type TranscoderConfig struct {
	ProtoDescriptor string `yaml:"proto_descriptor"`
}

// LogConfig 日志配置
type LogConfig struct {
	Level   string `yaml:"level"`    // debug, info, warn, error
	File    string `yaml:"file"`     // 日志文件路径，空则仅 stdout
	MaxAge  int    `yaml:"max_age"`  // 日志保留天数
	MaxSize int    `yaml:"max_size"` // 单个日志文件最大 MB
}

// EtcdConfig etcd 连接配置
type EtcdConfig struct {
	Endpoints []string `yaml:"endpoints"`
	Prefix    string   `yaml:"prefix"`
}

// AdsConfig ADS gRPC 服务配置
type AdsConfig struct {
	Port uint32 `yaml:"port"`
}

// ServiceConfig 单个服务的路由配置
type ServiceConfig struct {
	Name        string `yaml:"name"`         // 服务名（对应 etcd 注册名）
	GrpcService string `yaml:"grpc_service"` // proto gRPC 服务全名
	RoutePrefix string `yaml:"route_prefix"` // HTTP 路由前缀
}

// LoadConfig 从 YAML 文件加载配置
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// 校验必要字段
	if len(cfg.Services) == 0 {
		return nil, fmt.Errorf("config: no services defined")
	}
	for i, svc := range cfg.Services {
		if svc.Name == "" {
			return nil, fmt.Errorf("config: service[%d] name is empty", i)
		}
		if svc.GrpcService == "" {
			return nil, fmt.Errorf("config: service[%d] (%s) grpc_service is empty", i, svc.Name)
		}
		if svc.RoutePrefix == "" {
			return nil, fmt.Errorf("config: service[%d] (%s) route_prefix is empty", i, svc.Name)
		}
	}
	if len(cfg.Etcd.Endpoints) == 0 {
		return nil, fmt.Errorf("config: etcd endpoints is empty")
	}
	if cfg.Listener.Port == 0 {
		cfg.Listener.Port = 8080
	}
	if cfg.Ads.Port == 0 {
		cfg.Ads.Port = 18000
	}
	if cfg.Log.Level == "" {
		cfg.Log.Level = "info"
	}
	if cfg.Log.MaxAge == 0 {
		cfg.Log.MaxAge = 7
	}
	if cfg.Log.MaxSize == 0 {
		cfg.Log.MaxSize = 100
	}

	return cfg, nil
}

// BuildLogger 根据配置构建 zap.Logger
func BuildLogger(cfg LogConfig) (*zap.Logger, error) {
	level, err := zapcore.ParseLevel(cfg.Level)
	if err != nil {
		return nil, fmt.Errorf("parse log level %q: %w", cfg.Level, err)
	}

	encoderCfg := zap.NewProductionEncoderConfig()
	encoderCfg.TimeKey = "ts"
	encoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	encoderCfg.EncodeLevel = zapcore.CapitalLevelEncoder
	encoderCfg.EncodeDuration = zapcore.SecondsDurationEncoder
	encoder := zapcore.NewJSONEncoder(encoderCfg)

	cores := []zapcore.Core{
		zapcore.NewCore(encoder, zapcore.AddSync(os.Stdout), level),
	}

	if cfg.File != "" {
		f, err := os.OpenFile(cfg.File, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return nil, fmt.Errorf("open log file %q: %w", cfg.File, err)
		}
		cores = append(cores, zapcore.NewCore(encoder, zapcore.AddSync(f), level))
	}

	core := zapcore.NewTee(cores...)
	return zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel)), nil
}
