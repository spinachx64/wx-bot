package cli

import (
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

const maxNewspicImages = 20

func (m Meta) articleType() string { return first(m.ArticleType, "news") }

func (m *Meta) setArticleType(override string) error {
	typ := first(override, m.ArticleType, "news")
	if typ != "news" && typ != "newspic" {
		return usageError(fmt.Sprintf("article_type must be news or newspic, got %q", typ))
	}
	m.ArticleType = typ
	return nil
}

// imageSource 区分显式素材 ID、URL 和文件路径；不猜测不透明的素材 ID。
func imageSource(src string) (kind, value string, err error) {
	if strings.TrimSpace(src) == "" {
		return "", "", fmt.Errorf("图片来源不能为空")
	}
	if strings.HasPrefix(src, "media_id:") {
		id := strings.TrimPrefix(src, "media_id:")
		if id == "" || strings.ContainsAny(id, " \t\r\n") {
			return "", "", fmt.Errorf("media_id: 后必须填写有效的永久素材 ID")
		}
		return "media_id", id, nil
	}
	if isRemote(src) {
		u, e := url.Parse(src)
		if e != nil || u.Host == "" {
			return "", "", fmt.Errorf("无效的图片 URL：%s", src)
		}
		return "url", src, nil
	}
	if strings.Contains(src, "://") && !strings.HasPrefix(src, "file://") {
		return "", "", fmt.Errorf("图片仅支持本地路径、http(s) URL 或 media_id:ID：%s", src)
	}
	return "path", src, nil
}

func (a *Article) validateNewspic() error {
	if n := len(a.Meta.Images); n < 1 || n > maxNewspicImages {
		return fmt.Errorf("newspic images must contain 1-%d items, got %d", maxNewspicImages, n)
	}
	for i, src := range a.Meta.Images {
		if _, _, err := imageSource(src); err != nil {
			return fmt.Errorf("images[%d]: %w", i, err)
		}
	}
	if len(markdownImages(a.Markdown)) > 0 {
		return fmt.Errorf("newspic 正文还有图片，请先运行 wxctl format --type newspic 将图片整理到 images")
	}
	return nil
}

type markdownImage struct {
	src        string
	start, end int
}

// imageLinkParser 复用 Goldmark 的链接解析，并记录完整图片语法的源码范围。
// 因此括号、转义、引用式图片、代码块和行内代码均遵守 Markdown 规则。
type imageLinkParser struct {
	parser.InlineParser
	open   []imageOpening
	images map[*ast.Image]markdownImage
}

type imageOpening struct {
	node  ast.Node
	start int
}

func (p *imageLinkParser) Parse(parent ast.Node, reader text.Reader, pc parser.Context) ast.Node {
	ch := reader.Peek()
	_, before := reader.Position()
	start := -1
	if ch == ']' {
		for i := len(p.open) - 1; i >= 0; i-- {
			if p.open[i].node.Parent() == parent {
				start = p.open[i].start
				break
			}
		}
	}
	node := p.InlineParser.Parse(parent, reader, pc)
	if node != nil && (ch == '!' || ch == '[') {
		p.open = append(p.open, imageOpening{node, before.Start})
	}
	if img, ok := node.(*ast.Image); ok && start >= 0 {
		_, after := reader.Position()
		dst := util.ResolveEntityNames(util.ResolveNumericReferences(util.UnescapePunctuations(img.Destination)))
		p.images[img] = markdownImage{string(dst), start, after.Start}
	}
	return node
}

func (p *imageLinkParser) CloseBlock(parent ast.Node, reader text.Reader, pc parser.Context) {
	p.InlineParser.(parser.CloseBlocker).CloseBlock(parent, reader, pc)
	p.open = nil
}

func markdownImages(md string) []markdownImage {
	p := &imageLinkParser{InlineParser: parser.NewLinkParser(), images: map[*ast.Image]markdownImage{}}
	inlines := parser.DefaultInlineParsers()
	for i := range inlines {
		if inlines[i].Value == parser.NewLinkParser() {
			inlines[i].Value = p
		}
	}
	parsed := parser.NewParser(
		parser.WithBlockParsers(parser.DefaultBlockParsers()...),
		parser.WithInlineParsers(inlines...),
		parser.WithParagraphTransformers(parser.DefaultParagraphTransformers()...),
	).Parse(text.NewReader([]byte(md)))
	var images []markdownImage
	ast.Walk(parsed, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			if img, ok := node.(*ast.Image); ok {
				if ref, found := p.images[img]; found {
					images = append(images, ref)
				}
				return ast.WalkSkipChildren, nil
			}
		}
		return ast.WalkContinue, nil
	})
	sort.Slice(images, func(i, j int) bool { return images[i].start < images[j].start })
	return images
}

// collectNewspicImages 保留已有顺序，将正文里的新图片移入头信息。
func (a *Article) collectNewspicImages() {
	seen := map[string]bool{}
	key := func(src string) string {
		kind, _, err := imageSource(src)
		if err == nil && kind == "path" {
			return a.resolveLocal(src)
		}
		return src
	}
	for _, src := range a.Meta.Images {
		seen[key(src)] = true
	}
	var body strings.Builder
	last := 0
	for _, img := range markdownImages(a.Markdown) {
		if !seen[key(img.src)] {
			a.Meta.Images = append(a.Meta.Images, img.src)
			seen[key(img.src)] = true
		}
		body.WriteString(a.Markdown[last:img.start])
		last = img.end
	}
	body.WriteString(a.Markdown[last:])
	a.Markdown = tidyNewspicBody(body.String())
}

// tidyNewspicBody 合并正文空行，但保留代码示例内部的空行和缩进。
func tidyNewspicBody(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	var codeRanges [][2]int
	parsed := goldmark.DefaultParser().Parse(text.NewReader([]byte(body)))
	ast.Walk(parsed, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering && (node.Kind() == ast.KindFencedCodeBlock || node.Kind() == ast.KindCodeBlock) && node.Lines().Len() > 0 {
			lines := node.Lines()
			codeRanges = append(codeRanges, [2]int{lines.At(0).Start, lines.At(lines.Len() - 1).Stop})
		}
		return ast.WalkContinue, nil
	})
	var out []string
	blank := true
	off := 0
	for _, line := range strings.Split(body, "\n") {
		inCode := false
		for _, span := range codeRanges {
			if off <= span[1] && off+len(line) >= span[0] {
				inCode = true
				break
			}
		}
		empty := strings.TrimSpace(line) == ""
		if inCode || !empty || !blank {
			out = append(out, line)
		}
		blank = empty
		off += len(line) + 1
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"
}

// rebaseImages 只调整相对路径，绝对路径、URL 和素材 ID 保持原样。
func (a *Article) rebaseImages(dstDir string) error {
	for i, src := range a.Meta.Images {
		kind, _, err := imageSource(src)
		if err != nil || kind != "path" || filepath.IsAbs(src) || strings.HasPrefix(src, "~/") || strings.HasPrefix(src, "file://") {
			continue
		}
		path, err := filepath.Rel(dstDir, a.resolveLocal(src))
		if err != nil {
			return err
		}
		a.Meta.Images[i] = filepath.ToSlash(path)
	}
	a.Dir = dstDir
	return nil
}
