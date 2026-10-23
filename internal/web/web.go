// Package web 内嵌 API 测试面板的静态文件。
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

// 写作页在浏览器里排版用的渲染器，产物不进 git，构建服务端前先执行 go generate ./internal/web
//go:generate sh -c "GOOS=js GOARCH=wasm go build -trimpath -ldflags='-s -w' -o static/render.wasm ../../cmd/mdwasm && cp \"$(go env GOROOT)/lib/wasm/wasm_exec.js\" static/"

//go:embed static
var static embed.FS

// Handler 提供面板静态资源，挂载在 / 下
func Handler() http.Handler {
	sub, _ := fs.Sub(static, "static")
	fileServer := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Frame-Options", "DENY")
		// 草稿正文全是内联样式，图片来自微信图床；写作页预览允许任意 https 图片。
		// 脚本仍只允许同源（wasm-unsafe-eval 只放开 WebAssembly 编译），正文预览的 iframe 也不允许执行脚本
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob: https: http://*.qpic.cn; "+
			"media-src 'self' blob:; style-src 'self' 'unsafe-inline'; script-src 'self' 'wasm-unsafe-eval'; frame-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		fileServer.ServeHTTP(w, r)
	})
}
