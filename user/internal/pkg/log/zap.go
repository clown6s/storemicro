package log

import (
	"github.com/go-kratos/kratos/v2/log"
	pkglog "github.com/storemicro/pkg/zaplogger"
)

// Config 日志配置（re-export from pkg/zaplogger）
type Config = pkglog.Config

// NewZapLogger 委托到共享的 pkg/zaplogger，消除跨服务代码重复
// 支持完整的日志配置（level, mode, file rotation）
func NewZapLogger(cfg Config) log.Logger {
	return pkglog.New(cfg)
}

// NewNopLogger 返回静默 logger，用于 config.Load() 前抑制 Kratos 框架内部日志
func NewNopLogger() log.Logger {
	return pkglog.NewNopLogger()
}
