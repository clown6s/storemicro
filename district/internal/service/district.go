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

func (d *DistrictService) GetProvinceTree(ctx context.Context, req *districtv1.GetProvinceTreeReq) (*districtv1.GetProvinceTreeResp, error) {
	zap.L().Info("GetProvinceTree called", zap.String("adCode", req.AdCode))

	// 调用 biz 层获取树形数据
	districts, err := d.uc.GetTree(ctx, req.AdCode)
	if err != nil {
		zap.L().Error("failed to get district tree", zap.Error(err))
		return &districtv1.GetProvinceTreeResp{
			Code:    500,
			Message: err.Error(),
		}, err
	}

	// 转换为 proto 类型
	data := make([]*districtv1.DistrictTree, 0, len(districts))
	for _, district := range districts {
		data = append(data, convertDistrictToProto(district))
	}

	return &districtv1.GetProvinceTreeResp{
		Code:    200,
		Message: "success",
		Data:    data,
	}, nil
}

// convertDistrictToProto 将 biz.District 转换为 proto DistrictTree
func convertDistrictToProto(d *biz.District) *districtv1.DistrictTree {
	if d == nil {
		return nil
	}

	children := make([]*districtv1.DistrictTree, 0, len(d.Children))
	for _, child := range d.Children {
		children = append(children, convertDistrictToProto(child))
	}

	return &districtv1.DistrictTree{
		Id:           d.ID,
		ParentAdCode: d.ParentAdCode,
		AdCode:       d.AdCode,
		Name:         d.Name,
		Level:        d.Level,
		Center:       d.Center,
		Children:     children,
	}
}
