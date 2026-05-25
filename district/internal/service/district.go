package service

import (
	"context"
	"errors"
	"sort"
	"sync/atomic"

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
	uc        *biz.DistrictUseCase
	treeCache atomic.Value // *districtv1.GetProvinceTreeResp — 全量树缓存（预热后只读）
}

// NewDistrictService 创建行政区 gRPC 服务
func NewDistrictService(uc *biz.DistrictUseCase) *DistrictService {
	s := &DistrictService{uc: uc}
	s.treeCache.Store((*districtv1.GetProvinceTreeResp)(nil))
	return s
}

func (d *DistrictService) GetProvinceTree(ctx context.Context, req *districtv1.GetProvinceTreeReq) (*districtv1.GetProvinceTreeResp, error) {
	// 全量查询：直接返回缓存的树（零建树开销）
	if req.AdCode == "" {
		if cached := d.treeCache.Load(); cached != nil {
			if resp, ok := cached.(*districtv1.GetProvinceTreeResp); ok && resp != nil {
				return resp, nil
			}
		}
		// 缓存未就绪，走正常路径
		return d.getAndBuild(ctx, "")
	}

	// 指定 adCode：走 DB（biz 层有 sync.Map 缓存）
	return d.getAndBuild(ctx, req.AdCode)
}

// getAndBuild 查询并构建树（仅用于 adCode 指定查询或缓存未就绪时的全量回退）
func (d *DistrictService) getAndBuild(ctx context.Context, adCode string) (*districtv1.GetProvinceTreeResp, error) {
	districts, err := d.uc.Get(ctx, adCode)
	if err != nil {
		if errors.Is(err, biz.ErrInvalidAdCode) {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		zap.L().Error("GetProvinceTree failed", zap.String("adCode", adCode), zap.Error(err))
		return nil, status.Error(codes.Internal, "internal error")
	}
	if len(districts) == 0 {
		if adCode == "" {
			return &districtv1.GetProvinceTreeResp{Data: []*districtv1.DistrictTree{}}, nil
		}
		return nil, status.Error(codes.NotFound, "district not found")
	}

	tree := buildDistrictTree(districts)
	return &districtv1.GetProvinceTreeResp{Data: tree}, nil
}

// WarmupTreeCache 预热全量树缓存：启动时构建一次，后续请求直接返回
func (d *DistrictService) WarmupTreeCache(ctx context.Context) {
	districts, err := d.uc.Get(ctx, "")
	if err != nil {
		zap.L().Error("WarmupTreeCache failed", zap.Error(err))
		return
	}
	if len(districts) == 0 {
		zap.L().Warn("WarmupTreeCache: no districts loaded, skip caching")
		return
	}

	tree := buildDistrictTree(districts)
	resp := &districtv1.GetProvinceTreeResp{Data: tree}
	d.treeCache.Store(resp)
	zap.L().Info("tree cache warmup completed", zap.Int("count", len(districts)))
}

// buildDistrictTree 将平铺的行政区列表构建为树结构
// 根节点判断：父 adCode 为 "100000" 或父节点不在结果集中
// 包含环检测：如果存在循环 parent 引用，记录警告并将环内节点提升为根
func buildDistrictTree(districts []*biz.District) []*districtv1.DistrictTree {
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

	// 2. O(n) 环检测：一次 DFS 标记所有在环上的节点（沿 parent 链向上遍历）
	//    标记为 true 的节点说明其祖先链中存在环，不能作为子节点挂载
	inCycle := detectCycles(nodeMap)

	var roots []*districtv1.DistrictTree

	// 3. 构建父子关系
	for _, node := range nodeMap {
		if inCycle[node.AdCode] {
			// 环内节点提升为根
			zap.L().Warn("buildDistrictTree: cycle detected, treating as root",
				zap.String("adCode", node.AdCode),
				zap.String("parentAdCode", node.ParentAdCode),
			)
			roots = append(roots, node)
			continue
		}
		parent, ok := nodeMap[node.ParentAdCode]
		if !ok || node.ParentAdCode == "100000" {
			roots = append(roots, node)
			continue
		}
		parent.Children = append(parent.Children, node)
	}

	// 4. 对各级 children 按 adCode 排序，保证输出稳定
	sortTree(roots)

	return roots
}

// detectCycles O(n) 环检测：对每个节点沿 parent 链向上标记祖先路径。
// 如果链中遇到另一个节点的"正在访问"标记，说明形成环，将整条链标记为 inCycle。
func detectCycles(nodeMap map[string]*districtv1.DistrictTree) map[string]bool {
	inCycle := make(map[string]bool, len(nodeMap))
	const (
		stateNone     = 0 // 未访问
		stateVisiting = 1 // 正在沿 parent 链向上遍历（当前路径上）
		stateDone     = 2 // 已完成遍历，确认无环
	)
	state := make(map[string]int8, len(nodeMap))

	for _, node := range nodeMap {
		if state[node.AdCode] != stateNone {
			continue
		}
		// 沿 parent 链向上走，收集当前路径
		var path []*districtv1.DistrictTree
		cur := node
		for cur != nil {
			s := state[cur.AdCode]
			if s == stateDone {
				// 已确认无环，路径上所有节点都安全
				break
			}
			if s == stateVisiting {
				// 发现环！从 cur 到 path 末尾之间的节点都在环上
				found := false
				for _, p := range path {
					if p.AdCode == cur.AdCode {
						found = true
					}
					if found {
						inCycle[p.AdCode] = true
					}
				}
				inCycle[cur.AdCode] = true
				break
			}
			// stateNone: 标记为正在访问，加入路径
			state[cur.AdCode] = stateVisiting
			path = append(path, cur)
			parent, ok := nodeMap[cur.ParentAdCode]
			if !ok || cur.ParentAdCode == "100000" {
				break
			}
			cur = parent
		}
		// 路径上所有节点标记为已完成
		for _, p := range path {
			state[p.AdCode] = stateDone
		}
	}

	return inCycle
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
