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

	districts, err := d.uc.Get(ctx, req.AdCode)
	if err != nil {
		return nil, err
	}
	tree := BuildDistrictTree(districts)
	return &districtv1.GetProvinceTreeResp{
		Code:    200,
		Message: "success",
		Data:    tree,
	}, nil
}

func BuildDistrictTree(districts []*biz.District) []*districtv1.DistrictTree {
	nodeMap := make(map[string]*districtv1.DistrictTree)

	// 1. 先创建所有节点
	for _, d := range districts {
		nodeMap[d.AdCode] = &districtv1.DistrictTree{
			Id:           d.ID,
			ParentAdCode: d.ParentAdCode,
			AdCode:       d.AdCode,
			Name:         d.Name,
			Level:        d.Level,
			Center:       d.Center,
			Children:     []*districtv1.DistrictTree{},
		}
	}

	var roots []*districtv1.DistrictTree

	// 2. 构建树
	for _, node := range nodeMap {
		// 没有父节点
		if node.ParentAdCode == "100000" {
			roots = append(roots, node)
			continue
		}

		// 找父节点
		parent, ok := nodeMap[node.ParentAdCode]
		if !ok {
			// 父节点不存在，作为根节点
			roots = append(roots, node)
			continue
		}

		parent.Children = append(parent.Children, node)
	}

	return roots
}
