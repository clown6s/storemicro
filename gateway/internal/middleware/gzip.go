package middleware

import (
	"compress/gzip"
	"net/http"
	"strings"
	"sync"
)

// gzipPool 复用 gzip.Writer 减少内存分配
var gzipPool = sync.Pool{
	New: func() any {
		// 默认压缩级别 6（速度和压缩率平衡）
		w, _ := gzip.NewWriterLevel(nil, gzip.DefaultCompression)
		return w
	},
}

// minCompressSize 小于此大小的响应不压缩（压缩收益低，反而增加 CPU 开销）
const minCompressSize = 1024 // 1KB

// Gzip gzip 压缩中间件
func Gzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 检查客户端是否接受 gzip
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}

		// 不压缩 WebSocket、已压缩的响应
		if strings.Contains(r.Header.Get("Connection"), "Upgrade") {
			next.ServeHTTP(w, r)
			return
		}

		gz := gzipPool.Get().(*gzip.Writer)
		gz.Reset(w)
		defer func() {
			gz.Close()
			gzipPool.Put(gz)
		}()

		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Del("Content-Length") // 压缩后长度会变，删除原始长度

		gw := &gzipResponseWriter{ResponseWriter: w, Writer: gz}
		next.ServeHTTP(gw, r)
	})
}

// gzipResponseWriter 包装 ResponseWriter，拦截 Write 写入 gzip
type gzipResponseWriter struct {
	http.ResponseWriter
	http.Flusher
	Writer *gzip.Writer
}

func (gw *gzipResponseWriter) Write(b []byte) (int, error) {
	return gw.Writer.Write(b)
}

func (gw *gzipResponseWriter) Flush() {
	if gw.Flusher == nil {
		if f, ok := gw.ResponseWriter.(http.Flusher); ok {
			gw.Flusher = f
		} else {
			return
		}
	}
	_ = gw.Writer.Flush()
	gw.Flusher.Flush()
}
