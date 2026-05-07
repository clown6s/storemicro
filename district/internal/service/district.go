package service

import (
	"context"
	districtv1 "district/api/district/v1"
	"district/internal/biz"

	"github.com/google/wire"
	"go.uber.org/zap"
)

var ProviderSet = wire.NewSet(NewDistrictService)

type DistrictService struct {
	districtv1.UnimplementedDistrictServer
	uc *biz.DistrictUseCase
}

// NewDistrictService 创建行政区 gRPC 服务
func NewDistrictService(uc *biz.DistrictUseCase) *DistrictService {
	return &DistrictService{uc: uc}
}

func (d *DistrictService) GetProvinceTree(context.Context, *districtv1.GetProvinceTreeReq) (*districtv1.GetProvinceTreeResp, error) {

	zap.L().Info("进入了")
	return &districtv1.GetProvinceTreeResp{}, nil
}
