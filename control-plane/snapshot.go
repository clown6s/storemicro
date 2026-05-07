package main

import (
	"context"
	"fmt"
	"log"
	"sync/atomic"

	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	"github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"google.golang.org/protobuf/proto"
)

// SnapshotManager 管理 xDS 快照的生成和更新
type SnapshotManager struct {
	Cache   cache.SnapshotCache
	nodeID  string
	cfg     *Config
	version atomic.Int64
}

func NewSnapshotManager(nodeID string, cfg *Config) *SnapshotManager {
	return &SnapshotManager{
		Cache:  cache.NewSnapshotCache(false, cache.IDHash{}, nil),
		nodeID: nodeID,
		cfg:    cfg,
	}
}

// OnServiceUpdate 当服务列表变化时被调用，生成新的 xDS 快照
func (m *SnapshotManager) OnServiceUpdate(services map[string][]ServiceInstance) {
	version := m.version.Add(1)
	versionStr := fmt.Sprintf("v%d", version)

	log.Printf("[snapshot] generating snapshot %s, services: %v", versionStr, mapKeys(services))

	// 生成 xDS 资源（从配置读取服务列表）
	listeners := GenerateListeners(m.cfg)
	routes := GenerateRouteConfig(m.cfg)
	clusters, endpoints := GenerateClustersAndEndpoints(m.cfg, services)

	// v0.13+ API: map[resource.Type][]types.Resource
	resources := map[resource.Type][]types.Resource{
		resource.EndpointType: toResourceSlice(endpoints),
		resource.ClusterType:  toResourceSlice(clusters),
		resource.RouteType:    toResourceSlice(routes),
		resource.ListenerType: toResourceSlice(listeners),
	}

	snapshot, err := cache.NewSnapshot(versionStr, resources)
	if err != nil {
		log.Printf("[snapshot] failed to create snapshot: %v", err)
		return
	}

	if err := snapshot.Consistent(); err != nil {
		log.Printf("[snapshot] snapshot inconsistent: %v", err)
		return
	}

	for _, node := range m.Cache.GetStatusKeys() {
		if err := m.Cache.SetSnapshot(context.Background(), node, snapshot); err != nil {
			log.Printf("[snapshot] failed to set snapshot for node %s: %v", node, err)
		} else {
			log.Printf("[snapshot] pushed snapshot %s to node %s", versionStr, node)
		}
	}

	m.Cache.SetSnapshot(context.Background(), m.nodeID, snapshot)
}

func mapKeys(m map[string][]ServiceInstance) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func toResourceSlice(msgs []proto.Message) []types.Resource {
	result := make([]types.Resource, len(msgs))
	for i, msg := range msgs {
		result[i] = msg
	}
	return result
}
