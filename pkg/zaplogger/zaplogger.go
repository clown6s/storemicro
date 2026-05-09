package zaplogger

import (
	"os"

	"github.com/go-kratos/kratos/v2/log"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var zapLog *zap.Logger

// NewZapLogger 创建 zap logger
// 返回 Kratos log.Logger 给 kratos 中间件（logging.Server）桥接使用
// 同时将 *zap.Logger 存入全局，业务代码通过 zap.L() 获取
func NewZapLogger(level string) log.Logger {
	lv := parseLevel(level)

	enc := zapcore.NewConsoleEncoder(zap.NewDevelopmentEncoderConfig())
	core := zapcore.NewCore(enc, zapcore.AddSync(os.Stdout), lv)

	// 业务代码用：通过 zap.L() 获取，caller 正确指向业务代码
	zapLog = zap.New(core, zap.AddCaller())
	_ = zap.ReplaceGlobals(zapLog)

	// Kratos 桥接用：Skip 2 层（log.Helper -> ZapLogger.Log -> kratos 调用方）
	kratosLogger := zap.New(core, zap.AddCaller(), zap.AddCallerSkip(2))
	return &ZapLogger{logger: kratosLogger}
}

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
