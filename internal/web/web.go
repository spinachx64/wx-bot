// Package web 内嵌 API 测试面板的静态文件。
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
)

// 写作页在浏览器里排版用的渲染器，产物不进 git，构建服务端前先执行 go generate ./internal/web
//go:generate sh -c "GOOS=js GOARCH=wasm go build -trimpath -ldflags='-s -w' -o static/render.wasm ../../cmd/mdwasm && cp \"$(go env GOROOT)/lib/wasm/wasm_exec.js\" static/"

//go:embed static
var static embed.FS

// etags 按内容 hash 生成 ETag。embed 文件没有修改时间，不设 ETag 时浏览器无法协商缓存，
// 每次都会重新下载完整的 render.wasm
func etags(fsys fs.FS) map[string]string {
	out := map[string]string{}
	fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		out["/"+p] = `"` + hex.EncodeToString(sum[:8]) + `"`
		return nil
	})
	out["/"] = out["/index.html"]
	return out
}

// Handler 提供面板静态资源，挂载在 / 下
func Handler() http.Handler {
	sub, _ := fs.Sub(static, "static")
	fileServer := http.FileServerFS(sub)
	tags := etags(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// no-cache 仍可缓存，只是每次用 ETag 协商；内容没变时返回 304，不重新下载
		w.Header().Set("Cache-Control", "no-cache")
		if tag, ok := tags[r.URL.Path]; ok {
			w.Header().Set("ETag", tag)
		}
		w.Header().Set("X-Frame-Options", "DENY")
		// 草稿正文全是内联样式，图片来自微信图床；写作页预览允许任意 https 图片。
		// 脚本仍只允许同源（wasm-unsafe-eval 只放开 WebAssembly 编译），正文预览的 iframe 也不允许执行脚本
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob: https: http://*.qpic.cn; "+
			"media-src 'self' blob:; style-src 'self' 'unsafe-inline'; script-src 'self' 'wasm-unsafe-eval'; frame-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		fileServer.ServeHTTP(w, r)
	})
}
