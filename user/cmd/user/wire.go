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

	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/registry"
	"github.com/google/wire"
)

func wireApp(*conf.Server, *conf.Data, *conf.Jwt, *conf.Redis, log.Logger, registry.Registrar) (*kratos.App, func(), error) {
	panic(wire.Build(
		server.ProviderSet,
		data.ProviderSet,
		service.ProviderSet,
		biz.ProviderSet,
		newJWT,
		newRedis,
		newApp,
	))
}

func newJWT(c *conf.Jwt) *jwt.JWT {
	return jwt.NewJWT(c.Secret, c.ExpireHours)
}

func newRedis(c *conf.Redis) (*redis.Redis, error) {
	return redis.NewRedis(c.Addr, c.Password, int(c.Db))
}
