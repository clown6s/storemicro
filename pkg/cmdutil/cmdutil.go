package cmdutil

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// UnquoteValue 剥离配对的引号（"value" 或 'value'），不配对时返回原值
// 注意：strings.Trim 按字符集剥离，"he'llo 会被错误剥掉两端的 " 和 '
func UnquoteValue(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// LoadDotenv 自动加载 .env 文件（配置目录 → 当前目录 → 上级目录），不覆盖已有环境变量
// 仅加载合法的环境变量名（字母/数字/下划线），防止 .env 被篡改后设置 PATH 等系统变量
var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func LoadDotenv(configFile string) {
	candidates := []string{".env"}
	if configFile != "" {
		if info, err := os.Stat(configFile); err == nil && info.IsDir() {
			candidates = append([]string{filepath.Join(configFile, ".env")}, candidates...)
		} else {
			candidates = append([]string{filepath.Join(filepath.Dir(configFile), ".env")}, candidates...)
		}
	}
	for _, p := range candidates {
		if data, err := os.ReadFile(p); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				key, val, ok := strings.Cut(line, "=")
				if !ok {
					continue
				}
				key = strings.TrimSpace(key)
				if !envKeyPattern.MatchString(key) {
					continue // 跳过非法环境变量名
				}
				val = strings.TrimSpace(val)
				val = UnquoteValue(val)
				if os.Getenv(key) == "" {
					os.Setenv(key, val)
				}
			}
			return
		}
	}
}

// MetricsBearerAuth 包装 handler，要求 Authorization header 中带 "Bearer {password}" 才放行（timing-safe 比较）
func MetricsBearerAuth(next http.Handler, password string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":401,"message":"unauthorized"}`))
			return
		}
		token := strings.TrimPrefix(auth, "Bearer ")
		if subtle.ConstantTimeCompare([]byte(token), []byte(password)) != 1 {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":401,"message":"unauthorized"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ResolveConfDir 查找配置目录（可执行文件旁 → 上级 → 当前目录下）
// exeErr 用于记录 os.Executable 的错误（在 init 阶段 zap 尚未初始化时可用 fmt.Fprintln）
func ResolveConfDir() string {
	exe, exeErr := os.Executable()
	exeDir := "."
	if exeErr != nil {
		// os.Executable 失败时使用当前目录作为 fallback，并输出警告
		fmt.Fprintln(os.Stderr, "[WARN] os.Executable() failed, using current directory:", exeErr)
	} else {
		exeDir = filepath.Dir(exe)
	}
	candidates := []string{
		filepath.Join(exeDir, "configs"),
		filepath.Join(exeDir, "..", "configs"),
		filepath.Join("configs"),
	}
	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			return p
		}
	}
	// 未找到任何配置目录，返回空字符串让调用方报错
	return ""
}
