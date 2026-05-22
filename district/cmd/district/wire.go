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

// wireResult Wire 注入结果（Wire 不支持多返回值，用结构体打包）
type wireResult struct {
	App     *kratos.App
	AppData *data.Data
	UseCase *biz.DistrictUseCase
	Service *service.DistrictService
}

func wireApp(*conf.Server, *conf.Data, log.Logger, registry.Registrar) (wireResult, func(), error) {
	panic(wire.Build(
		server.ProviderSet,
		data.ProviderSet,
		service.ProviderSet,
		biz.ProviderSet,
		newApp,
		newWireResult,
	))
}

func newWireResult(app *kratos.App, appData *data.Data, uc *biz.DistrictUseCase, svc *service.DistrictService) wireResult {
	return wireResult{App: app, AppData: appData, UseCase: uc, Service: svc}
}
