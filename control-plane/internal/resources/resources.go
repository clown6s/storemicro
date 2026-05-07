package resources

import (
	"fmt"
	"net"
	"sort"
	"strconv"

	envoy_clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoy_corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoy_endpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoy_listenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	envoy_routev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	transcoderv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/grpc_json_transcoder/v3"
	healthv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/health_check/v3"
	routerv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/router/v3"
	hcmv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	"github.com/envoyproxy/go-control-plane/pkg/wellknown"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"control-plane/internal/config"
	"control-plane/internal/types"
)

// GenerateListeners 生成 LDS 资源（HTTP Listener）
func GenerateListeners(cfg *config.Config) []proto.Message {
	hcm := &hcmv3.HttpConnectionManager{
		StatPrefix: "ingress_http",
		CodecType:  hcmv3.HttpConnectionManager_AUTO,
		RouteSpecifier: &hcmv3.HttpConnectionManager_Rds{
			Rds: &hcmv3.Rds{
				ConfigSource: &envoy_corev3.ConfigSource{
					ConfigSourceSpecifier: &envoy_corev3.ConfigSource_Ads{},
				},
				RouteConfigName: "local_route",
			},
		},
		HttpFilters: []*hcmv3.HttpFilter{
			{
				Name: wellknown.HealthCheck,
				ConfigType: &hcmv3.HttpFilter_TypedConfig{
					TypedConfig: mustAny(&healthv3.HealthCheck{
						PassThroughMode: wrapperspb.Bool(false),
					}),
				},
			},
			{
				Name: wellknown.GRPCJSONTranscoder,
				ConfigType: &hcmv3.HttpFilter_TypedConfig{
					TypedConfig: mustAny(&transcoderv3.GrpcJsonTranscoder{
						DescriptorSet: &transcoderv3.GrpcJsonTranscoder_ProtoDescriptor{
							ProtoDescriptor: cfg.Transcoder.ProtoDescriptor,
						},
						Services: allServiceNames(cfg),
						PrintOptions: &transcoderv3.GrpcJsonTranscoder_PrintOptions{
							AddWhitespace:              true,
							AlwaysPrintPrimitiveFields: true,
							PreserveProtoFieldNames:    true,
						},
					}),
				},
			},
			{
				Name: wellknown.Router,
				ConfigType: &hcmv3.HttpFilter_TypedConfig{
					TypedConfig: mustAny(&routerv3.Router{}),
				},
			},
		},
	}

	hcmAny, _ := anypb.New(hcm)

	listener := &envoy_listenerv3.Listener{
		Name: "http_listener",
		Address: &envoy_corev3.Address{
			Address: &envoy_corev3.Address_SocketAddress{
				SocketAddress: &envoy_corev3.SocketAddress{
					Protocol: envoy_corev3.SocketAddress_TCP,
					Address:  "0.0.0.0",
					PortSpecifier: &envoy_corev3.SocketAddress_PortValue{
						PortValue: cfg.Listener.Port,
					},
				},
			},
		},
		FilterChains: []*envoy_listenerv3.FilterChain{
			{
				Filters: []*envoy_listenerv3.Filter{
					{
						Name: wellknown.HTTPConnectionManager,
						ConfigType: &envoy_listenerv3.Filter_TypedConfig{
							TypedConfig: hcmAny,
						},
					},
				},
			},
		},
	}

	return []proto.Message{listener}
}

// GenerateRouteConfig 生成 RDS 资源（路由配置）
func GenerateRouteConfig(cfg *config.Config) []proto.Message {
	// 按路由前缀长度降序排列（长前缀优先匹配）
	sorted := make([]config.ServiceConfig, len(cfg.Services))
	copy(sorted, cfg.Services)
	sort.Slice(sorted, func(i, j int) bool {
		return len(sorted[j].RoutePrefix) < len(sorted[i].RoutePrefix)
	})

	routes := make([]*envoy_routev3.Route, 0, len(sorted))
	for _, svc := range sorted {
		clusterName := svc.Name + "_cluster"
		routes = append(routes, &envoy_routev3.Route{
			Match: &envoy_routev3.RouteMatch{
				PathSpecifier: &envoy_routev3.RouteMatch_Prefix{
					Prefix: svc.RoutePrefix,
				},
			},
			Action: &envoy_routev3.Route_Route{
				Route: &envoy_routev3.RouteAction{
					ClusterSpecifier: &envoy_routev3.RouteAction_Cluster{
						Cluster: clusterName,
					},
					Timeout: durationpb.New(10 * 1000 * 1000 * 1000),
				},
			},
		})
	}

	routeConfig := &envoy_routev3.RouteConfiguration{
		Name: "local_route",
		VirtualHosts: []*envoy_routev3.VirtualHost{
			{
				Name:    "backend",
				Domains: []string{"*"},
				Routes:  routes,
			},
		},
	}

	return []proto.Message{routeConfig}
}

// GenerateClustersAndEndpoints 根据 etcd 中的服务实例生成 CDS + EDS 资源
func GenerateClustersAndEndpoints(cfg *config.Config, services map[string][]types.ServiceInstance) (clusters []proto.Message, endpoints []proto.Message) {
	clusters = make([]proto.Message, 0, len(cfg.Services))
	endpoints = make([]proto.Message, 0, len(cfg.Services))

	for _, svcCfg := range cfg.Services {
		clusterName := svcCfg.Name + "_cluster"

		cluster := &envoy_clusterv3.Cluster{
			Name: clusterName,
			ClusterDiscoveryType: &envoy_clusterv3.Cluster_Type{
				Type: envoy_clusterv3.Cluster_EDS,
			},
			EdsClusterConfig: &envoy_clusterv3.Cluster_EdsClusterConfig{
				EdsConfig: &envoy_corev3.ConfigSource{
					ConfigSourceSpecifier: &envoy_corev3.ConfigSource_Ads{},
				},
				ServiceName: clusterName,
			},
			LbPolicy: envoy_clusterv3.Cluster_ROUND_ROBIN,
			HealthChecks: []*envoy_corev3.HealthCheck{
				{
					Timeout:            durationpb.New(5 * 1000 * 1000 * 1000),
					Interval:           durationpb.New(10 * 1000 * 1000 * 1000),
					UnhealthyThreshold: wrapperspb.UInt32(3),
					HealthyThreshold:   wrapperspb.UInt32(1),
					HealthChecker: &envoy_corev3.HealthCheck_GrpcHealthCheck_{
						GrpcHealthCheck: &envoy_corev3.HealthCheck_GrpcHealthCheck{
							ServiceName: "",
						},
					},
				},
			},
		}
		clusters = append(clusters, cluster)

		// EDS endpoints
		lbEndpoints := make([]*envoy_endpointv3.LbEndpoint, 0)
		instances := services[svcCfg.Name]
		for _, inst := range instances {
			host, portStr, err := parseAddr(inst.Metadata.Addr)
			if err != nil {
				zap.L().Named("resources").Warn("invalid addr",
					zap.String("addr", inst.Metadata.Addr),
					zap.String("service", svcCfg.Name),
					zap.Error(err))
				continue
			}
			port, _ := strconv.Atoi(portStr)

			lbEndpoints = append(lbEndpoints, &envoy_endpointv3.LbEndpoint{
				HostIdentifier: &envoy_endpointv3.LbEndpoint_Endpoint{
					Endpoint: &envoy_endpointv3.Endpoint{
						Address: &envoy_corev3.Address{
							Address: &envoy_corev3.Address_SocketAddress{
								SocketAddress: &envoy_corev3.SocketAddress{
									Protocol: envoy_corev3.SocketAddress_TCP,
									Address:  host,
									PortSpecifier: &envoy_corev3.SocketAddress_PortValue{
										PortValue: uint32(port),
									},
								},
							},
						},
					},
				},
			})
		}

		// 无实例时使用占位地址
		if len(lbEndpoints) == 0 {
			lbEndpoints = append(lbEndpoints, &envoy_endpointv3.LbEndpoint{
				HostIdentifier: &envoy_endpointv3.LbEndpoint_Endpoint{
					Endpoint: &envoy_endpointv3.Endpoint{
						Address: &envoy_corev3.Address{
							Address: &envoy_corev3.Address_SocketAddress{
								SocketAddress: &envoy_corev3.SocketAddress{
									Protocol: envoy_corev3.SocketAddress_TCP,
									Address:  "127.0.0.1",
									PortSpecifier: &envoy_corev3.SocketAddress_PortValue{
										PortValue: 1,
									},
								},
							},
						},
					},
				},
			})
		}

		cla := &envoy_endpointv3.ClusterLoadAssignment{
			ClusterName: clusterName,
			Endpoints: []*envoy_endpointv3.LocalityLbEndpoints{
				{
					LbEndpoints: lbEndpoints,
				},
			},
		}
		endpoints = append(endpoints, cla)
	}

	return clusters, endpoints
}

// --- Helper functions ---

func mustAny(msg proto.Message) *anypb.Any {
	any, err := anypb.New(msg)
	if err != nil {
		panic(fmt.Sprintf("failed to marshal %T: %v", msg, err))
	}
	return any
}

func allServiceNames(cfg *config.Config) []string {
	names := make([]string, 0, len(cfg.Services))
	for _, svc := range cfg.Services {
		names = append(names, svc.GrpcService)
	}
	return names
}

func parseAddr(addr string) (host string, port string, err error) {
	return net.SplitHostPort(addr)
}
