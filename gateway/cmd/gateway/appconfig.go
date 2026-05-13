package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

// AppConfig 应用配置（从 config.yaml 读取）
type AppConfig struct {
	Name       string           `yaml:"name"`
	Version    string           `yaml:"version"`
	Server     ServerConfig     `yaml:"server"`
	GrpcClient GrpcClientConfig `yaml:"grpc_client"`
	Etcd       EtcdConfig       `yaml:"etcd"`
	Jwt        JwtConfig        `yaml:"jwt"`
	Redis      RedisConfig      `yaml:"redis"`
	Log        LogConfig        `yaml:"log"`
	Tracing    TracingConfig    `yaml:"tracing"`
}

// GrpcClientConfig gRPC 出站客户端配置
type GrpcClientConfig struct {
	LoadBalancingPolicy string               `yaml:"load_balancing_policy"` // round_robin | pick_first
	HealthCheck         HealthCheckConfig    `yaml:"health_check"`
	Retry               RetryConfig          `yaml:"retry"`
	CircuitBreaker      CircuitBreakerConfig `yaml:"circuit_breaker"`
	MaxRecvMsgSize      int                  `yaml:"max_recv_msg_size"` // MB
	MaxSendMsgSize      int                  `yaml:"max_send_msg_size"` // MB
}

// HealthCheckConfig gRPC 客户端健康检查配置
type HealthCheckConfig struct {
	Enabled  bool          `yaml:"enabled"`
	Interval time.Duration `yaml:"interval"` // 检查间隔
	Timeout  time.Duration `yaml:"timeout"`  // 单次检查超时
}

// RetryConfig gRPC 方法重试策略配置
type RetryConfig struct {
	MaxAttempts       int           `yaml:"max_attempts"`       // 最大重试次数（含首次）
	InitialBackoff    time.Duration `yaml:"initial_backoff"`    // 首次退避
	MaxBackoff        time.Duration `yaml:"max_backoff"`        // 最大退避
	BackoffMultiplier float64       `yaml:"backoff_multiplier"` // 退避倍数
	RetryableCodes    []string      `yaml:"retryable_codes"`    // 可重试的 gRPC 状态码
}

// CircuitBreakerConfig 熔断器配置
type CircuitBreakerConfig struct {
	MaxRequests  int     `yaml:"max_requests"`  // 半开状态最大并发
	Interval     int     `yaml:"interval"`      // 统计周期（秒）
	Timeout      int     `yaml:"timeout"`       // 熔断时长（秒）
	FailureRatio float64 `yaml:"failure_ratio"` // 触发熔断的失败率阈值
	MinRequests  int     `yaml:"min_requests"`  // 最少请求数（低于此值不触发熔断）
}

// TracingConfig 链路追踪配置
type TracingConfig struct {
	Endpoint   string  `yaml:"endpoint"`
	SampleRate float64 `yaml:"sample_rate"`
}

// ServerConfig 服务配置
type ServerConfig struct {
	Addr           string   `yaml:"addr"`
	AllowOrigins   []string `yaml:"allow_origins"`
	TrustedProxies []string `yaml:"trusted_proxies"`
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
		GrpcClient: GrpcClientConfig{
			LoadBalancingPolicy: "round_robin",
			HealthCheck: HealthCheckConfig{
				Enabled:  true,
				Interval: 10 * time.Second,
				Timeout:  3 * time.Second,
			},
			Retry: RetryConfig{
				MaxAttempts:       3,
				InitialBackoff:    100 * time.Millisecond,
				MaxBackoff:        1 * time.Second,
				BackoffMultiplier: 2.0,
				RetryableCodes:    []string{"UNAVAILABLE"},
			},
			CircuitBreaker: CircuitBreakerConfig{
				MaxRequests:  5,
				Interval:     10,
				Timeout:      30,
				FailureRatio: 0.5,
				MinRequests:  10,
			},
			MaxRecvMsgSize: 4,
			MaxSendMsgSize: 4,
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

// grpcDuration 将 time.Duration 转为 gRPC service config 要求的秒级格式（如 "0.1s"、"1s"）
func grpcDuration(d time.Duration) string {
	return fmt.Sprintf("%.9gs", d.Seconds())
}

// BuildServiceConfig 将 GrpcClientConfig 转为 gRPC WithDefaultServiceConfig 所需的 JSON 字符串
func (c *GrpcClientConfig) BuildServiceConfig() string {
	type methodRetry struct {
		MaxAttempts          int      `json:"maxAttempts"`
		InitialBackoff       string   `json:"initialBackoff"`
		MaxBackoff           string   `json:"maxBackoff"`
		BackoffMultiplier    float64  `json:"backoffMultiplier"`
		RetryableStatusCodes []string `json:"retryableStatusCodes"`
	}

	type methodConfig struct {
		Name         []interface{} `json:"name"`
		RetryPolicy  *methodRetry  `json:"retryPolicy,omitempty"`
		WaitForReady *bool         `json:"waitForReady,omitempty"`
	}

	type healthCheck struct {
		ServiceName string `json:"serviceName,omitempty"`
	}

	type svcConfig struct {
		LoadBalancingPolicy string         `json:"loadBalancingPolicy"`
		HealthCheckConfig   *healthCheck   `json:"healthCheckConfig,omitempty"`
		MethodConfig        []methodConfig `json:"methodConfig,omitempty"`
	}

	sc := svcConfig{
		LoadBalancingPolicy: c.LoadBalancingPolicy,
	}

	if c.HealthCheck.Enabled {
		sc.HealthCheckConfig = &healthCheck{
			ServiceName: "", // 空表示检查整体连通性
		}
	}

	if c.Retry.MaxAttempts > 0 {
		waitForReady := true
		sc.MethodConfig = []methodConfig{
			{
				Name:         []interface{}{}, // 匹配所有方法
				WaitForReady: &waitForReady,
				RetryPolicy: &methodRetry{
					MaxAttempts:          c.Retry.MaxAttempts,
					InitialBackoff:       grpcDuration(c.Retry.InitialBackoff),
					MaxBackoff:           grpcDuration(c.Retry.MaxBackoff),
					BackoffMultiplier:    c.Retry.BackoffMultiplier,
					RetryableStatusCodes: c.Retry.RetryableCodes,
				},
			},
		}
	}

	b, _ := json.Marshal(sc)
	return string(b)
}
