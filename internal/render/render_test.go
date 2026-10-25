package render

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
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

func mustRender(t *testing.T, r *Renderer, md, theme string) string {
	t.Helper()
	out, err := r.Render(md, theme)
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

func TestListsHaveOnlyItemChildren(t *testing.T) {
	r := newTestRenderer(t, "")
	for _, tc := range []struct {
		name, markdown string
		items          []int
	}{
		{"ordered", "1. Anthropic\n2. OpenAI\n3. Hugging Face\n4. 阿里\n\n正文\n\n1. 规范\n2. 安全\n3. 评估\n", []int{4, 3}},
		{"unordered", "- 第一项\n- 第二项\n", []int{2}},
		{"nested", "1. 第一项\n   - 子项一\n   - 子项二\n2. 第二项\n", []int{2, 2}},
		{"loose", "1. 第一段\n\n   第二段\n\n2. 第二项\n", []int{2}},
		{"tasks", "- [x] 已完成\n- [ ] 待完成\n", []int{2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := mustRender(t, r, tc.markdown, "")
			doc, err := html.Parse(strings.NewReader(out))
			if err != nil {
				t.Fatal(err)
			}
			var lists int
			var walk func(*html.Node)
			walk = func(n *html.Node) {
				if n.DataAtom == atom.Ol || n.DataAtom == atom.Ul {
					var count int
					for c := n.FirstChild; c != nil; c = c.NextSibling {
						// 微信编辑器会把列表内的空白文本节点也当作列表项。
						if c.Type != html.ElementNode || c.DataAtom != atom.Li {
							t.Fatalf("non-item child in list: type=%v data=%q\n%s", c.Type, c.Data, out)
						}
						count++
					}
					if lists >= len(tc.items) || count != tc.items[lists] {
						t.Fatalf("list %d has %d items, want %v: %s", lists, count, tc.items, out)
					}
					lists++
				}
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
			}
			walk(doc)
			if lists != len(tc.items) {
				t.Fatalf("got %d lists, want %d: %s", lists, len(tc.items), out)
			}
		})
	}
}

func TestListCleanupPreservesContentWhitespace(t *testing.T) {
	doc, err := html.Parse(strings.NewReader("<ol> \t\r\n<li><strong>first</strong> <em>second</em><pre>  code\n  next</pre></li> \n<li>third</li>\t </ol>"))
	if err != nil {
		t.Fatal(err)
	}
	cleanup(doc)
	var out strings.Builder
	if err := html.Render(&out, doc); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "<ol><li>") || !strings.Contains(out.String(), "</li><li>third</li></ol>") {
		t.Fatalf("list whitespace not removed: %s", out.String())
	}
	for _, want := range []string{"</strong> <em>", "<pre>  code\n  next</pre>"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("content whitespace lost: want %q in %s", want, out.String())
		}
	}
}

func TestThemeColorsApplied(t *testing.T) {
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
	// goldmark 输出的对齐样式会和主题样式合并后重新序列化
	if !strings.Contains(out, "text-align: center;") || !strings.Contains(out, "text-align: right;") {
		t.Fatalf("table align: %s", out)
	}
}

// 自定义主题是一份独立 CSS，可以完全改变结构表现：伪元素、居中标题、背景块等
func TestCustomCSSTheme(t *testing.T) {
	dir := t.TempDir()
	theme := `/* 玫瑰 —— 测试主题 */
#nice { color: #333333; }
#nice h1 { text-align: center; background: #be123c; color: #ffffff; padding: 12px; }
#nice h1::before { content: "\201C"; color: #ffd6e0; }
#nice h1::after { content: "”"; }
#nice h2 { color: #be123c }
#nice p strong { color: #be123c; }
#nice .hljs-keyword { color: #ff0000; }
@media (max-width: 600px) { #nice h1 { color: #000000; } }
`
	os.WriteFile(filepath.Join(dir, "rose.css"), []byte(theme), 0o644)
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
		t.Fatalf("default theme leaked or helper attrs kept: %s", out)
	}
	s, _ := r.Themes().Get("rose")
	if s.Label != "玫瑰" || len(s.Warnings) == 0 {
		t.Fatalf("label/warnings: %+v", s)
	}
	if _, err := r.Render("x", "nope"); err == nil {
		t.Fatal("unknown theme should error")
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

func TestThemeRejectsScriptValues(t *testing.T) {
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

// mdnice 主题靠 h2 .content 做色块；「;;」这类空声明不能让主题加载失败
func TestHeadingContentSpanAndEmptyDecl(t *testing.T) {
	dir := t.TempDir()
	theme := `/* 块 */
#nice h2 .content { background: #ef7060;; color: #ffffff; }
#nice h2 .prefix { display: none; }
`
	os.WriteFile(filepath.Join(dir, "block.css"), []byte(theme), 0o644)
	r := newTestRenderer(t, dir)
	out := mustRender(t, r, "## 标题\n\n### 三级\n", "block")
	if !strings.Contains(out, `<h2><span style="background: #ef7060; color: #ffffff;">标题</span></h2>`) {
		t.Fatalf("content span not styled or prefix kept: %s", out)
	}
	if !strings.Contains(out, "<h3>三级</h3>") {
		t.Fatalf("unstyled content span should be unwrapped: %s", out)
	}
}

func TestBuiltinThemesLoadClean(t *testing.T) {
	r := newTestRenderer(t, "")
	for _, s := range r.Themes().List() {
		out := mustRender(t, r, "# 一\n\n## 二\n\n> 引用\n\n```go\nx := 1\n```\n", s.Name)
		if strings.Contains(out, "class=") {
			t.Fatalf("theme %s leaked class: %s", s.Name, out)
		}
	}
}
