package log

import (
	pkglog "github.com/storemicro/pkg/zaplogger"

	"github.com/go-kratos/kratos/v2/log"
)

// NewZapLogger 委托到共享的 pkg/zaplogger，消除跨服务代码重复
func NewZapLogger(level string) log.Logger {
	return pkglog.NewZapLogger(level)
}
