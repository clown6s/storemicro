package types

// ServiceInstance 表示从 etcd 中解析出的服务实例
type ServiceInstance struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Metadata struct {
		Addr string `json:"addr"`
	} `json:"metadata"`
}

// OnUpdateFunc 当服务列表变化时被调用的回调函数
type OnUpdateFunc func(services map[string][]ServiceInstance)
