package appconfig

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
	Name            string           `yaml:"name"`
	Version         string           `yaml:"version"`
	Server          ServerConfig     `yaml:"server"`
	GrpcClient      GrpcClientConfig `yaml:"grpc_client"`
	Etcd            EtcdConfig       `yaml:"etcd"`
	Jwt             JwtConfig        `yaml:"jwt"`
	Redis           RedisConfig      `yaml:"redis"`
	Log             LogConfig        `yaml:"log"`
	Tracing         TracingConfig    `yaml:"tracing"`
	MetricsPassword string           `yaml:"metrics_password"`
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
	Level      string `yaml:"level"`
	Mode       string `yaml:"mode"`        // dev (console) | prod (json)，默认 dev
	Filepath   string `yaml:"filepath"`    // 日志文件路径，空 = 仅 stdout
	MaxSize    int    `yaml:"max_size"`    // 单文件最大 MB，默认 100
	MaxBackups int    `yaml:"max_backups"` // 保留旧文件数，默认 10
	MaxAge     int    `yaml:"max_age"`     // 保留天数，默认 30
	Compress   bool   `yaml:"compress"`    // 是否 gzip 压缩旧文件
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
			DialTimeout: 5 * time.Second,
		},
		Redis: RedisConfig{
			Db: 0,
		},
		Log: LogConfig{
			Level: "info",
		},
	}
}

// ExpandEnv 支持 ${VAR:default} 和 ${VAR} / $VAR 语法
// 使用单次正则匹配处理所有格式，避免 os.ExpandEnv 双重展开导致密码含 $ 时被截断
var EnvPattern = regexp.MustCompile(`\$\{(\w+):([^}]*)\}|\$\{(\w+)\}|\$(\w+)`)

// ExpandEnv 执行环境变量展开
func ExpandEnv(s string) string {
	return EnvPattern.ReplaceAllStringFunc(s, func(match string) string {
		sub := EnvPattern.FindStringSubmatch(match)
		if sub[1] != "" {
			// ${VAR:default}
			if val, ok := os.LookupEnv(sub[1]); ok {
				return val
			}
			return sub[2]
		}
		// ${VAR} 或 $VAR
		varName := sub[3]
		if varName == "" {
			varName = sub[4]
		}
		if val, ok := os.LookupEnv(varName); ok {
			return val
		}
		return match // 环境变量不存在时保留原文
	})
}

// Load 从 YAML 文件加载应用配置
func Load(path string) (*AppConfig, error) {
	cfg := DefaultAppConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// 展开环境变量 ${VAR:default} 语法
	expanded := ExpandEnv(string(data))

	if err := yaml.Unmarshal([]byte(expanded), cfg); err != nil {
		return nil, err
	}

	// 设置默认值
	if cfg.Server.Addr == "" {
		cfg.Server.Addr = ":8080"
	}
	if len(cfg.Etcd.Endpoints) == 0 {
		return nil, fmt.Errorf("etcd.endpoints is required in config")
	}
	if cfg.Etcd.DialTimeout == 0 {
		cfg.Etcd.DialTimeout = 5 * time.Second
	}
	if cfg.Redis.Addr == "" {
		return nil, fmt.Errorf("redis.addr is required in config")
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

	b, err := json.Marshal(sc)
	if err != nil {
		return ""
	}
	return string(b)
}
