package render

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var styleAttrRe = regexp.MustCompile(` style="[^"]*"`)

func newTestRenderer(t *testing.T, dir string) *Renderer {
	t.Helper()
	reg, err := LoadRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	return New(reg)
}

func mustRender(t *testing.T, r *Renderer, md, skin string) string {
	t.Helper()
	out, err := r.Render(md, skin)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSingleItemOlFlattened(t *testing.T) {
	r := newTestRenderer(t, "")
	out := styleAttrRe.ReplaceAllString(mustRender(t, r, "1. 先出分镜\n\n正文一\n\n2. 规格写死\n\n正文二\n", ""), "")
	if strings.Contains(out, "<ol") {
		t.Fatalf("ol not flattened: %s", out)
	}
	for _, want := range []string{"<p><strong>1. 先出分镜</strong></p>", "<p><strong>2. 规格写死</strong></p>"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %s", want, out)
		}
	}
}

func TestMultiItemOlKept(t *testing.T) {
	r := newTestRenderer(t, "")
	out := styleAttrRe.ReplaceAllString(mustRender(t, r, "1. x\n2. y\n", ""), "")
	if !strings.Contains(out, "<ol>") || strings.Count(out, "<li>") != 2 {
		t.Fatalf("multi-item ol changed: %s", out)
	}
}

func TestSkinColorsApplied(t *testing.T) {
	r := newTestRenderer(t, "")
	out := mustRender(t, r, "## 标题\n\n**粗体** `code`\n\n> 引用\n", "")
	for _, want := range []string{"border-left: 4px solid #0891b2", "color: #0891b2; font-weight: bold", "background: #e6f7fb", "background: #eefcff"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Contains(out, "class=") || strings.Contains(out, "<style") {
		t.Fatalf("output must not depend on class/style tags: %s", out)
	}
}

func TestCodeBlockLinesAndHighlight(t *testing.T) {
	r := newTestRenderer(t, "")
	out := mustRender(t, r, "```js\nconst a = 1;\n  if (a) {}\n```\n", "")
	if n := strings.Count(out, `<span style="display: block; color: #d8edf1;">`); n != 2 {
		t.Fatalf("want 2 line spans, got %d: %s", n, out)
	}
	if !strings.Contains(out, "&nbsp;&nbsp;") {
		t.Fatalf("indent not preserved: %s", out)
	}
	if !strings.Contains(out, "color: #8bd7e8") { // keyword
		t.Fatalf("keyword not highlighted: %s", out)
	}
	if strings.Contains(out, "data-hl") {
		t.Fatalf("data-hl leaked: %s", out)
	}
}

func TestEmphasisPunctuation(t *testing.T) {
	r := newTestRenderer(t, "")
	out := mustRender(t, r, "**结论。**这层意思\n", "")
	if strings.Contains(out, "**") || !strings.Contains(out, "结论。</strong>这层") {
		t.Fatalf("emphasis not parsed: %s", out)
	}
	if strings.Contains(out, "‌") || strings.Contains(out, "zwnj") {
		t.Fatalf("zwnj leaked: %s", out)
	}
}

func TestRawHTMLEscaped(t *testing.T) {
	r := newTestRenderer(t, "")
	out := mustRender(t, r, "文字 <script>alert(1)</script>\n", "")
	if strings.Contains(out, "<script") {
		t.Fatalf("raw html not escaped: %s", out)
	}
}

func TestTaskListAndTableAlign(t *testing.T) {
	r := newTestRenderer(t, "")
	out := mustRender(t, r, "- [x] 完成\n- [ ] 待办\n\n| a | b |\n|:-:|--:|\n| 1 | 2 |\n", "")
	if strings.Contains(out, "<input") || !strings.Contains(out, "✅") || !strings.Contains(out, "⬜") {
		t.Fatalf("task list: %s", out)
	}
	// goldmark 输出的对齐样式会和皮肤样式合并后重新序列化
	if !strings.Contains(out, "text-align: center;") || !strings.Contains(out, "text-align: right;") {
		t.Fatalf("table align: %s", out)
	}
}

// 自定义皮肤是一份独立 CSS，可以完全改变结构表现：伪元素、居中标题、背景块等
func TestCustomCSSSkin(t *testing.T) {
	dir := t.TempDir()
	skin := `/* 玫瑰 —— 测试皮肤 */
#nice { color: #333333; }
#nice h1 { text-align: center; background: #be123c; color: #ffffff; padding: 12px; }
#nice h1::before { content: "\201C"; color: #ffd6e0; }
#nice h1::after { content: "”"; }
#nice h2 { color: #be123c }
#nice p strong { color: #be123c; }
#nice .hljs-keyword { color: #ff0000; }
@media (max-width: 600px) { #nice h1 { color: #000000; } }
`
	os.WriteFile(filepath.Join(dir, "rose.css"), []byte(skin), 0o644)
	r := newTestRenderer(t, dir)
	out := mustRender(t, r, "# 标题\n\n## 二级\n\n**x**\n\n```js\nconst a = 1;\n```\n", "rose")
	for _, want := range []string{
		"background: #be123c; color: #ffffff; padding: 12px;",
		`<span style="color: #ffd6e0;">“</span>标题<span>”</span>`,
		`<h2 style="color: #be123c;">`, // 末尾无分号的声明也要生效
		`<strong style="color: #be123c;">`,
		`<span style="color: #ff0000;">const</span>`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %s", want, out)
		}
	}
	if strings.Contains(out, "#0891b2") || strings.Contains(out, "class=") || strings.Contains(out, `id="nice"`) {
		t.Fatalf("default skin leaked or helper attrs kept: %s", out)
	}
	s, _ := r.Skins().Get("rose")
	if s.Label != "玫瑰" || len(s.Warnings) == 0 {
		t.Fatalf("label/warnings: %+v", s)
	}
	if _, err := r.Render("x", "nope"); err == nil {
		t.Fatal("unknown skin should error")
	}
}

func TestSpecificityAndImportant(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "spec.css"), []byte(`
#nice blockquote p { color: #111111; }
#nice p { color: #222222; }
#nice p { font-size: 15px !important; }
#nice p { font-size: 20px; }
`), 0o644)
	r := newTestRenderer(t, dir)
	out := mustRender(t, r, "> 引用\n\n正文\n", "spec")
	if !strings.Contains(out, `<p style="color: #111111; font-size: 15px !important;">引用`) {
		t.Fatalf("specificity in quote: %s", out)
	}
	if !strings.Contains(out, `<p style="color: #222222; font-size: 15px !important;">正文`) {
		t.Fatalf("important: %s", out)
	}
}

func TestSkinRejectsScriptValues(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "bad.css"), []byte(`#nice a { background: url("javascript:alert(1)"); color: #000000; }`), 0o644)
	r := newTestRenderer(t, dir)
	out := mustRender(t, r, "[x](https://a.b)\n", "bad")
	if strings.Contains(out, "javascript") {
		t.Fatalf("script value kept: %s", out)
	}
}

// 微信会清掉 <code> 的 style，pre 带 font-family 时整条 style 也会被清掉，
// 所以代码文字色必须落在每行 span 上，pre 只保留背景和布局
func TestCodeTextColorSurvivesCodeStyleStrip(t *testing.T) {
	r := newTestRenderer(t, "")
	out := mustRender(t, r, "```js\nconst a = 1;\n```\n", "")
	if !strings.Contains(out, `<span style="display: block; color: #d8edf1;">`) {
		t.Fatalf("code line color missing: %s", out)
	}
	pre := out[strings.Index(out, "<pre"):]
	pre = pre[:strings.Index(pre, ">")]
	if !strings.Contains(pre, "background: #18343d") || strings.Contains(pre, "font-family") {
		t.Fatalf("pre must keep background and avoid font-family: %s", pre)
	}
}

// mdnice 主题靠 h2 .content 做色块；「;;」这类空声明不能让皮肤加载失败
func TestHeadingContentSpanAndEmptyDecl(t *testing.T) {
	dir := t.TempDir()
	skin := `/* 块 */
#nice h2 .content { background: #ef7060;; color: #ffffff; }
#nice h2 .prefix { display: none; }
`
	os.WriteFile(filepath.Join(dir, "block.css"), []byte(skin), 0o644)
	r := newTestRenderer(t, dir)
	out := mustRender(t, r, "## 标题\n\n### 三级\n", "block")
	if !strings.Contains(out, `<h2><span style="background: #ef7060; color: #ffffff;">标题</span></h2>`) {
		t.Fatalf("content span not styled or prefix kept: %s", out)
	}
	if !strings.Contains(out, "<h3>三级</h3>") {
		t.Fatalf("unstyled content span should be unwrapped: %s", out)
	}
}

func TestBuiltinSkinsLoadClean(t *testing.T) {
	r := newTestRenderer(t, "")
	for _, s := range r.Skins().List() {
		out := mustRender(t, r, "# 一\n\n## 二\n\n> 引用\n\n```go\nx := 1\n```\n", s.Name)
		if strings.Contains(out, "class=") {
			t.Fatalf("skin %s leaked class: %s", s.Name, out)
		}
	}
}
