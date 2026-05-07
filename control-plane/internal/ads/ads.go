package ads

import (
	"context"

	discoveryv3 "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	"github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"github.com/envoyproxy/go-control-plane/pkg/server/v3"
	"google.golang.org/grpc"
)

// RegisterADS 将 ADS (Aggregated Discovery Service) 注册到 gRPC server
func RegisterADS(s *grpc.Server, snapshotCache cache.SnapshotCache) {
	srv := server.NewServer(context.Background(), snapshotCache, nil)
	discoveryv3.RegisterAggregatedDiscoveryServiceServer(s, srv)
}
