package render

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
	"golang.org/x/net/html"
)

// ---------- 中文强调符边界 ----------

var (
	codeFenceRe  = regexp.MustCompile("(?s)```.*?```|~~~.*?~~~")
	inlineCodeRe = regexp.MustCompile("`+[^`\n]*?`+")

	colonSpaceCloseRe = regexp.MustCompile(`(\*\*[^*\n]+?[：:])\s+\*\*`)
	colonCloseRe      = regexp.MustCompile(`\*\*[^*\n]+?[：:]\*\*`)
	strongRe          = regexp.MustCompile(`\*\*([^\s*][^*\n]*?)\*\*`)
	emRe              = regexp.MustCompile(`\*([^\s*][^*\n]*?)\*`)
)

// zwnj 以实体形式插入：解析时 & 算标点，能让闭合星号成立；渲染后统一删掉
const zwnj = "&zwnj;"

// normalizeEmphasis CommonMark 不认「**结论。**这层」「*问题？*对于」这类
// 标点结尾、紧跟汉字的闭合星号，会原样保留星号。临时插入边界让它们正确解析。
// 代码块和行内代码不处理。
func normalizeEmphasis(md string) string {
	return mapOutside(md, codeFenceRe, func(s string) string {
		return mapOutside(s, inlineCodeRe, normalizeText)
	})
}

func mapOutside(s string, re *regexp.Regexp, fn func(string) string) string {
	var b strings.Builder
	last := 0
	for _, m := range re.FindAllStringIndex(s, -1) {
		b.WriteString(fn(s[last:m[0]]))
		b.WriteString(s[m[0]:m[1]])
		last = m[1]
	}
	b.WriteString(fn(s[last:]))
	return b.String()
}

func normalizeText(s string) string {
	// **标题：** 正文 → 去掉冒号与闭合星号之间的空格
	s = replaceMatches(s, colonSpaceCloseRe, func(s string, m []int, next rune) (string, bool) {
		switch {
		case next == 0:
			return "", false
		case unicode.IsSpace(next):
			return s[m[2]:m[3]] + "**", true
		default:
			return s[m[2]:m[3]] + "**" + zwnj, true
		}
	})
	s = replaceMatches(s, colonCloseRe, func(s string, m []int, next rune) (string, bool) {
		if next == 0 || unicode.IsSpace(next) {
			return "", false
		}
		return s[m[0]:m[1]] + zwnj, true
	})
	punctClose := func(marker string) func(string, []int, rune) (string, bool) {
		return func(s string, m []int, next rune) (string, bool) {
			inner := s[m[2]:m[3]]
			last, _ := utf8.DecodeLastRuneInString(inner)
			prev, _ := utf8.DecodeLastRuneInString(s[:m[0]])
			if !isPunctSym(last) || prev == '*' || next == 0 || next == '*' ||
				unicode.IsSpace(next) || isPunctSym(next) {
				return "", false
			}
			return marker + inner + marker + zwnj, true
		}
	}
	s = replaceMatches(s, strongRe, punctClose("**"))
	return replaceMatches(s, emRe, punctClose("*"))
}

// replaceMatches 对每个匹配调用 fn（附带匹配后的下一个字符，0 表示末尾），
// 返回 false 时保留原文。用来替代 Go 正则不支持的前后断言。
func replaceMatches(s string, re *regexp.Regexp, fn func(s string, m []int, next rune) (string, bool)) string {
	var b strings.Builder
	last := 0
	for _, m := range re.FindAllStringSubmatchIndex(s, -1) {
		next, _ := utf8.DecodeRuneInString(s[m[1]:])
		if next == utf8.RuneError {
			next = 0
		}
		rep, ok := fn(s, m, next)
		if !ok {
			continue
		}
		b.WriteString(s[last:m[0]])
		b.WriteString(rep)
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

func isPunctSym(r rune) bool { return unicode.IsPunct(r) || unicode.IsSymbol(r) }

// ---------- 自定义节点渲染 ----------

type nodeRenderer struct{}

func (nodeRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindFencedCodeBlock, renderCode)
	reg.Register(ast.KindCodeBlock, renderCode)
	reg.Register(ast.KindHTMLBlock, renderHTMLBlock)
	reg.Register(ast.KindRawHTML, renderRawHTML)
}

func renderCode(w util.BufWriter, src []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	lang := ""
	if fc, ok := n.(*ast.FencedCodeBlock); ok {
		lang = string(fc.Language(src))
	}
	var code strings.Builder
	lines := n.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		code.Write(seg.Value(src))
	}
	w.WriteString("<pre><code")
	if lang != "" {
		fmt.Fprintf(w, ` class="hljs language-%s"`, html.EscapeString(lang))
	} else {
		w.WriteString(` class="hljs"`)
	}
	w.WriteString(">")
	w.WriteString(highlight(code.String(), lang))
	w.WriteString("</code></pre>\n")
	return ast.WalkSkipChildren, nil
}

// 正文里写的字面标签（<br>、<style> 等）一律转义成文本，避免被当成真标签
func renderRawHTML(w util.BufWriter, src []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		segs := n.(*ast.RawHTML).Segments
		for i := 0; i < segs.Len(); i++ {
			seg := segs.At(i)
			w.WriteString(html.EscapeString(string(seg.Value(src))))
		}
	}
	return ast.WalkSkipChildren, nil
}

func renderHTMLBlock(w util.BufWriter, src []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	hb := n.(*ast.HTMLBlock)
	var parts []string
	lines := hb.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		parts = append(parts, string(seg.Value(src)))
	}
	if hb.HasClosure() {
		parts = append(parts, string(hb.ClosureLine.Value(src)))
	}
	text := strings.TrimRight(strings.Join(parts, ""), "\n")
	w.WriteString("<p>")
	w.WriteString(strings.ReplaceAll(html.EscapeString(text), "\n", "<br>"))
	w.WriteString("</p>\n")
	return ast.WalkSkipChildren, nil
}

// ---------- 代码高亮 ----------

var codeSpaceRe = regexp.MustCompile(`^ +| {2,}`)

// highlight 微信会压掉 <pre> 里的换行和空格，所以每行包成 display:block 的 span，
// 行首缩进和连续空格转 &nbsp;。token 输出 hljs-* class，由皮肤 CSS 决定颜色，
// 内联后 class 会被清掉。行 span 带 data-line，供 fixPre 下放代码文字色。
func highlight(code, lang string) string {
	code = strings.TrimSuffix(code, "\n")
	type run struct{ cls, text string }
	lines := [][]run{nil}
	add := func(cls, text string) {
		for i, part := range strings.Split(text, "\n") {
			if i > 0 {
				lines = append(lines, nil)
			}
			if part == "" {
				continue
			}
			l := &lines[len(lines)-1]
			if k := len(*l); k > 0 && (*l)[k-1].cls == cls {
				(*l)[k-1].text += part
			} else {
				*l = append(*l, run{cls, part})
			}
		}
	}

	var it chroma.Iterator
	if lang != "" {
		if lx := lexers.Get(lang); lx != nil {
			it, _ = chroma.Coalesce(lx).Tokenise(nil, code)
		}
	}
	if it == nil {
		add("", code)
	} else {
		for t := it(); t != chroma.EOF; t = it() {
			add(tokenClass(t.Type), t.Value)
		}
	}
	// lexer 可能补一个结尾换行
	if want := strings.Count(code, "\n") + 1; len(lines) > want {
		lines = lines[:want]
	}

	var b strings.Builder
	for _, l := range lines {
		b.WriteString(`<span data-line="" style="display:block">`)
		if len(l) == 0 {
			b.WriteString("&nbsp;")
		}
		for _, r := range l {
			t := codeSpaceRe.ReplaceAllStringFunc(html.EscapeString(r.text), func(s string) string {
				return strings.Repeat("&nbsp;", len(s))
			})
			if r.cls == "" {
				b.WriteString(t)
			} else {
				fmt.Fprintf(&b, `<span class="%s">%s</span>`, r.cls, t)
			}
		}
		b.WriteString("</span>")
	}
	return b.String()
}

// tokenClass 把 chroma token 映射到 highlight.js 的 class，皮肤可以直接沿用 hljs 主题的选择器
func tokenClass(t chroma.TokenType) string {
	switch {
	case t.InCategory(chroma.Comment):
		return "hljs-comment"
	case t == chroma.KeywordConstant:
		return "hljs-literal"
	case t == chroma.KeywordType, t == chroma.NameClass:
		return "hljs-type"
	case t == chroma.NameTag:
		return "hljs-name"
	case t.InCategory(chroma.Keyword):
		return "hljs-keyword"
	case t == chroma.NameAttribute:
		return "hljs-attr"
	case t.InSubCategory(chroma.LiteralString):
		return "hljs-string"
	case t.InSubCategory(chroma.LiteralNumber):
		return "hljs-number"
	case t == chroma.NameFunction, t == chroma.NameFunctionMagic:
		return "hljs-title function_"
	case t == chroma.GenericHeading:
		return "hljs-section"
	case t == chroma.NameVariable, t == chroma.NameVariableClass, t == chroma.NameVariableGlobal,
		t == chroma.NameVariableInstance, t == chroma.NameVariableMagic:
		return "hljs-variable"
	case t == chroma.NameBuiltin, t == chroma.NameBuiltinPseudo:
		return "hljs-built_in"
	case t == chroma.Operator, t == chroma.OperatorWord:
		return "hljs-operator"
	case t.InCategory(chroma.Punctuation):
		return "hljs-punctuation"
	}
	return ""
}
