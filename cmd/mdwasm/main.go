//go:build js && wasm

// mdwasm 把渲染器编译成 WebAssembly，供面板「写作」页在浏览器里排版，
// 不需要调用需要 token 的 /api/v1/render。输出与服务端完全一致。
//
// 构建：go generate ./internal/web
package main

import (
	"encoding/json"
	"syscall/js"

	"wx-bot/internal/render"
)

func main() {
	skins, err := render.LoadRegistry("")
	if err != nil {
		js.Global().Set("wxRenderError", err.Error())
		return
	}
	r := render.New(skins)

	// wxSkins() → JSON 字符串 {"default":"...","skins":[{name,label}]}
	js.Global().Set("wxSkins", js.FuncOf(func(js.Value, []js.Value) any {
		type item struct {
			Name  string `json:"name"`
			Label string `json:"label"`
		}
		list := []item{}
		for _, s := range skins.List() {
			list = append(list, item{s.Name, s.Label})
		}
		data, _ := json.Marshal(map[string]any{"default": render.DefaultSkin, "skins": list})
		return string(data)
	}))

	// wxRender(markdown, skin) → {html} 或 {error}
	js.Global().Set("wxRender", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) < 2 {
			return map[string]any{"error": "wxRender(markdown, skin)"}
		}
		out, err := r.Render(args[0].String(), args[1].String())
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		return map[string]any{"html": out}
	}))

	js.Global().Call("dispatchEvent", js.Global().Get("Event").New("wxrender-ready"))
	select {}
}
