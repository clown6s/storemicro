package envexpand

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/go-kratos/kratos/v2/config"
)

var _ config.Source = (*envFile)(nil)

// envPattern 匹配 ${VAR:default}、${VAR} 和 $VAR 三种语法
// 使用单次正则匹配，避免 os.ExpandEnv 双重展开导致密码含 $ 时被截断
var envPattern = regexp.MustCompile(`\$\{(\w+):([^}]*)\}|\$\{(\w+)\}|\$(\w+)`)

// expandEnv 支持 ${VAR:default}、${VAR} 和 $VAR 语法
func expandEnv(s string) string {
	return envPattern.ReplaceAllStringFunc(s, func(match string) string {
		sub := envPattern.FindStringSubmatch(match)
		if sub[1] != "" {
			// ${VAR:default} — 有默认值
			if val, ok := os.LookupEnv(sub[1]); ok {
				return val
			}
			return sub[2] // 返回默认值
		}
		// ${VAR} 或 $VAR — 无默认值
		varName := sub[3]
		if varName == "" {
			varName = sub[4]
		}
		if val, ok := os.LookupEnv(varName); ok {
			return val
		}
		return match // 环境变量不存在时保留原文
	})
}

type envFile struct {
	path string
}

// NewSource 创建一个在加载时展开环境变量的文件配置源。
// 支持 ${VAR:default} 语法（找不到环境变量时使用默认值），
// 以及标准 ${VAR} / $VAR 语法。
func NewSource(path string) config.Source {
	return &envFile{path: path}
}

func (f *envFile) loadFile(path string) (*config.KeyValue, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	// 展开 YAML 中的环境变量引用
	expanded := expandEnv(string(data))
	return &config.KeyValue{
		Key:    info.Name(),
		Format: format(info.Name()),
		Value:  []byte(expanded),
	}, nil
}

func (f *envFile) loadDir(path string) (kvs []*config.KeyValue, err error) {
	files, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		if file.IsDir() || strings.HasPrefix(file.Name(), ".") {
			continue
		}
		kv, err := f.loadFile(filepath.Join(path, file.Name()))
		if err != nil {
			return nil, err
		}
		kvs = append(kvs, kv)
	}
	return
}

func (f *envFile) Load() (kvs []*config.KeyValue, err error) {
	fi, err := os.Stat(f.path)
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		return f.loadDir(f.path)
	}
	kv, err := f.loadFile(f.path)
	if err != nil {
		return nil, err
	}
	return []*config.KeyValue{kv}, nil
}

func (f *envFile) Watch() (config.Watcher, error) {
	return &noopWatcher{}, nil
}

// noopWatcher 不监听文件变更，Next 立即返回 context.Canceled 让 Kratos 退出 watch 循环
type noopWatcher struct{}

func (w *noopWatcher) Next() ([]*config.KeyValue, error) {
	return nil, context.Canceled
}

func (w *noopWatcher) Stop() error { return nil }

// format 根据文件扩展名推断配置格式（与 Kratos file 包一致）
func format(name string) string {
	ext := filepath.Ext(name)
	switch ext {
	case ".yaml", ".yml":
		return "yaml"
	case ".json":
		return "json"
	case ".toml":
		return "toml"
	default:
		if ext == "" {
			return ""
		}
		return ext[1:]
	}
}
