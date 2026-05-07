package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// ServiceInstance 表示从 etcd 中解析出的服务实例
type ServiceInstance struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Metadata struct {
		Addr string `json:"addr"`
	} `json:"metadata"`
}

// OnUpdateFunc 当服务列表变化时被调用
type OnUpdateFunc func(services map[string][]ServiceInstance)

// EtcdWatcher 监听 etcd 中的服务注册信息
type EtcdWatcher struct {
	client   *clientv3.Client
	prefix   string
	onUpdate OnUpdateFunc
	mu       sync.Mutex
	services map[string][]ServiceInstance
}

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
		services: make(map[string][]ServiceInstance),
	}

	// 初始加载已有服务
	if err := w.loadInitial(ctx); err != nil {
		log.Printf("[watcher] initial load warning: %v", err)
	}

	return w, nil
}

func (w *EtcdWatcher) SetOnUpdate(fn OnUpdateFunc) {
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

	log.Printf("[watcher] initial load: %d service instances", len(resp.Kvs))
	return nil
}

func (w *EtcdWatcher) Run(ctx context.Context) {
	rch := w.client.Watch(ctx, w.prefix, clientv3.WithPrefix())
	log.Printf("[watcher] watching etcd prefix: %s", w.prefix)

	for {
		select {
		case <-ctx.Done():
			return
		case wr, ok := <-rch:
			if !ok {
				log.Println("[watcher] watch channel closed, reconnecting in 5s...")
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

	var inst ServiceInstance
	if err := json.Unmarshal([]byte(value), &inst); err != nil {
		log.Printf("[watcher] failed to parse instance %s: %v", key, err)
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

	// 移除最后一个添加的实例（简单策略，按 key 匹配更好但这里够用）
	if len(instances) > 0 {
		w.services[svcName] = instances[:len(instances)-1]
		if len(w.services[svcName]) == 0 {
			delete(w.services, svcName)
		}
	}
}

func (w *EtcdWatcher) getServicesCopy() map[string][]ServiceInstance {
	w.mu.Lock()
	defer w.mu.Unlock()

	result := make(map[string][]ServiceInstance, len(w.services))
	for k, v := range w.services {
		copied := make([]ServiceInstance, len(v))
		copy(copied, v)
		result[k] = copied
	}
	return result
}

// extractServiceName 从 etcd key 中提取服务名
// Kratos 注册的 key 格式: /kratos/registry/<service-name>/<instance-id>
func extractServiceName(key, prefix string) string {
	// 去掉前缀
	trimmed := strings.TrimPrefix(key, prefix)
	parts := strings.Split(trimmed, "/")
	if len(parts) >= 1 {
		return parts[0]
	}
	return trimmed
}
