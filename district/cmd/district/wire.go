//go:build wireinject
// +build wireinject

package main

import (
	"district/internal/biz"
	"district/internal/conf"
	"district/internal/data"
	"district/internal/server"
	"district/internal/service"

	"github.com/go-kratos/kratos/v2"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/registry"
	"github.com/google/wire"
)

func wireApp(*conf.Server, *conf.Data, log.Logger, registry.Registrar) (*kratos.App, *data.Data, func(), error) {
	panic(wire.Build(
		server.ProviderSet,
		data.ProviderSet,
		service.ProviderSet,
		biz.ProviderSet,
		newApp,
	))
}
