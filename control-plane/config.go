package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config 控制平面全局配置
type Config struct {
	Listener   ListenerConfig   `yaml:"listener"`
	Transcoder TranscoderConfig `yaml:"transcoder"`
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

	return cfg, nil
}
