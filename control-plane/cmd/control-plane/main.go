package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"
	"google.golang.org/grpc"

	"control-plane/internal/ads"
	"control-plane/internal/config"
	"control-plane/internal/snapshot"
	"control-plane/internal/watcher"
)

func main() {
	configPath := flag.String("config", "configs/config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	logger, err := config.BuildLogger(cfg.Log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to init logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()
	zap.ReplaceGlobals(logger)

	logger.Info("control-plane starting",
		zap.Int("services", len(cfg.Services)),
		zap.Uint32("listener_port", cfg.Listener.Port),
		zap.Uint32("ads_port", cfg.Ads.Port),
		zap.Strings("etcd_endpoints", cfg.Etcd.Endpoints),
	)
	for _, svc := range cfg.Services {
		logger.Info("service registered",
			zap.String("name", svc.Name),
			zap.String("grpc", svc.GrpcService),
			zap.String("prefix", svc.RoutePrefix),
		)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := watcher.NewEtcdWatcher(ctx, cfg.Etcd.Endpoints, cfg.Etcd.Prefix)
	if err != nil {
		logger.Fatal("failed to create etcd watcher", zap.Error(err))
	}

	snapshotMgr := snapshot.NewSnapshotManager("storemicro-gateway", cfg, logger)
	w.SetOnUpdate(snapshotMgr.OnServiceUpdate)

	go w.Run(ctx)

	adsAddr := fmt.Sprintf(":%d", cfg.Ads.Port)
	lis, err := net.Listen("tcp", adsAddr)
	if err != nil {
		logger.Fatal("failed to listen", zap.String("addr", adsAddr), zap.Error(err))
	}

	grpcServer := grpc.NewServer()
	ads.RegisterADS(grpcServer, snapshotMgr.Cache)

	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			logger.Fatal("gRPC server error", zap.Error(err))
		}
	}()

	logger.Info("ADS server listening",
		zap.String("addr", lis.Addr().String()),
		zap.Strings("etcd", cfg.Etcd.Endpoints),
	)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("shutting down...")
	grpcServer.GracefulStop()
	cancel()
}
