package snowflake

import (
	"context"
	"fmt"
	"log"
	"time"

	sf "github.com/bwmarrin/snowflake"
	clientv3 "go.etcd.io/etcd/client/v3"
)

const (
	// etcd 中存储 node_id 的 key 前缀
	keyPrefix = "/snowflake/nodes/"
	// 最大 node_id（10 bit = 1024 个节点）
	maxNodeID = 1023
	// 租约时长（秒），实例需要定期续约
	leaseTTL = 30
)

var node *sf.Node

// NodeID 当前实例分配到的 node_id
var NodeID int64

// InitWithEtcd 通过 etcd 自动分配唯一的 node_id
// 启动时从 0 遍历到 1023，尝试抢锁，抢到第一个可用的即作为本实例的 node_id
// 同时启动后台协程续约租约，保证实例存活期间 node_id 不会被其他实例抢占
func InitWithEtcd(etcdClient *clientv3.Client, serviceName string) error {
	ctx := context.Background()

	// 遍历 0-1023，找到第一个未被占用或租约已过期的 node_id
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
				return fmt.Errorf("snowflake: create node failed: %w", err)
			}
			node = sfNode
			NodeID = int64(i)

			// 后台续约
			go keepAlive(etcdClient, lease.ID, key)

			log.Printf("[snowflake] allocated node_id=%d for service=%s", i, serviceName)
			return nil
		}

		// 没抢到，继续尝试下一个
		_ = lease
	}

	return fmt.Errorf("snowflake: no available node_id (all 0-%d are occupied)", maxNodeID)
}

// keepAlive 后台续约租约，实例退出或崩溃时租约自动过期，node_id 被释放
func keepAlive(etcdClient *clientv3.Client, leaseID clientv3.LeaseID, key string) {
	ctx := context.Background()

	ch, err := etcdClient.KeepAlive(ctx, leaseID)
	if err != nil {
		log.Printf("[snowflake] keep-alive failed: %v", err)
		return
	}

	// 消费 keep-alive 响应通道
	for {
		select {
		case resp, ok := <-ch:
			if !ok {
				log.Printf("[snowflake] keep-alive channel closed, node_id may be lost")
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
	for {
		time.Sleep(5 * time.Second)

		ctx := context.Background()
		lease, err := etcdClient.Grant(ctx, leaseTTL)
		if err != nil {
			log.Printf("[snowflake] retry grant failed: %v", err)
			continue
		}

		txn := etcdClient.Txn(ctx).
			If(clientv3.Compare(clientv3.CreateRevision(key), "=", 0)).
			Then(clientv3.OpPut(key, "1", clientv3.WithLease(lease.ID)))

		resp, err := txn.Commit()
		if err != nil {
			log.Printf("[snowflake] retry txn failed: %v", err)
			continue
		}

		if resp.Succeeded {
			ch, err := etcdClient.KeepAlive(ctx, lease.ID)
			if err != nil {
				log.Printf("[snowflake] retry keep-alive failed: %v", err)
				continue
			}
			go func() {
				for {
					select {
					case _, ok := <-ch:
						if !ok {
							log.Printf("[snowflake] retry keep-alive closed, retrying...")
							return
						}
					}
				}
			}()
			log.Printf("[snowflake] re-acquired node_id successfully")
			return
		}

		// key 被别人占了，放弃重试（说明本实例可能已经不再持有这个 node_id）
		log.Printf("[snowflake] failed to re-acquire node_id, giving up")
		return
	}
}

// GenID 生成一个雪花 ID
func GenID() int64 {
	return node.Generate().Int64()
}
