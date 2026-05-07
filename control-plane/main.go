package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"
)

func main() {
	// 命令行参数：配置文件路径
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	// 加载配置
	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	log.Printf("[control-plane] config loaded: %d services registered", len(cfg.Services))
	for _, svc := range cfg.Services {
		log.Printf("[control-plane]   - %s -> %s (%s)", svc.Name, svc.GrpcService, svc.RoutePrefix)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 连接 etcd
	watcher, err := NewEtcdWatcher(ctx, cfg.Etcd.Endpoints, cfg.Etcd.Prefix)
	if err != nil {
		log.Fatalf("failed to create etcd watcher: %v", err)
	}

	// 创建 snapshot manager（传入配置）
	snapshotMgr := NewSnapshotManager("storemicro-gateway", cfg)
	watcher.SetOnUpdate(snapshotMgr.OnServiceUpdate)

	// 启动 etcd watcher
	go watcher.Run(ctx)

	// 启动 ADS gRPC server
	adsAddr := fmt.Sprintf(":%d", cfg.Ads.Port)
	lis, err := net.Listen("tcp", adsAddr)
	if err != nil {
		log.Fatalf("failed to listen on %s: %v", adsAddr, err)
	}

	grpcServer := grpc.NewServer()
	RegisterADS(grpcServer, snapshotMgr.Cache)

	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			log.Fatalf("gRPC server error: %v", err)
		}
	}()

	log.Printf("[control-plane] ADS listening on %s, etcd: %v, listener port: %d",
		lis.Addr(), cfg.Etcd.Endpoints, cfg.Listener.Port)

	// 等待退出信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("[control-plane] shutting down...")
	grpcServer.GracefulStop()
	cancel()
}
