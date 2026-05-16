package zaplogger

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"

	"github.com/go-kratos/kratos/v2/log"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// globalLog 使用 atomic.Pointer 保护并发读写（New 写入，L 读取）
var globalLog atomic.Pointer[zap.Logger]

// Config 日志配置
type Config struct {
	Level      string // debug, info, warn, error
	Mode       string // dev (console) | prod (json)，默认 dev
	Filepath   string // 日志文件路径，空 = 仅 stdout
	MaxSize    int    // lumberjack: 单文件最大 MB，默认 100
	MaxBackups int    // lumberjack: 保留旧文件数，默认 10
	MaxAge     int    // lumberjack: 保留天数，默认 30
	Compress   bool   // lumberjack: 是否 gzip 压缩旧文件
}

// New 根据 Config 创建 zap logger
// 返回 Kratos log.Logger 给 kratos 中间件桥接使用
// 同时通过 zap.ReplaceGlobals 设置全局 *zap.Logger，业务代码通过 zap.L() 或 L() 获取
func New(cfg Config) log.Logger {
	lv := parseLevel(cfg.Level)

	// 选择 encoder：prod 用 JSON，dev 用 Console（大小写不敏感）
	var enc zapcore.Encoder
	if strings.EqualFold(cfg.Mode, "prod") {
		ec := zap.NewProductionEncoderConfig()
		ec.TimeKey = "ts"
		ec.EncodeTime = zapcore.ISO8601TimeEncoder
		enc = zapcore.NewJSONEncoder(ec)
	} else {
		ec := zap.NewDevelopmentEncoderConfig()
		enc = zapcore.NewConsoleEncoder(ec)
	}

	// 构造输出目标：文件 + stdout（tee），或仅 stdout
	ws := buildWriters(cfg)

	// 多 core 合并（同一个 encoder + level 写入多个 writer）
	cores := make([]zapcore.Core, 0, len(ws))
	for _, w := range ws {
		cores = append(cores, zapcore.NewCore(enc, zapcore.AddSync(w), lv))
	}
	core := zapcore.NewTee(cores...)

	// 业务代码用：通过 zap.L() / L() 获取
	logger := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))
	globalLog.Store(logger)
	_ = zap.ReplaceGlobals(logger)

	// Kratos 桥接用：Skip 2 层（log.Helper -> ZapLogger.Log -> kratos 调用方）
	kratosLogger := zap.New(core, zap.AddCaller(), zap.AddCallerSkip(2))
	return &ZapLogger{logger: kratosLogger}
}

// L 返回全局 *zap.Logger，供业务代码直接使用（等同于 zap.L()）
// [I3] 如果 New() 尚未调用，返回 nop logger 避免空指针 panic
// [S1] 使用 atomic.Pointer 保证并发安全
func L() *zap.Logger {
	if l := globalLog.Load(); l != nil {
		return l
	}
	return zap.NewNop()
}

// buildWriters 构造日志输出目标列表
// 配置了 Filepath 时同时输出到文件和 stdout，否则仅 stdout
func buildWriters(cfg Config) []io.Writer {
	if cfg.Filepath == "" {
		return []io.Writer{os.Stdout}
	}

	maxSize := cfg.MaxSize
	if maxSize <= 0 {
		maxSize = 100
	}
	maxBackups := cfg.MaxBackups
	if maxBackups <= 0 {
		maxBackups = 10
	}
	maxAge := cfg.MaxAge
	if maxAge <= 0 {
		maxAge = 30
	}

	fileWriter := &lumberjack.Logger{
		Filename:   cfg.Filepath,
		MaxSize:    maxSize,
		MaxBackups: maxBackups,
		MaxAge:     maxAge,
		Compress:   cfg.Compress,
	}

	// 同时输出到文件和 stdout
	return []io.Writer{fileWriter, os.Stdout}
}

// nopLogger 静默日志，用于在 config.Load() 之前抑制 Kratos 框架内部日志
type nopLogger struct{}

func (nopLogger) Log(_ log.Level, _ ...any) error { return nil }

// NewNopLogger 返回一个不输出任何内容的 Kratos log.Logger
// 用于在 config.Load() 前调用 log.SetLogger() 抑制框架内部 DEBUG/INFO 日志
func NewNopLogger() log.Logger { return nopLogger{} }

// ZapLogger 实现 Kratos log.Logger 接口（给 resolver、logging middleware 等内部组件用）
type ZapLogger struct {
	logger *zap.Logger
}

func (l *ZapLogger) Log(level log.Level, keyvals ...any) error {
	var zl zapcore.Level
	var msg string
	var fields []zap.Field

	switch level {
	case log.LevelDebug:
		zl = zapcore.DebugLevel
	case log.LevelInfo:
		zl = zapcore.InfoLevel
	case log.LevelWarn:
		zl = zapcore.WarnLevel
	case log.LevelError:
		zl = zapcore.ErrorLevel
	default:
		zl = zapcore.InfoLevel
	}

	for i := 0; i < len(keyvals); i += 2 {
		if i+1 >= len(keyvals) {
			break
		}
		key, ok := keyvals[i].(string)
		if !ok {
			// 非 string key 保留为 Any 字段，避免静默丢失日志信息
			fields = append(fields, zap.Any(fmt.Sprintf("non_string_key_%d", i/2), keyvals[i]))
			continue
		}
		val := keyvals[i+1]

		switch key {
		case "msg", "message":
			if s, ok := val.(string); ok {
				msg = s
			}
		default:
			fields = append(fields, zap.Any(key, val))
		}
	}

	if msg == "" {
		msg = "log"
	}

	switch zl {
	case zapcore.DebugLevel:
		l.logger.Debug(msg, fields...)
	case zapcore.InfoLevel:
		l.logger.Info(msg, fields...)
	case zapcore.WarnLevel:
		l.logger.Warn(msg, fields...)
	case zapcore.ErrorLevel:
		l.logger.Error(msg, fields...)
	default:
		l.logger.Info(msg, fields...)
	}

	return nil
}

func (l *ZapLogger) Sync() error {
	return l.logger.Sync()
}

func parseLevel(s string) zapcore.Level {
	switch s {
	case "debug", "DEBUG":
		return zapcore.DebugLevel
	case "info", "INFO":
		return zapcore.InfoLevel
	case "warn", "WARN", "warning":
		return zapcore.WarnLevel
	case "error", "ERROR":
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}

// Validate 校验配置合法性，不合法时返回 error
func Validate(cfg Config) error {
	if cfg.Mode != "" && cfg.Mode != "dev" && cfg.Mode != "prod" {
		return fmt.Errorf("logger.mode must be 'dev' or 'prod', got: %s", cfg.Mode)
	}
	validLevels := map[string]bool{"debug": true, "info": true, "warn": true, "warning": true, "error": true}
	if cfg.Level != "" && !validLevels[cfg.Level] {
		return fmt.Errorf("logger.level must be debug/info/warn/error, got: %s", cfg.Level)
	}
	return nil
}
