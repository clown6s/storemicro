package snapshot

import (
	"context"
	"fmt"
	"sync/atomic"

	cachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	cachev3 "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"

	"control-plane/internal/config"
	"control-plane/internal/resources"
	"control-plane/internal/types"
)

// SnapshotManager 管理 xDS 快照的生成和更新
type SnapshotManager struct {
	Cache   cachev3.SnapshotCache
	nodeID  string
	cfg     *config.Config
	version atomic.Int64
	logger  *zap.Logger
}

// NewSnapshotManager 创建快照管理器
func NewSnapshotManager(nodeID string, cfg *config.Config, logger *zap.Logger) *SnapshotManager {
	return &SnapshotManager{
		Cache:  cachev3.NewSnapshotCache(false, cachev3.IDHash{}, nil),
		nodeID: nodeID,
		cfg:    cfg,
		logger: logger.Named("snapshot"),
	}
}

// OnServiceUpdate 当服务列表变化时被调用，生成新的 xDS 快照
func (m *SnapshotManager) OnServiceUpdate(services map[string][]types.ServiceInstance) {
	version := m.version.Add(1)
	versionStr := fmt.Sprintf("v%d", version)

	m.logger.Info("generating snapshot",
		zap.String("version", versionStr),
		zap.Strings("services", mapKeys(services)),
	)

	listeners := resources.GenerateListeners(m.cfg)
	routes := resources.GenerateRouteConfig(m.cfg)
	clusters, endpoints := resources.GenerateClustersAndEndpoints(m.cfg, services)

	xdsResources := map[resource.Type][]cachetypes.Resource{
		resource.EndpointType: toResourceSlice(endpoints),
		resource.ClusterType:  toResourceSlice(clusters),
		resource.RouteType:    toResourceSlice(routes),
		resource.ListenerType: toResourceSlice(listeners),
	}

	snapshot, err := cachev3.NewSnapshot(versionStr, xdsResources)
	if err != nil {
		m.logger.Error("failed to create snapshot", zap.Error(err))
		return
	}

	if err := snapshot.Consistent(); err != nil {
		m.logger.Error("snapshot inconsistent", zap.Error(err))
		return
	}

	for _, node := range m.Cache.GetStatusKeys() {
		if err := m.Cache.SetSnapshot(context.Background(), node, snapshot); err != nil {
			m.logger.Error("failed to push snapshot", zap.String("node", node), zap.Error(err))
		} else {
			m.logger.Info("snapshot pushed", zap.String("version", versionStr), zap.String("node", node))
		}
	}

	m.Cache.SetSnapshot(context.Background(), m.nodeID, snapshot)
}

func mapKeys(m map[string][]types.ServiceInstance) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func toResourceSlice(msgs []proto.Message) []cachetypes.Resource {
	result := make([]cachetypes.Resource, len(msgs))
	for i, msg := range msgs {
		result[i] = msg
	}
	return result
}
