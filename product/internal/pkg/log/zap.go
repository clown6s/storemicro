package log

import (
	"os"
	"product/internal/conf"

	"github.com/go-kratos/kratos/v2/log"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// ZapLogger 实现 Kratos log.Logger 接口
type ZapLogger struct {
	logger *zap.Logger
}

// NewZapLogger 根据配置创建 ZapLogger
func NewZapLogger(c *conf.Logger) log.Logger {
	level := zap.NewAtomicLevel()
	switch c.Level {
	case "debug":
		level.SetLevel(zap.DebugLevel)
	case "info":
		level.SetLevel(zap.InfoLevel)
	case "warn":
		level.SetLevel(zap.WarnLevel)
	case "error":
		level.SetLevel(zap.ErrorLevel)
	default:
		level.SetLevel(zap.InfoLevel)
	}

	encoderConfig := zapcore.EncoderConfig{
		TimeKey:       "time",
		LevelKey:      "level",
		CallerKey:     "caller",
		MessageKey:    "msg",
		StacktraceKey: "stacktrace",
		LineEnding:    zapcore.DefaultLineEnding,
		EncodeTime:    zapcore.ISO8601TimeEncoder,
		EncodeLevel:   zapcore.LowercaseLevelEncoder,
		EncodeCaller:  zapcore.ShortCallerEncoder,
	}

	stdoutSyncer := zapcore.AddSync(os.Stdout)
	fileSyncer := zapcore.AddSync(&lumberjack.Logger{
		Filename:   c.Output,
		MaxSize:    int(c.MaxSize),
		MaxBackups: int(c.MaxBackups),
		MaxAge:     int(c.MaxAge),
		Compress:   c.Compress,
	})

	multiSyncer := zapcore.NewMultiWriteSyncer(stdoutSyncer, fileSyncer)

	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderConfig),
		multiSyncer,
		level,
	)

	zapLogger := zap.New(core, zap.AddCaller(), zap.AddCallerSkip(2))
	return &ZapLogger{logger: zapLogger}
}

// Log 实现 Kratos log.Logger 接口
func (l *ZapLogger) Log(level log.Level, keyvals ...any) error {
	var (
		zl     zapcore.Level
		msg    string
		fields []zap.Field
	)

	switch level {
	case log.LevelDebug:
		zl = zapcore.DebugLevel
	case log.LevelInfo:
		zl = zapcore.InfoLevel
	case log.LevelWarn:
		zl = zapcore.WarnLevel
	case log.LevelError:
		zl = zapcore.ErrorLevel
	case log.LevelFatal:
		zl = zapcore.FatalLevel
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
	case zapcore.FatalLevel:
		l.logger.Fatal(msg, fields...)
	default:
		l.logger.Info(msg, fields...)
	}

	return nil
}

// Sync 同步缓冲区
func (l *ZapLogger) Sync() error {
	return l.logger.Sync()
}
