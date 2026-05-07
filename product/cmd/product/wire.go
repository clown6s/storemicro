//go:build wireinject
// +build wireinject

package main

import (
	"product/internal/biz"
	"product/internal/conf"
	"product/internal/data"
	"product/internal/server"
	"product/internal/service"

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
