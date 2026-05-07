package watcher

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"

	"control-plane/internal/types"
)

// EtcdWatcher 监听 etcd 中的服务注册信息
type EtcdWatcher struct {
	client   *clientv3.Client
	prefix   string
	onUpdate types.OnUpdateFunc
	mu       sync.Mutex
	services map[string][]types.ServiceInstance
	logger   *zap.Logger
}

// NewEtcdWatcher 创建 etcd watcher
func NewEtcdWatcher(ctx context.Context, endpoints []string, prefix string) (*EtcdWatcher, error) {
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("connect etcd: %w", err)
	}

	w := &EtcdWatcher{
		client:   client,
		prefix:   prefix,
		services: make(map[string][]types.ServiceInstance),
		logger:   zap.L().Named("watcher"),
	}

	if err := w.loadInitial(ctx); err != nil {
		w.logger.Warn("initial load warning", zap.Error(err))
	}

	return w, nil
}

// SetOnUpdate 设置服务变更回调
func (w *EtcdWatcher) SetOnUpdate(fn types.OnUpdateFunc) {
	w.onUpdate = fn
}

func (w *EtcdWatcher) loadInitial(ctx context.Context) error {
	resp, err := w.client.Get(ctx, w.prefix, clientv3.WithPrefix())
	if err != nil {
		return err
	}

	for _, kv := range resp.Kvs {
		w.parseAndAdd(string(kv.Key), string(kv.Value))
	}

	if w.onUpdate != nil {
		w.onUpdate(w.getServicesCopy())
	}

	w.logger.Info("initial load completed", zap.Int("instances", len(resp.Kvs)))
	return nil
}

// Run 启动 watcher 监听循环
func (w *EtcdWatcher) Run(ctx context.Context) {
	rch := w.client.Watch(ctx, w.prefix, clientv3.WithPrefix())
	w.logger.Info("watching etcd prefix", zap.String("prefix", w.prefix))

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("watcher stopped")
			return
		case wr, ok := <-rch:
			if !ok {
				w.logger.Warn("watch channel closed, reconnecting in 5s")
				time.Sleep(5 * time.Second)
				go w.Run(ctx)
				return
			}

			changed := false
			for _, ev := range wr.Events {
				key := string(ev.Kv.Key)
				switch ev.Type {
				case clientv3.EventTypePut:
					w.parseAndAdd(key, string(ev.Kv.Value))
					changed = true
				case clientv3.EventTypeDelete:
					w.removeInstance(key)
					changed = true
				}
			}

			if changed && w.onUpdate != nil {
				w.onUpdate(w.getServicesCopy())
			}
		}
	}
}

func (w *EtcdWatcher) parseAndAdd(key, value string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	var inst types.ServiceInstance
	if err := json.Unmarshal([]byte(value), &inst); err != nil {
		w.logger.Error("failed to parse instance", zap.String("key", key), zap.Error(err))
		return
	}

	svcName := extractServiceName(key, w.prefix)
	w.services[svcName] = append(w.services[svcName], inst)
}

func (w *EtcdWatcher) removeInstance(key string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	svcName := extractServiceName(key, w.prefix)
	instances, ok := w.services[svcName]
	if !ok {
		return
	}

	if len(instances) > 0 {
		w.services[svcName] = instances[:len(instances)-1]
		if len(w.services[svcName]) == 0 {
			delete(w.services, svcName)
		}
	}
}

func (w *EtcdWatcher) getServicesCopy() map[string][]types.ServiceInstance {
	w.mu.Lock()
	defer w.mu.Unlock()

	result := make(map[string][]types.ServiceInstance, len(w.services))
	for k, v := range w.services {
		copied := make([]types.ServiceInstance, len(v))
		copy(copied, v)
		result[k] = copied
	}
	return result
}

// extractServiceName 从 etcd key 中提取服务名
// Kratos 注册的 key 格式: /kratos/registry/<service-name>/<instance-id>
func extractServiceName(key, prefix string) string {
	trimmed := strings.TrimPrefix(key, prefix)
	parts := strings.Split(trimmed, "/")
	if len(parts) >= 1 {
		return parts[0]
	}
	return trimmed
}
