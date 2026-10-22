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
		// 草稿正文全是内联样式，图片来自微信图床；脚本仍只允许同源，正文预览的 iframe 也不允许执行脚本
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob: https://*.qpic.cn http://*.qpic.cn; "+
			"media-src 'self' blob:; style-src 'self' 'unsafe-inline'; script-src 'self'; frame-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		fileServer.ServeHTTP(w, r)
	})
}
