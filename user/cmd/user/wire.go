//go:build wireinject
// +build wireinject

package main

import (
	"user/internal/biz"
	"user/internal/conf"
	"user/internal/data"
	"user/internal/server"
	"user/internal/service"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/google/wire"
)

func wireApp(*conf.Server, *conf.Data, log.Logger) (*kratos.App, func(), error) {
	panic(wire.Build(
		server.ProviderSet,
		data.ProviderSet,
		service.ProviderSet,
		biz.ProviderSet,
		newApp,
	))
}
