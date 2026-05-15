package snowflake

import (
	"context"
	"fmt"
	"sync"
	"time"

	sf "github.com/bwmarrin/snowflake"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
)

const (
	// etcd 中存储 node_id 的 key 前缀
	keyPrefix = "/snowflake/nodes/"
	// 最大 node_id（10 bit = 1024 个节点）
	maxNodeID = 1023
	// 租约时长（秒），实例需要定期续约
	leaseTTL = 30
)

var (
	node *sf.Node

	// NodeID 当前实例分配到的 node_id
	NodeID int64

	// nodeReady 标记 snowflake 节点是否可用
	// 租约丢失且无法重新获取时，nodeReady 设为 false，GenID() 将直接报错而非生成冲突 ID
	nodeReady bool
	readyMu   sync.RWMutex

	// [L2] 用于通知后台 goroutine 优雅退出的 cancel
	shutdownCtx    context.Context
	shutdownCancel context.CancelFunc
	shutdownOnce   sync.Once
)

func init() {
	shutdownCtx, shutdownCancel = context.WithCancel(context.Background())
}

// Shutdown 通知所有 snowflake 后台 goroutine 退出，应在进程关闭时调用
func Shutdown() {
	shutdownOnce.Do(func() {
		shutdownCancel()
	})
}

// InitWithEtcd 通过 etcd 自动分配唯一的 node_id
// 启动时从 0 遍历到 1023，尝试抢锁，抢到第一个可用的即作为本实例的 node_id
// 同时启动后台协程续约租约，保证实例存活期间 node_id 不会被其他实例抢占
func InitWithEtcd(etcdClient *clientv3.Client, serviceName string) error {
	ctx := context.Background()

	for i := 0; i <= maxNodeID; i++ {
		key := fmt.Sprintf("%s%s/%d", keyPrefix, serviceName, i)

		// 创建租约
		lease, err := etcdClient.Grant(ctx, leaseTTL)
		if err != nil {
			return fmt.Errorf("snowflake: create lease failed: %w", err)
		}

		// 尝试写入（NX = 不存在才写入），抢锁
		txn := etcdClient.Txn(ctx).
			If(clientv3.Compare(clientv3.CreateRevision(key), "=", 0)).
			Then(clientv3.OpPut(key, "1", clientv3.WithLease(lease.ID)))

		resp, err := txn.Commit()
		if err != nil {
			return fmt.Errorf("snowflake: txn commit failed: %w", err)
		}

		if resp.Succeeded {
			// 抢到了，初始化 snowflake node
			var sfNode *sf.Node
			sfNode, err = sf.NewNode(int64(i))
			if err != nil {
				// [L1] 抢到但初始化失败，释放 lease
				_, _ = etcdClient.Revoke(ctx, lease.ID)
				return fmt.Errorf("snowflake: create node failed: %w", err)
			}
			node = sfNode
			NodeID = int64(i)
			readyMu.Lock()
			nodeReady = true
			readyMu.Unlock()

			// 后台续约
			go keepAlive(etcdClient, lease.ID, key)

			zap.L().Info("snowflake: allocated node_id",
				zap.Int("node_id", i),
				zap.String("service", serviceName),
			)
			return nil
		}

		// [L1] 没抢到，释放不再需要的 lease
		_, _ = etcdClient.Revoke(ctx, lease.ID)
	}

	return fmt.Errorf("snowflake: no available node_id (all 0-%d are occupied)", maxNodeID)
}

// keepAlive 后台续约租约，实例退出或崩溃时租约自动过期，node_id 被释放
func keepAlive(etcdClient *clientv3.Client, leaseID clientv3.LeaseID, key string) {
	ctx := shutdownCtx

	ch, err := etcdClient.KeepAlive(ctx, leaseID)
	if err != nil {
		zap.L().Error("snowflake: keep-alive failed", zap.Error(err))
		return
	}

	// 消费 keep-alive 响应通道
	for {
		select {
		case <-ctx.Done():
			// [L2] 进程关闭，退出 goroutine
			zap.L().Info("snowflake: keep-alive goroutine exiting (shutdown)")
			return
		case resp, ok := <-ch:
			if !ok {
				zap.L().Warn("snowflake: keep-alive channel closed, disabling GenID until lease is re-acquired")
				readyMu.Lock()
				nodeReady = false
				readyMu.Unlock()
				// 尝试重新续约
				go retryKeepAlive(etcdClient, key)
				return
			}
			_ = resp
		}
	}
}

// retryKeepAlive 租约丢失后尝试重新抢锁
func retryKeepAlive(etcdClient *clientv3.Client, key string) {
	ctx := shutdownCtx

	for {
		select {
		case <-ctx.Done():
			// [L2] 进程关闭，退出重试循环
			return
		default:
		}

		time.Sleep(5 * time.Second)

		lease, err := etcdClient.Grant(ctx, leaseTTL)
		if err != nil {
			zap.L().Error("snowflake: retry grant failed", zap.Error(err))
			continue
		}

		txn := etcdClient.Txn(ctx).
			If(clientv3.Compare(clientv3.CreateRevision(key), "=", 0)).
			Then(clientv3.OpPut(key, "1", clientv3.WithLease(lease.ID)))

		resp, err := txn.Commit()
		if err != nil {
			zap.L().Error("snowflake: retry txn failed", zap.Error(err))
			// [L1] 释放失败的 lease
			_, _ = etcdClient.Revoke(ctx, lease.ID)
			continue
		}

		if resp.Succeeded {
			ch, err := etcdClient.KeepAlive(ctx, lease.ID)
			if err != nil {
				zap.L().Error("snowflake: retry keep-alive failed", zap.Error(err))
				_, _ = etcdClient.Revoke(ctx, lease.ID)
				continue
			}
			go func() {
				for {
					select {
					case <-ctx.Done():
						return
					case _, ok := <-ch:
						if !ok {
							readyMu.Lock()
							nodeReady = false
							readyMu.Unlock()
							zap.L().Warn("snowflake: retry keep-alive closed, retrying...")
							return
						}
					}
				}
			}()
			readyMu.Lock()
			nodeReady = true
			readyMu.Unlock()
			zap.L().Info("snowflake: re-acquired node_id successfully")
			return
		}

		// key 被别人占了，释放 lease
		_, _ = etcdClient.Revoke(ctx, lease.ID)
		// nodeReady 保持 false，GenID() 将直接报错
		zap.L().Error("snowflake: failed to re-acquire node_id, GenID is permanently disabled - please restart instance")
		return
	}
}

// GenID 生成一个雪花 ID
// 如果节点 lease 已丢失且未重新获取，直接返回错误，防止与其他实例产生 ID 碰撞
func GenID() (int64, error) {
	readyMu.RLock()
	defer readyMu.RUnlock()
	if !nodeReady || node == nil {
		return 0, fmt.Errorf("snowflake: node not ready, cannot generate ID (lease lost or not initialized)")
	}
	return node.Generate().Int64(), nil
}
