// Package web 内嵌 API 测试面板的静态文件。
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var static embed.FS

// Handler 提供面板静态资源，挂载在 / 下
func Handler() http.Handler {
	sub, _ := fs.Sub(static, "static")
	fileServer := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; style-src 'self'; script-src 'self'")
		fileServer.ServeHTTP(w, r)
	})
}
