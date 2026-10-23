// Package render 把 Markdown 渲染成带内联样式、可直接提交给公众号的 HTML。
// 结构规则移植自 md2media 的 render.mjs，样式完全由主题 CSS 决定。
package render

import (
	"bytes"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer"
	gmhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/util"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type Renderer struct {
	md     goldmark.Markdown
	themes *Registry
}

func New(themes *Registry) *Renderer {
	md := goldmark.New(
		goldmark.WithExtensions(
			// 微信不认 align 属性，表格对齐直接输出成 text-align
			extension.NewTable(extension.WithTableCellAlignMethod(extension.TableCellAlignStyle)),
			extension.Strikethrough,
			extension.TaskList,
			extension.Linkify,
		),
		goldmark.WithRendererOptions(
			gmhtml.WithHardWraps(), // 与 marked 的 breaks: true 一致
			renderer.WithNodeRenderers(util.Prioritized(nodeRenderer{}, 100)),
		),
	)
	return &Renderer{md: md, themes: themes}
}

func (r *Renderer) Themes() *Registry { return r.themes }

// Render 用指定主题渲染，theme 为空时用默认主题
func (r *Renderer) Render(markdown, theme string) (string, error) {
	s, err := r.themes.Get(theme)
	if err != nil {
		return "", err
	}
	return r.RenderWith(markdown, s)
}

// RenderWith 用给定主题渲染，自定义 CSS 先用 ParseTheme 解析
func (r *Renderer) RenderWith(markdown string, s *Theme) (string, error) {
	var buf bytes.Buffer
	if err := r.md.Convert([]byte(normalizeEmphasis(markdown)), &buf); err != nil {
		return "", err
	}
	nodes, err := html.ParseFragment(&buf, &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body})
	if err != nil {
		return "", err
	}

	root := elem(atom.Section)
	setAttr(root, "id", "nice")
	for _, n := range nodes {
		root.AppendChild(n)
	}
	transform(root)
	inline(root, s)
	cleanup(root)

	var out strings.Builder
	if err := html.Render(&out, root); err != nil {
		return "", err
	}
	// 代码缩进依赖 &nbsp;，写回实体形式，避免编辑器把裸 U+00A0 当普通空格
	return strings.ReplaceAll(out.String(), " ", "&nbsp;"), nil
}

var blockAtoms = map[atom.Atom]bool{
	atom.Ul: true, atom.Ol: true, atom.Li: true, atom.Section: true, atom.P: true,
	atom.Blockquote: true, atom.Pre: true, atom.Table: true,
	atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true,
}

// transform 做与样式无关的结构调整，必须在内联之前执行
func transform(n *html.Node) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		switch {
		case c.Type == html.TextNode:
			// 去掉 normalizeEmphasis 插入的零宽边界
			c.Data = strings.ReplaceAll(c.Data, "‌", "")
		case c.DataAtom == atom.Input && getAttr(c, "type") == "checkbox":
			// 微信会清掉 input，任务列表换成 emoji
			mark := "⬜ "
			if hasAttr(c, "checked") {
				mark = "✅ "
			}
			n.InsertBefore(&html.Node{Type: html.TextNode, Data: mark}, c)
			n.RemoveChild(c)
		case c.DataAtom == atom.Ol:
			if p := flattenSingleItemOl(c); p != nil {
				n.InsertBefore(p, c)
				n.RemoveChild(c)
				c = p
			}
		case headingAtoms[c.DataAtom]:
			wrapHeading(c)
		}
		if c.Type == html.ElementNode {
			transform(c)
			if c.DataAtom == atom.Li {
				wrapListItem(c)
			}
		}
		c = next
	}
}

// flattenSingleItemOl 「1. 标题」后紧跟正文时，每个编号会被拆成只有一项的 <ol>，
// 后续编号靠 start 续上。微信不认 start，编号会错位成「1.」「2.先出分镜」。
// 单项 <ol> 实际是数字小标题，转成编号写死在文字里的加粗段落。
func flattenSingleItemOl(ol *html.Node) *html.Node {
	var li *html.Node
	for c := ol.FirstChild; c != nil; c = c.NextSibling {
		switch {
		case c.DataAtom == atom.Li && li == nil:
			li = c
		case c.Type == html.TextNode && strings.TrimSpace(c.Data) == "":
		default:
			return nil
		}
	}
	if li == nil || hasBlockDescendant(li) {
		return nil
	}
	if f := li.FirstChild; f != nil && f.Type == html.TextNode {
		f.Data = strings.TrimLeft(f.Data, " \t\n")
	}
	if l := li.LastChild; l != nil && l.Type == html.TextNode {
		l.Data = strings.TrimRight(l.Data, " \t\n")
	}

	start := getAttr(ol, "start")
	if start == "" {
		start = "1"
	}
	strong := elem(atom.Strong)
	strong.AppendChild(&html.Node{Type: html.TextNode, Data: start + ". "})
	moveChildren(li, strong)
	p := elem(atom.P)
	p.AppendChild(strong)
	return p
}

// wrapListItem 把纯行内内容的列表项整体包进一个 section，
// 避免公众号把「<strong>标题</strong>：正文」拆成两块换行
func wrapListItem(li *html.Node) {
	for c := li.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && blockAtoms[c.DataAtom] {
			return
		}
	}
	sec := elem(atom.Section)
	setAttr(sec, "style", "margin: 0; line-height: 1.80;")
	moveChildren(li, sec)
	li.AppendChild(sec)
}

var headingAtoms = map[atom.Atom]bool{
	atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true,
}

// wrapHeading 与 mdnice 一致，标题内容包成 prefix/content/suffix 三个 span，
// 移植来的主题靠 h2 .content 之类的选择器做色块、装饰
func wrapHeading(h *html.Node) {
	if h.FirstChild == nil {
		return
	}
	span := func(cls string) *html.Node {
		s := elem(atom.Span)
		setAttr(s, "class", cls)
		return s
	}
	content := span("content")
	moveChildren(h, content)
	h.AppendChild(span("prefix"))
	h.AppendChild(content)
	h.AppendChild(span("suffix"))
}

func hasBlockDescendant(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && (blockAtoms[c.DataAtom] || hasBlockDescendant(c)) {
			return true
		}
	}
	return false
}

// ---------- DOM 工具 ----------

func elem(a atom.Atom) *html.Node {
	return &html.Node{Type: html.ElementNode, Data: a.String(), DataAtom: a}
}

func moveChildren(from, to *html.Node) {
	for c := from.FirstChild; c != nil; {
		next := c.NextSibling
		from.RemoveChild(c)
		to.AppendChild(c)
		c = next
	}
}

func getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}

func setAttr(n *html.Node, key, val string) {
	for i := range n.Attr {
		if n.Attr[i].Key == key {
			n.Attr[i].Val = val
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: val})
}

func removeAttr(n *html.Node, key string) {
	for i, a := range n.Attr {
		if a.Key == key {
			n.Attr = append(n.Attr[:i], n.Attr[i+1:]...)
			return
		}
	}
}
