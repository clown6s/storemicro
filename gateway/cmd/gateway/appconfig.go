package main

import (
	"fmt"
	"os"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

// AppConfig 应用配置（从 config.yaml 读取）
type AppConfig struct {
	Name    string        `yaml:"name"`
	Version string        `yaml:"version"`
	Server  ServerConfig  `yaml:"server"`
	Etcd    EtcdConfig    `yaml:"etcd"`
	Jwt     JwtConfig     `yaml:"jwt"`
	Redis   RedisConfig   `yaml:"redis"`
	Log     LogConfig     `yaml:"log"`
	Tracing TracingConfig `yaml:"tracing"`
}

// TracingConfig 链路追踪配置
type TracingConfig struct {
	Endpoint   string  `yaml:"endpoint"`
	SampleRate float64 `yaml:"sample_rate"`
}

// ServerConfig 服务配置
type ServerConfig struct {
	Addr         string   `yaml:"addr"`
	AllowOrigins []string `yaml:"allow_origins"`
}

// EtcdConfig etcd 配置
type EtcdConfig struct {
	Endpoints   []string      `yaml:"endpoints"`
	DialTimeout time.Duration `yaml:"dial_timeout"`
}

// LogConfig 日志配置
type LogConfig struct {
	Level string `yaml:"level"`
}

// JwtConfig JWT 配置
type JwtConfig struct {
	Secret string `yaml:"secret"`
}

// RedisConfig Redis 配置
type RedisConfig struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	Db       int    `yaml:"db"`
}

// DefaultAppConfig 返回默认配置
func DefaultAppConfig() *AppConfig {
	return &AppConfig{
		Server: ServerConfig{
			Addr: ":8080",
		},
		Etcd: EtcdConfig{
			Endpoints:   []string{"10.211.55.5:2379"},
			DialTimeout: 5 * time.Second,
		},
		Redis: RedisConfig{
			Addr: "10.211.55.5:6379",
			Db:   0,
		},
		Log: LogConfig{
			Level: "info",
		},
	}
}

// expandEnv 支持 ${VAR:default} 语法，找不到环境变量时使用默认值
var envPattern = regexp.MustCompile(`\$\{(\w+):([^}]*)\}`)

func expandEnv(s string) string {
	// 先处理 ${VAR:default} 格式
	s = envPattern.ReplaceAllStringFunc(s, func(match string) string {
		sub := envPattern.FindStringSubmatch(match)
		if val, ok := os.LookupEnv(sub[1]); ok {
			return val
		}
		return sub[2]
	})
	// 再处理标准 ${VAR} / $VAR 格式
	return os.ExpandEnv(s)
}

// LoadAppConfig 从 YAML 文件加载应用配置
func LoadAppConfig(path string) (*AppConfig, error) {
	cfg := DefaultAppConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// 展开环境变量 ${VAR:default} 语法
	expanded := expandEnv(string(data))

	if err := yaml.Unmarshal([]byte(expanded), cfg); err != nil {
		return nil, err
	}

	// 设置默认值
	if cfg.Server.Addr == "" {
		cfg.Server.Addr = ":8080"
	}
	if len(cfg.Etcd.Endpoints) == 0 {
		cfg.Etcd.Endpoints = []string{"10.211.55.5:2379"}
	}
	if cfg.Etcd.DialTimeout == 0 {
		cfg.Etcd.DialTimeout = 5 * time.Second
	}
	if cfg.Log.Level == "" {
		cfg.Log.Level = "info"
	}

	// 校验 JWT secret（必须通过环境变量注入，不允许空值）
	if len(cfg.Jwt.Secret) < 32 {
		return nil, fmt.Errorf("jwt.secret must be at least 32 characters and set via JWT_SECRET env var (current: %d)", len(cfg.Jwt.Secret))
	}

	// Redis 密码为空时记录警告（本地开发可能不设密码，生产环境应通过 REDIS_PASSWORD 注入）
	if cfg.Redis.Password == "" {
		fmt.Fprintln(os.Stderr, "[WARN] redis.password is empty, set REDIS_PASSWORD env var for production")
	}

	return cfg, nil
}
