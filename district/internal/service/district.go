package service

import (
	"context"
	"sort"

	districtv1 "district/api/district/v1"
	"district/internal/biz"

	"github.com/google/wire"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	zap.L().Debug("GetProvinceTree called", zap.String("adCode", req.AdCode))

	districts, err := d.uc.Get(ctx, req.AdCode)
	if err != nil {
		zap.L().Error("GetProvinceTree failed", zap.String("adCode", req.AdCode), zap.Error(err))
		return nil, status.Error(codes.Internal, "internal error")
	}
	if len(districts) == 0 {
		// [M4] 区分空查询（adCode 为空）与未找到（adCode 指定但不存在）
		if req.AdCode == "" {
			// 全量查询结果为空，返回 OK + 空数组
			return &districtv1.GetProvinceTreeResp{
				Code:    200,
				Message: "success",
				Data:    []*districtv1.DistrictTree{},
			}, nil
		}
		return nil, status.Errorf(codes.NotFound, "district not found: %s", req.AdCode)
	}

	tree := BuildDistrictTree(districts)
	return &districtv1.GetProvinceTreeResp{
		Code:    200,
		Message: "success",
		Data:    tree,
	}, nil
}

// BuildDistrictTree 将平铺的行政区列表构建为树结构
// 根节点判断：父 adCode 为 "100000" 或父节点不在结果集中
func BuildDistrictTree(districts []*biz.District) []*districtv1.DistrictTree {
	nodeMap := make(map[string]*districtv1.DistrictTree, len(districts))

	// 1. 创建所有节点
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
		parent, ok := nodeMap[node.ParentAdCode]
		if !ok || node.ParentAdCode == "100000" {
			// 父节点不在结果集中，或是省级根节点
			roots = append(roots, node)
			continue
		}
		parent.Children = append(parent.Children, node)
	}

	// 3. 对各级 children 按 adCode 排序，保证输出稳定
	sortTree(roots)

	return roots
}

func sortTree(nodes []*districtv1.DistrictTree) {
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].AdCode < nodes[j].AdCode
	})
	for _, node := range nodes {
		if len(node.Children) > 0 {
			sortTree(node.Children)
		}
	}
}
