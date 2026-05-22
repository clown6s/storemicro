//go:build wireinject
// +build wireinject

package main

import (
	"user/internal/biz"
	"user/internal/conf"
	"user/internal/data"
	"user/internal/pkg/jwt"
	"user/internal/pkg/redis"
	"user/internal/server"
	"user/internal/service"

	"github.com/go-kratos/kratos/v2"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/registry"
	"github.com/google/wire"
)

// wireResult Wire 注入结果（Wire 不支持多返回值，用结构体打包）
type wireResult struct {
	App     *kratos.App
	AppData *data.Data
}

func wireApp(*conf.Server, *conf.Data, *conf.Jwt, *conf.Redis, log.Logger, registry.Registrar) (wireResult, func(), error) {
	panic(wire.Build(
		server.ProviderSet,
		data.ProviderSet,
		service.ProviderSet,
		biz.ProviderSet,
		newJWT,
		newRedis,
		newApp,
		newWireResult,
	))
}

func newWireResult(app *kratos.App, appData *data.Data) wireResult {
	return wireResult{App: app, AppData: appData}
}

func newJWT(c *conf.Jwt) *jwt.JWT {
	return jwt.NewJWT(c.Secret, c.ExpireHours)
}

func newRedis(c *conf.Redis) (*redis.Redis, error) {
	return redis.NewRedis(c.Addr, c.Password, int(c.Db))
}
