package proxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	etcdreg "github.com/go-kratos/kratos/contrib/registry/etcd/v2"
	"github.com/go-kratos/kratos/v2/transport/grpc/resolver/discovery"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/redis/go-redis/v9"
	pkgretry "github.com/storemicro/pkg/retry"
	pkglog "github.com/storemicro/pkg/zaplogger"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	_ "google.golang.org/grpc/health"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	"gateway/internal/appconfig"
	"gateway/internal/auth"
	"gateway/internal/circuitbreaker"
	gzipmw "gateway/internal/middleware"
)

// ServiceRegistration 描述一个后端 gRPC 服务的注册信息
type ServiceRegistration struct {
	Name            string
	RegisterHandler func(ctx context.Context, mux *runtime.ServeMux, conn *grpc.ClientConn) error
}

// Proxy 封装 grpc-gateway mux 和 gRPC 连接
type Proxy struct {
	mux            *runtime.ServeMux
	conns          []*grpc.ClientConn
	etcdClient     *clientv3.Client
	rdb            *redis.Client
	jwtSecret      string
	origins        []string
	proxies        []string
	otelHandler    http.Handler
	requestTimeout time.Duration // 单次 gRPC 请求超时
	closeOnce      sync.Once     // #14: 防止 Close 被重复调用
	// HTTP 响应缓存（内存级，用于大响应如 provinceTree）
	respCache    map[string]*cachedResponse
	respCacheMu  sync.RWMutex
	respCacheTTL time.Duration
	cachedPaths  map[string]struct{} // 需要缓存的路径白名单
}

// cachedResponse 缓存的完整 HTTP 响应
type cachedResponse struct {
	status   int
	headers  http.Header
	body     []byte
	cachedAt time.Time
}

// cacheResponseWriter 捕获 HTTP 响应到内存 buffer（用于首次缓存）
type cacheResponseWriter struct {
	status int
	header http.Header
	buf    *bytes.Buffer
}

func (cw *cacheResponseWriter) Header() http.Header { return cw.header }
func (cw *cacheResponseWriter) Write(b []byte) (int, error) {
	if cw.status == 0 {
		cw.status = http.StatusOK
	}
	return cw.buf.Write(b)
}
func (cw *cacheResponseWriter) WriteHeader(statusCode int) { cw.status = statusCode }
func (cw *cacheResponseWriter) Flush()                     {} // gzip 中间件需要
func (cw *cacheResponseWriter) CloseNotify() <-chan bool   { return nil }

// New 创建 grpc-gateway 代理
func New(cfg *appconfig.AppConfig, services []ServiceRegistration) (*Proxy, error) {
	p := &Proxy{
		jwtSecret:      cfg.Jwt.Secret,
		origins:        cfg.Server.AllowOrigins,
		proxies:        cfg.Server.TrustedProxies,
		requestTimeout: cfg.GrpcClient.RequestTimeout,
		respCacheTTL:   5 * time.Minute, // 缓存 TTL
		respCache:      make(map[string]*cachedResponse),
		cachedPaths: map[string]struct{}{
			"/api/v1/district/provinceTree": {},
		},
	}

	// 创建 Redis 客户端（连接池配置从配置读取）
	rdb := redis.NewClient(&redis.Options{
		Addr:         cfg.Redis.Addr,
		Password:     cfg.Redis.Password,
		DB:           cfg.Redis.Db,
		PoolSize:     cfg.Redis.PoolSize,
		MinIdleConns: cfg.Redis.MinIdleConns,
		DialTimeout:  cfg.Redis.DialTimeout,
		ReadTimeout:  cfg.Redis.ReadTimeout,
		WriteTimeout: cfg.Redis.WriteTimeout,
		MaxRetries:   cfg.Redis.MaxRetries,
	})

	// 验证 Redis 连接（带重试，30s 整体超时）
	redisCtx, redisCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer redisCancel()
	if err := pkgretry.Do(redisCtx, pkgretry.DefaultConfig(), "redis",
		func(rCtx context.Context) error {
			pingCtx, pingCancel := context.WithTimeout(rCtx, 5*time.Second)
			defer pingCancel()
			return rdb.Ping(pingCtx).Err()
		},
	); err != nil {
		return nil, fmt.Errorf("failed to connect redis after retries: %w", err)
	}
	p.rdb = rdb
	pkglog.L().Info("redis connected", zap.String("addr", cfg.Redis.Addr))

	// 创建 etcd client（用于服务发现，带重试，60s 整体超时）
	var etcdClient *clientv3.Client
	etcdCtx, etcdCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer etcdCancel()
	if err := pkgretry.Do(etcdCtx, pkgretry.DefaultConfig(), "etcd",
		func(rCtx context.Context) error {
			if etcdClient != nil {
				etcdClient.Close()
			}
			var err error
			etcdClient, err = clientv3.New(clientv3.Config{
				Endpoints:   cfg.Etcd.Endpoints,
				DialTimeout: cfg.Etcd.DialTimeout,
				Username:    cfg.Etcd.Username,
				Password:    cfg.Etcd.Password,
			})
			return err
		},
	); err != nil {
		return nil, fmt.Errorf("failed to connect etcd after retries: %w", err)
	}
	p.etcdClient = etcdClient

	etcdRegistry := etcdreg.New(etcdClient)
	discoveryBuilder := discovery.NewBuilder(
		etcdRegistry,
		discovery.WithInsecure(true),
		discovery.PrintDebugLog(false),
	)

	// gRPC 连接选项（keepalive + otel + retry + 熔断器）
	cbCfg := &circuitbreaker.Config{
		MaxRequests:  cfg.GrpcClient.CircuitBreaker.MaxRequests,
		Interval:     cfg.GrpcClient.CircuitBreaker.Interval,
		Timeout:      cfg.GrpcClient.CircuitBreaker.Timeout,
		FailureRatio: cfg.GrpcClient.CircuitBreaker.FailureRatio,
		MinRequests:  cfg.GrpcClient.CircuitBreaker.MinRequests,
	}

	serviceCfg, err := cfg.GrpcClient.BuildServiceConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to build gRPC service config: %w", err)
	}

	// 构建服务名列表（供熔断器白名单使用）
	serviceNames := make([]string, 0, len(services))
	for _, svc := range services {
		serviceNames = append(serviceNames, svc.Name)
	}

	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithResolvers(discoveryBuilder),
		grpc.WithDefaultServiceConfig(serviceCfg),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
		grpc.WithUnaryInterceptor(circuitbreaker.UnaryInterceptor(cbCfg, serviceNames)),
		grpc.WithStreamInterceptor(circuitbreaker.StreamInterceptor(cbCfg, serviceNames)),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(cfg.GrpcClient.MaxRecvMsgSize*1024*1024),
			grpc.MaxCallSendMsgSize(cfg.GrpcClient.MaxSendMsgSize*1024*1024),
		),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:    30 * time.Second,
			Timeout: 10 * time.Second,
		}),
	}

	p.mux = runtime.NewServeMux(
		runtime.WithMarshalerOption(runtime.MIMEWildcard, &runtime.JSONPb{
			MarshalOptions: protojson.MarshalOptions{
				UseProtoNames: false, // JSON 输出使用 camelCase（如 pageSize）而非 snake_case（如 page_size）
			},
			UnmarshalOptions: protojson.UnmarshalOptions{
				DiscardUnknown: true,
			},
		}),
		runtime.WithErrorHandler(customErrorHandler),
		runtime.WithRoutingErrorHandler(customRoutingErrorHandler),
		runtime.WithMetadata(func(ctx context.Context, r *http.Request) metadata.MD {
			md := metadata.MD{}
			// 注入 JWT 认证后的 user_id（供下游服务做资源归属校验）
			if userID, ok := ctx.Value(auth.ContextKeyUserID).(int64); ok {
				md.Set("x-user-id", strconv.FormatInt(userID, 10))
			}
			// 注入 X-Request-Id（跨服务链路追踪）
			if reqID := r.Header.Get("X-Request-Id"); reqID != "" {
				md.Set("x-request-id", reqID)
			}
			return md
		}),
	)

	// RegisterHandler 使用带超时的 context，防止启动时卡死
	regCtx, regCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer regCancel()

	for _, svc := range services {
		conn, err := grpc.NewClient("discovery:///"+svc.Name, dialOpts...)
		if err != nil {
			p.Close()
			return nil, fmt.Errorf("failed to connect %s service: %w", svc.Name, err)
		}
		p.conns = append(p.conns, conn)

		if err := svc.RegisterHandler(regCtx, p.mux, conn); err != nil {
			p.Close()
			return nil, fmt.Errorf("failed to register %s handler: %w", svc.Name, err)
		}

		pkglog.L().Info("service registered", zap.String("service", svc.Name))
	}

	// 预构建 otel handler（gzip → otel → panic recovery → CORS → security headers → JWT 认证 → grpc-gateway mux）
	p.otelHandler = gzipmw.Gzip(otelhttp.NewHandler(
		auth.RecoverMiddleware(auth.CORSMiddleware(p.origins)(auth.SecurityHeadersMiddleware(auth.JWTAuth(p.jwtSecret, p.rdb, p.proxies, p.mux)))),
		"gateway",
	))

	return p, nil
}

// WarmupResponseCache 预热 HTTP 响应缓存
// 直接调用 grpc-gateway mux，跳过 JWT 等中间件，手动 gzip 后存入缓存
// 在 main.go 中 HTTP 服务器启动后调用，确保首个外部请求直接命中缓存
func (p *Proxy) WarmupResponseCache(ctx context.Context) {
	for cachePath := range p.cachedPaths {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, cachePath, nil)
		if err != nil {
			pkglog.L().Warn("warmup: bad path", zap.String("path", cachePath), zap.Error(err))
			continue
		}

		cw := &cacheResponseWriter{header: make(http.Header), buf: &bytes.Buffer{}}
		p.mux.ServeHTTP(cw, req)

		if cw.status != http.StatusOK {
			pkglog.L().Warn("warmup: non-200 response",
				zap.Int("status", cw.status),
				zap.String("path", cachePath),
			)
			continue
		}

		rawBody := cw.buf.Bytes()
		now := time.Now()

		// 缓存原始（非 gzip）版本
		p.respCacheMu.Lock()
		p.respCache[cachePath] = &cachedResponse{
			status: cw.status, headers: cw.header.Clone(),
			body: rawBody, cachedAt: now,
		}
		p.respCacheMu.Unlock()

		// 缓存 gzip 版本
		var gzBuf bytes.Buffer
		gz := gzip.NewWriter(&gzBuf)
		gz.Write(rawBody)
		gz.Close()

		gzHeaders := cw.header.Clone()
		gzHeaders.Set("Content-Encoding", "gzip")
		gzHeaders.Del("Content-Length")

		p.respCacheMu.Lock()
		p.respCache[cachePath+"?gzip"] = &cachedResponse{
			status: cw.status, headers: gzHeaders,
			body: gzBuf.Bytes(), cachedAt: now,
		}
		p.respCacheMu.Unlock()

		pkglog.L().Info("response cache warmed up",
			zap.String("path", cachePath),
			zap.Int("rawSize", len(rawBody)),
			zap.Int("gzipSize", gzBuf.Len()),
		)
	}
}

// ServeHTTP 实现 http.Handler 接口
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// ===== HTTP 响应缓存：对指定路径的 GET 请求缓存完整响应（跳过 gRPC + JSON 序列化 + gzip） =====
	if r.Method == http.MethodGet && r.URL.RawQuery == "" {
		if _, shouldCache := p.cachedPaths[r.URL.Path]; shouldCache {
			acceptGzip := strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
			cacheKey := r.URL.Path
			if acceptGzip {
				cacheKey += "?gzip"
			}

			p.respCacheMu.RLock()
			cached, hit := p.respCache[cacheKey]
			p.respCacheMu.RUnlock()

			if hit && time.Since(cached.cachedAt) < p.respCacheTTL {
				// 命中：直接写回，跳过所有中间件和 gRPC 调用
				for k, vv := range cached.headers {
					for _, v := range vv {
						w.Header().Add(k, v)
					}
				}
				w.Header().Set("Cache-Control", "public, max-age=300")
				w.Header().Set("Content-Length", strconv.Itoa(len(cached.body)))
				w.Header().Set("X-Cache", "HIT")
				w.WriteHeader(cached.status)
				w.Write(cached.body)
				return
			}

			// 未命中：走完整中间件链，捕获响应后缓存
			ctx, cancel := context.WithTimeout(r.Context(), p.requestTimeout)

			cw := &cacheResponseWriter{
				header: make(http.Header),
				buf:    &bytes.Buffer{},
			}
			p.otelHandler.ServeHTTP(cw, r.WithContext(ctx))
			cancel()

			// 只缓存 200 响应
			if cw.status == http.StatusOK {
				p.respCacheMu.Lock()
				p.respCache[cacheKey] = &cachedResponse{
					status:   cw.status,
					headers:  cw.header.Clone(),
					body:     cw.buf.Bytes(),
					cachedAt: time.Now(),
				}
				p.respCacheMu.Unlock()

				pkglog.L().Info("response cached",
					zap.String("path", r.URL.Path),
					zap.Bool("gzip", acceptGzip),
					zap.Int("size", cw.buf.Len()),
				)
			}

			// 写回给客户端
			for k, vv := range cw.header {
				for _, v := range vv {
					w.Header().Add(k, v)
				}
			}
			w.Header().Set("X-Cache", "MISS")
			w.Header().Set("Content-Length", strconv.Itoa(cw.buf.Len()))
			w.WriteHeader(cw.status)
			w.Write(cw.buf.Bytes())
			return
		}
	}

	// ===== 非缓存路径：正常走中间件链 =====
	ctx, cancel := context.WithTimeout(r.Context(), p.requestTimeout)
	defer cancel()
	p.otelHandler.ServeHTTP(w, r.WithContext(ctx))
}

// Rdb 返回 Redis 客户端（供健康检查使用）
func (p *Proxy) Rdb() *redis.Client {
	return p.rdb
}

// EtcdClient 返回 etcd 客户端（供健康检查使用）
func (p *Proxy) EtcdClient() *clientv3.Client {
	return p.etcdClient
}

// Conns 返回 gRPC 连接列表（供优雅关闭 drain 使用）
func (p *Proxy) Conns() []*grpc.ClientConn {
	return p.conns
}

// Close 关闭所有连接（#14: sync.Once 保证仅执行一次，#22: 10s 超时防阻塞）
func (p *Proxy) Close() {
	p.closeOnce.Do(func() {
		pkglog.L().Info("gateway proxy closing connections...")
		for _, conn := range p.conns {
			if conn != nil {
				// #22: 每个 gRPC 连接关闭加 10s 超时，防止后端挂起时无限阻塞
				closeDone := make(chan error, 1)
				go func(c *grpc.ClientConn) {
					closeDone <- c.Close()
				}(conn)
				select {
				case err := <-closeDone:
					if err != nil {
						pkglog.L().Warn("failed to close gRPC connection", zap.Error(err))
					}
				case <-time.After(10 * time.Second):
					pkglog.L().Warn("gRPC connection close timed out after 10s")
				}
			}
		}
		p.conns = nil
		if p.rdb != nil {
			if err := p.rdb.Close(); err != nil {
				pkglog.L().Warn("failed to close redis connection", zap.Error(err))
			}
		}
		if p.etcdClient != nil {
			if err := p.etcdClient.Close(); err != nil {
				pkglog.L().Warn("failed to close etcd client", zap.Error(err))
			}
		}
	})
}

// customErrorHandler 将 gRPC 错误转为统一的 JSON 格式 HTTP 响应（#12: 不泄露上游错误详情）
func customErrorHandler(ctx context.Context, mux *runtime.ServeMux, marshaler runtime.Marshaler, w http.ResponseWriter, r *http.Request, err error) {
	st, ok := status.FromError(err)
	if !ok {
		// 非 gRPC 错误，返回通用 500
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":500,"message":"internal server error"}`))
		return
	}

	httpStatus := runtime.HTTPStatusFromCode(st.Code())
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(httpStatus)
	// 仅返回状态码和通用消息，不暴露上游 gRPC 错误详情
	_, _ = w.Write([]byte(fmt.Sprintf(`{"code":%d,"message":"%s"}`, httpStatus, http.StatusText(httpStatus))))
}

// customRoutingErrorHandler 未匹配路由时返回 JSON 格式 404（替代默认纯文本）
func customRoutingErrorHandler(ctx context.Context, mux *runtime.ServeMux, marshaler runtime.Marshaler, w http.ResponseWriter, r *http.Request, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	switch status {
	case http.StatusMethodNotAllowed:
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte(`{"code":405,"message":"method not allowed"}`))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":404,"message":"not found"}`))
	}
}
