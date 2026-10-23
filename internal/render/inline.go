package render

import (
	"sort"
	"strconv"
	"strings"

	"github.com/aymerick/douceur/parser"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type decl struct {
	prop, val string
	important bool
}

// styleSet 按首次出现顺序保存属性，后写覆盖前写；!important 只能被 !important 覆盖
type styleSet struct {
	list []decl
	idx  map[string]int
}

func (s *styleSet) set(d decl) {
	if s.idx == nil {
		s.idx = map[string]int{}
	}
	if i, ok := s.idx[d.prop]; ok {
		if s.list[i].important && !d.important {
			return
		}
		s.list[i] = d
		return
	}
	s.idx[d.prop] = len(s.list)
	s.list = append(s.list, d)
}

func (s *styleSet) get(prop string) string {
	if i, ok := s.idx[prop]; ok {
		return s.list[i].val
	}
	return ""
}

func (s *styleSet) del(prop string) {
	i, ok := s.idx[prop]
	if !ok {
		return
	}
	s.list = append(s.list[:i], s.list[i+1:]...)
	s.idx = map[string]int{}
	for j, d := range s.list {
		s.idx[d.prop] = j
	}
}

func (s *styleSet) String() string {
	var b strings.Builder
	for _, d := range s.list {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(d.prop + ": " + d.val)
		if d.important {
			b.WriteString(" !important")
		}
		b.WriteByte(';')
	}
	return b.String()
}

func parseStyle(text string) *styleSet {
	s := &styleSet{}
	if strings.TrimSpace(text) == "" {
		return s
	}
	// douceur 会丢掉末尾没有分号的那条声明，补一个
	if !strings.HasSuffix(strings.TrimSpace(text), ";") {
		text += ";"
	}
	decls, _ := parser.ParseDeclarations(text)
	for _, d := range decls {
		s.set(decl{prop: strings.ToLower(d.Property), val: d.Value, important: d.Important})
	}
	return s
}

func writeStyle(n *html.Node, s *styleSet) {
	if len(s.list) == 0 {
		removeAttr(n, "style")
		return
	}
	setAttr(n, "style", s.String())
}

var voidAtoms = map[atom.Atom]bool{atom.Br: true, atom.Hr: true, atom.Img: true, atom.Input: true}

type pseudoSpan struct {
	parent *html.Node
	before bool
	text   string
	style  *styleSet
}

// inline 把主题 CSS 按 specificity + 书写顺序内联到 style 属性，
// 元素原有的 style（如表格对齐）优先级最高；::before/::after 展开成 span
func inline(root *html.Node, s *Theme) {
	var nodes []*html.Node
	var collect func(n *html.Node)
	collect = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode {
				nodes = append(nodes, c)
				collect(c)
			}
		}
	}
	nodes = append(nodes, root)
	collect(root)

	var spans []pseudoSpan
	for _, n := range nodes {
		var matched []*rule
		for i := range s.rules {
			if s.rules[i].sel.Match(n) {
				matched = append(matched, &s.rules[i])
			}
		}
		sort.SliceStable(matched, func(i, j int) bool {
			a, b := matched[i].sel.Specificity(), matched[j].sel.Specificity()
			if a != b {
				return a.Less(b)
			}
			return matched[i].order < matched[j].order
		})

		main := &styleSet{}
		pseudo := map[string]*pseudoSpan{}
		for _, r := range matched {
			target := main
			if r.pseudo != "" {
				if voidAtoms[n.DataAtom] {
					continue
				}
				ps := pseudo[r.pseudo]
				if ps == nil {
					ps = &pseudoSpan{parent: n, before: r.pseudo == "before", style: &styleSet{}}
					pseudo[r.pseudo] = ps
				}
				target = ps.style
			}
			for _, d := range r.decls {
				target.set(d)
			}
		}
		for _, d := range parseStyle(getAttr(n, "style")).list {
			main.set(d)
		}
		writeStyle(n, main)

		// 和浏览器一致：没有 content 的伪元素不渲染
		for _, key := range []string{"before", "after"} {
			ps := pseudo[key]
			if ps == nil {
				continue
			}
			text, ok := parseContent(ps.style.get("content"))
			if !ok {
				continue
			}
			ps.style.del("content")
			ps.text = text
			spans = append(spans, *ps)
		}
	}

	// 全部内联完再插入，避免新 span 被 span 选择器匹配
	for _, ps := range spans {
		span := elem(atom.Span)
		writeStyle(span, ps.style)
		if ps.text != "" {
			span.AppendChild(&html.Node{Type: html.TextNode, Data: ps.text})
		}
		if ps.before {
			ps.parent.InsertBefore(span, ps.parent.FirstChild)
		} else {
			ps.parent.AppendChild(span)
		}
	}
}

// parseContent 解析 content 里的字符串，attr()/counter() 等不支持的部分忽略
func parseContent(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if v == "" || v == "none" || v == "normal" {
		return "", false
	}
	var b strings.Builder
	for i := 0; i < len(v); {
		q := v[i]
		if q != '"' && q != '\'' {
			i++
			continue
		}
		j := i + 1
		for j < len(v) && v[j] != q {
			if v[j] == '\\' {
				j++
			}
			j++
		}
		b.WriteString(unescapeCSS(v[i+1 : min(j, len(v))]))
		i = j + 1
	}
	return b.String(), true
}

// unescapeCSS 处理 \201C 这类十六进制转义和 \" 这类字符转义
func unescapeCSS(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	isHex := func(c byte) bool {
		return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		j := i + 1
		for j < len(s) && j-i <= 6 && isHex(s[j]) {
			j++
		}
		if j > i+1 {
			n, _ := strconv.ParseUint(s[i+1:j], 16, 32)
			b.WriteRune(rune(n))
			if j < len(s) && s[j] == ' ' {
				j++
			}
			i = j - 1
			continue
		}
		b.WriteByte(s[j])
		i = j
	}
	return b.String()
}

// cleanup 内联之后做微信兼容处理：去掉 id/class 等辅助属性，修正代码块
func cleanup(root *html.Node) {
	removeAttr(root, "id")
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; {
			next := c.NextSibling
			if c.Type != html.ElementNode {
				c = next
				continue
			}
			// 没被主题用到的 prefix/suffix 是空 span，或被 display:none 隐藏，直接去掉
			if c.DataAtom == atom.Span && c.FirstChild == nil {
				st := parseStyle(getAttr(c, "style"))
				if len(st.list) == 0 || strings.TrimSpace(st.get("display")) == "none" {
					n.RemoveChild(c)
					c = next
					continue
				}
			}
			if c.DataAtom == atom.Pre {
				fixPre(c)
			}
			// 主题没给样式的标题 content span 只是包装，拆掉保持输出干净
			unwrap := c.DataAtom == atom.Span && getAttr(c, "class") == "content" && !hasAttr(c, "style")
			removeAttr(c, "class")
			removeAttr(c, "data-line")
			walk(c)
			if unwrap {
				for gc := c.FirstChild; gc != nil; gc = c.FirstChild {
					c.RemoveChild(gc)
					n.InsertBefore(gc, c)
				}
				n.RemoveChild(c)
			}
			c = next
		}
	}
	walk(root)
}

// fixPre 微信会清掉 <pre> 内 <code> 的 style，<pre> 带 font-family 时整条 style
// 也会被清掉（背景跟着丢）。所以 pre 只保留背景和布局，代码文字色下放到每行 span。
func fixPre(pre *html.Node) {
	ps := parseStyle(getAttr(pre, "style"))
	color := ps.get("color")
	var code *html.Node
	for c := pre.FirstChild; c != nil; c = c.NextSibling {
		if c.DataAtom == atom.Code {
			code = c
			break
		}
	}
	if code != nil {
		if c := parseStyle(getAttr(code, "style")).get("color"); c != "" {
			color = c
		}
	}
	ps.del("color")
	ps.del("font-family")
	writeStyle(pre, ps)
	if code == nil || color == "" {
		return
	}
	for l := code.FirstChild; l != nil; l = l.NextSibling {
		if l.Type != html.ElementNode || !hasAttr(l, "data-line") {
			continue
		}
		ls := parseStyle(getAttr(l, "style"))
		if ls.get("color") == "" {
			ls.set(decl{prop: "color", val: color})
		}
		writeStyle(l, ls)
	}
}
