package cli

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Meta 文章 frontmatter，所有字段可选
type Meta struct {
	Title       string `yaml:"title"`
	Author      string `yaml:"author"`
	Digest      string `yaml:"digest"`
	Cover       string `yaml:"cover"`        // 封面图路径或 URL
	Skin        string `yaml:"skin"`         // 皮肤
	SourceURL   string `yaml:"source_url"`   // 阅读原文
	MediaID     string `yaml:"media_id"`     // 指定要更新的草稿
	OpenComment *bool  `yaml:"open_comment"` // 打开评论，不写时由服务端默认打开
	FansComment *bool  `yaml:"fans_comment"` // 仅粉丝可评论
}

// Article 解析后的本地文章
type Article struct {
	Path     string // 绝对路径
	Dir      string // 图片相对路径的基准目录
	Meta     Meta
	Markdown string // 去掉 frontmatter 的正文

	front     string // frontmatter 原文，检查时用来定位字段行号
	bodyLine  int    // Markdown 第一行在原文件中的行号
	titleFrom string // 标题来源：frontmatter / h1 / filename / flag
}

var (
	frontmatterRe = regexp.MustCompile(`(?s)\A---\r?\n(.*?)\r?\n---[ \t]*(?:\r?\n|\z)`)
	h1Re          = regexp.MustCompile(`(?m)\A\s*#[ \t]+(.+?)[ \t#]*(?:\r?\n|\z)`)
	// 没闭合的代码块按 markdown 规则延续到文末
	fenceRe      = regexp.MustCompile("(?s)```.*?(?:```|\\z)|~~~.*?(?:~~~|\\z)")
	inlineCodeRe = regexp.MustCompile("`+[^`\n]*?`+")
	// ![alt](src "title")，src 可用 <> 包住以支持空格
	imageRe = regexp.MustCompile(`!\[([^\]]*)\]\(\s*(<[^>\n]+>|[^)\s]+)(\s+"[^"\n]*")?\s*\)`)
)

// LoadArticle 读取 md 文件并解析 frontmatter；没有 title 时取开头的一级标题（并从正文移除），
// 再没有就用文件名。微信标题单独显示，正文里不再重复一级标题
func LoadArticle(path string) (*Article, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	a, err := ParseArticle(data, filepath.Dir(abs))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	a.Path = abs
	if a.Meta.Title == "" {
		a.Meta.Title = strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
		a.titleFrom = "filename"
	}
	return a, nil
}

// ParseArticle 解析 markdown 内容，dir 是图片相对路径的基准目录（stdin 输入时使用）
func ParseArticle(data []byte, dir string) (*Article, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	a := &Article{Dir: dir, Markdown: string(data), bodyLine: 1}
	if m := frontmatterRe.FindStringSubmatchIndex(a.Markdown); m != nil {
		a.front = a.Markdown[m[2]:m[3]]
		if err := yaml.Unmarshal([]byte(a.front), &a.Meta); err != nil {
			return nil, fmt.Errorf("frontmatter: %w", err)
		}
		a.bodyLine += strings.Count(a.Markdown[:m[1]], "\n")
		a.Markdown = a.Markdown[m[1]:]
	}
	if a.Meta.Title != "" {
		a.titleFrom = "frontmatter"
	} else if m := h1Re.FindStringSubmatchIndex(a.Markdown); m != nil {
		a.Meta.Title = strings.TrimSpace(a.Markdown[m[2]:m[3]])
		a.titleFrom = "h1"
		a.bodyLine += strings.Count(a.Markdown[:m[1]], "\n")
		a.Markdown = a.Markdown[m[1]:]
	}
	if strings.TrimSpace(a.Markdown) == "" {
		return nil, errors.New("article body is empty")
	}
	return a, nil
}

// IsWechat 已经是微信图床地址，无需上传
func IsWechat(src string) bool {
	u, err := url.Parse(src)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") &&
		(u.Host == "mmbiz.qpic.cn" || strings.HasSuffix(u.Host, ".qpic.cn"))
}

func isRemote(src string) bool {
	return strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://")
}

// resolveLocal 把相对路径解析到文章目录，支持 ~ 和 URL 编码（如 %20）
func (a *Article) resolveLocal(src string) string {
	src = strings.TrimPrefix(src, "file://")
	if dec, err := url.PathUnescape(src); err == nil {
		src = dec
	}
	if strings.HasPrefix(src, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			src = filepath.Join(home, src[2:])
		}
	}
	if !filepath.IsAbs(src) {
		src = filepath.Join(a.Dir, src)
	}
	return src
}

// Images 列出代码块之外的所有图片地址，按出现顺序去重
func (a *Article) Images() []string {
	seen := map[string]bool{}
	var out []string
	a.mapImages(func(src string) string {
		if !seen[src] {
			seen[src] = true
			out = append(out, src)
		}
		return src
	})
	return out
}

// ReplaceImages 用 fn 的返回值替换代码块之外的图片地址
func (a *Article) ReplaceImages(fn func(src string) string) {
	a.Markdown = a.mapImages(fn)
}

func (a *Article) mapImages(fn func(string) string) string {
	return mapOutside(a.Markdown, fenceRe, func(s string) string {
		return mapOutside(s, inlineCodeRe, func(s string) string {
			return imageRe.ReplaceAllStringFunc(s, func(m string) string {
				sub := imageRe.FindStringSubmatch(m)
				src := strings.TrimSuffix(strings.TrimPrefix(sub[2], "<"), ">")
				return "![" + sub[1] + "](<" + fn(src) + ">" + sub[3] + ")"
			})
		})
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
