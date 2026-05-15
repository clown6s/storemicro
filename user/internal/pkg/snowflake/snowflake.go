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
	// node 和 NodeID 必须在 readyMu 保护下读写，确保并发安全
	node   *sf.Node
	NodeID int64

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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

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
			readyMu.Lock()
			node = sfNode
			NodeID = int64(i)
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
// 如果 keep-alive 通道关闭，在同一 goroutine 中重试，避免递归创建 goroutine
func keepAlive(etcdClient *clientv3.Client, leaseID clientv3.LeaseID, key string) {
	ctx := shutdownCtx

	ch, err := etcdClient.KeepAlive(ctx, leaseID)
	if err != nil {
		zap.L().Error("snowflake: keep-alive failed", zap.Error(err))
		return
	}

	for {
		select {
		case <-ctx.Done():
			zap.L().Info("snowflake: keep-alive goroutine exiting (shutdown)")
			return
		case resp, ok := <-ch:
			if !ok {
				zap.L().Warn("snowflake: keep-alive channel closed, disabling GenID until lease is re-acquired")
				readyMu.Lock()
				nodeReady = false
				readyMu.Unlock()
				// 在同一 goroutine 中重试，不创建新 goroutine
				if retryKeepAliveOnce(etcdClient, key) {
					return // 成功重新获取 lease
				}
				return // 重试失败
			}
			_ = resp
		}
	}
}

// retryKeepAliveOnce 租约丢失后尝试重新抢锁（单次尝试，在同一 goroutine 中执行）
func retryKeepAliveOnce(etcdClient *clientv3.Client, key string) bool {
	ctx := shutdownCtx

	select {
	case <-ctx.Done():
		return false
	default:
	}

	// 可取消等待，避免 time.Sleep 阻塞进程关闭
	timer := time.NewTimer(5 * time.Second)
	select {
	case <-ctx.Done():
		timer.Stop()
		return false
	case <-timer.C:
	}

	select {
	case <-ctx.Done():
		return false
	default:
	}

	lease, err := etcdClient.Grant(ctx, leaseTTL)
	if err != nil {
		zap.L().Error("snowflake: retry grant failed", zap.Error(err))
		return false
	}

	txn := etcdClient.Txn(ctx).
		If(clientv3.Compare(clientv3.CreateRevision(key), "=", 0)).
		Then(clientv3.OpPut(key, "1", clientv3.WithLease(lease.ID)))

	resp, err := txn.Commit()
	if err != nil {
		zap.L().Error("snowflake: retry txn failed", zap.Error(err))
		_, _ = etcdClient.Revoke(ctx, lease.ID)
		return false
	}

	if !resp.Succeeded {
		_, _ = etcdClient.Revoke(ctx, lease.ID)
		zap.L().Error("snowflake: failed to re-acquire node_id, GenID is permanently disabled - please restart instance")
		return false
	}

	ch, err := etcdClient.KeepAlive(ctx, lease.ID)
	if err != nil {
		zap.L().Error("snowflake: retry keep-alive failed", zap.Error(err))
		_, _ = etcdClient.Revoke(ctx, lease.ID)
		return false
	}

	readyMu.Lock()
	nodeReady = true
	readyMu.Unlock()

	// 继续消费新的 keep-alive 通道
	for {
		select {
		case <-ctx.Done():
			return true
		case _, ok := <-ch:
			if !ok {
				readyMu.Lock()
				nodeReady = false
				readyMu.Unlock()
				zap.L().Error("snowflake: re-acquired keep-alive lost again, GenID disabled - please restart instance")
				return true
			}
		}
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
