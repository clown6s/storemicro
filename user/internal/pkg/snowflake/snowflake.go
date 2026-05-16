package snowflake

import (
	"context"
	"fmt"
	"strconv"
	"strings"
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
// 先批量 GET 已占用的节点，再在空闲节点中顺序尝试抢锁，避免逐个 Grant+Revoke 浪费
func InitWithEtcd(etcdClient *clientv3.Client, serviceName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 批量获取已占用的节点 key，确定空闲节点列表
	keyPrefix := fmt.Sprintf("%s%s/", keyPrefix, serviceName)
	getResp, err := etcdClient.Get(ctx, keyPrefix, clientv3.WithPrefix())
	if err != nil {
		return fmt.Errorf("snowflake: get existing nodes failed: %w", err)
	}

	occupied := make(map[int]bool, len(getResp.Kvs))
	for _, kv := range getResp.Kvs {
		// key 格式: /snowflake/nodes/{service}/{nodeId}
		parts := strings.Split(string(kv.Key), "/")
		if len(parts) > 0 {
			if nodeID, err := strconv.Atoi(parts[len(parts)-1]); err == nil && nodeID >= 0 && nodeID <= maxNodeID {
				occupied[nodeID] = true
			}
		}
	}

	// 优先尝试空闲节点
	for i := 0; i <= maxNodeID; i++ {
		if occupied[i] {
			continue
		}
		acquired, _, err := tryAcquireNode(ctx, etcdClient, serviceName, i)
		if err != nil {
			return err
		}
		if acquired {
			return nil
		}
	}

	// 所有标记为空闲的节点都抢锁失败（竞态），退回到全量扫描兜底
	zap.L().Warn("snowflake: all presumed-free nodes occupied by race, falling back to full scan")
	for i := 0; i <= maxNodeID; i++ {
		acquired, _, err := tryAcquireNode(ctx, etcdClient, serviceName, i)
		if err != nil {
			return err
		}
		if acquired {
			return nil
		}
	}

	return fmt.Errorf("snowflake: no available node_id (all 0-%d are occupied)", maxNodeID)
}

// tryAcquireNode 尝试为指定 node_id 抢锁（创建 lease + 事务写入）
// 返回 (是否成功, leaseID, error)
func tryAcquireNode(ctx context.Context, etcdClient *clientv3.Client, serviceName string, nodeID int) (bool, clientv3.LeaseID, error) {
	key := fmt.Sprintf("%s%s/%d", keyPrefix, serviceName, nodeID)

	lease, err := etcdClient.Grant(ctx, leaseTTL)
	if err != nil {
		return false, 0, fmt.Errorf("snowflake: create lease failed: %w", err)
	}

	txn := etcdClient.Txn(ctx).
		If(clientv3.Compare(clientv3.CreateRevision(key), "=", 0)).
		Then(clientv3.OpPut(key, "1", clientv3.WithLease(lease.ID)))

	resp, err := txn.Commit()
	if err != nil {
		_, _ = etcdClient.Revoke(ctx, lease.ID)
		return false, 0, fmt.Errorf("snowflake: txn commit failed: %w", err)
	}

	if resp.Succeeded {
		var sfNode *sf.Node
		sfNode, err = sf.NewNode(int64(nodeID))
		if err != nil {
			_, _ = etcdClient.Revoke(ctx, lease.ID)
			return false, 0, fmt.Errorf("snowflake: create node failed: %w", err)
		}
		readyMu.Lock()
		node = sfNode
		NodeID = int64(nodeID)
		nodeReady = true
		readyMu.Unlock()

		go keepAlive(etcdClient, lease.ID, key)

		zap.L().Info("snowflake: allocated node_id",
			zap.Int("node_id", nodeID),
			zap.String("service", serviceName),
		)
		return true, lease.ID, nil
	}

	// 没抢到，释放不再需要的 lease
	_, _ = etcdClient.Revoke(ctx, lease.ID)
	return false, 0, nil
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

// retryKeepAliveOnce 租约丢失后尝试重新抢锁（多次重试 + 指数退避，在同一 goroutine 中执行）
func retryKeepAliveOnce(etcdClient *clientv3.Client, key string) bool {
	ctx := shutdownCtx

	select {
	case <-ctx.Done():
		return false
	default:
	}

	const maxAttempts = 3
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return false
		default:
		}

		// 指数退避等待（2s, 6s）
		if attempt > 1 {
			wait := time.Duration(attempt*attempt) * time.Second
			timer := time.NewTimer(wait)
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
		}

		lease, err := etcdClient.Grant(ctx, leaseTTL)
		if err != nil {
			zap.L().Error("snowflake: retry grant failed", zap.Int("attempt", attempt), zap.Int("max", maxAttempts), zap.Error(err))
			continue
		}

		txn := etcdClient.Txn(ctx).
			If(clientv3.Compare(clientv3.CreateRevision(key), "=", 0)).
			Then(clientv3.OpPut(key, "1", clientv3.WithLease(lease.ID)))

		resp, err := txn.Commit()
		if err != nil {
			zap.L().Error("snowflake: retry txn failed", zap.Int("attempt", attempt), zap.Int("max", maxAttempts), zap.Error(err))
			_, _ = etcdClient.Revoke(ctx, lease.ID)
			continue
		}

		if !resp.Succeeded {
			_, _ = etcdClient.Revoke(ctx, lease.ID)
			zap.L().Error("snowflake: failed to re-acquire node_id", zap.Int("attempt", attempt), zap.Int("max", maxAttempts))
			continue
		}

		ch, err := etcdClient.KeepAlive(ctx, lease.ID)
		if err != nil {
			zap.L().Error("snowflake: retry keep-alive failed", zap.Int("attempt", attempt), zap.Int("max", maxAttempts), zap.Error(err))
			_, _ = etcdClient.Revoke(ctx, lease.ID)
			continue
		}

		readyMu.Lock()
		// 重置 node 引用，防止时钟漂移后使用陈旧节点生成碰撞 ID
		newNode, nodeErr := sf.NewNode(NodeID)
		if nodeErr != nil {
			zap.L().Error("snowflake: failed to recreate node after lease re-acquire", zap.Error(nodeErr))
			readyMu.Unlock()
			_, _ = etcdClient.Revoke(ctx, lease.ID)
			continue
		}
		node = newNode
		nodeReady = true
		readyMu.Unlock()

		zap.L().Info("snowflake: successfully re-acquired lease", zap.Int("attempt", attempt))

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

	zap.L().Error("snowflake: all re-acquire attempts exhausted, GenID permanently disabled - please restart instance")
	return false
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
