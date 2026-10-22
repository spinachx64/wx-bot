package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// 微信草稿接口的字段限制
const (
	maxTitle   = 32
	maxAuthor  = 16
	maxDigest  = 128
	maxContent = 20000 // 正文不超过 2 万字符
)

const (
	levelError = "error"
	levelWarn  = "warning"
)

// Issue 检查发现的一个问题；Line 为原文件行号，0 表示不对应具体行
type Issue struct {
	Level string `json:"level"`
	Line  int    `json:"line,omitempty"`
	Field string `json:"field,omitempty"`
	Msg   string `json:"message"`
}

type checker struct {
	a      *Article
	issues []Issue
}

func (c *checker) add(level string, line int, field, format string, args ...any) {
	c.issues = append(c.issues, Issue{Level: level, Line: line, Field: field, Msg: fmt.Sprintf(format, args...)})
}

var knownMeta = map[string]bool{
	"title": true, "author": true, "digest": true, "cover": true, "skin": true,
	"source_url": true, "media_id": true, "open_comment": true, "fans_comment": true,
}

// fieldLine 返回 frontmatter 中 key 所在的行号（frontmatter 从第 2 行开始）
func (c *checker) fieldLine(key string) int {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `\s*:`)
	if loc := re.FindStringIndex(c.a.front); loc != nil {
		return 2 + strings.Count(c.a.front[:loc[0]], "\n")
	}
	return 0
}

// bodyLine 把正文中的字节偏移换算成原文件行号
func (c *checker) bodyLine(off int) int {
	return c.a.bodyLine + strings.Count(c.a.Markdown[:off], "\n")
}

// CheckArticle 离线检查文章：frontmatter、长度限制、封面和图片、模板残留、微信会过滤的写法。
// skins 非 nil 时同时检查皮肤名
func CheckArticle(a *Article, skins []string) []Issue {
	c := &checker{a: a}
	c.checkMeta(skins)
	c.checkCover()
	c.checkBody()
	sort.SliceStable(c.issues, func(i, j int) bool {
		if (c.issues[i].Level == levelError) != (c.issues[j].Level == levelError) {
			return c.issues[i].Level == levelError
		}
		return c.issues[i].Line < c.issues[j].Line
	})
	return c.issues
}

func (c *checker) checkMeta(skins []string) {
	m := c.a.Meta
	if c.a.front != "" {
		var raw map[string]any
		if yaml.Unmarshal([]byte(c.a.front), &raw) == nil {
			for k := range raw {
				if !knownMeta[k] {
					c.add(levelWarn, c.fieldLine(k), k, "未知字段 %q，会被忽略（拼写错误？）", k)
				}
			}
		}
	}

	switch c.a.titleFrom {
	case "filename":
		c.add(levelWarn, 0, "title", "没有设置标题，将使用文件名 %q", m.Title)
	case "":
		if m.Title == "" {
			c.add(levelError, 0, "title", "缺少标题：在 frontmatter 写 title 或用开头的 # 标题")
		}
	}
	c.checkLen("title", m.Title, maxTitle, true)
	c.checkLen("author", m.Author, maxAuthor, true)
	c.checkLen("digest", m.Digest, maxDigest, true)

	if m.SourceURL != "" {
		if u, err := url.Parse(m.SourceURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			c.add(levelError, c.fieldLine("source_url"), "source_url", "阅读原文必须是 http(s) 链接：%q", m.SourceURL)
		}
	}
	if m.FansComment && !m.OpenComment {
		c.add(levelWarn, c.fieldLine("fans_comment"), "fans_comment", "fans_comment 需要 open_comment: true 才生效")
	}
	if m.Skin != "" && skins != nil && !contains(skins, m.Skin) {
		c.add(levelError, c.fieldLine("skin"), "skin", "皮肤 %q 不存在，可用：%s", m.Skin, strings.Join(skins, ", "))
	}
}

func (c *checker) checkLen(field, v string, limit int, isError bool) {
	if n := utf8.RuneCountInString(v); n > limit {
		level := levelWarn
		if isError {
			level = levelError
		}
		c.add(level, c.fieldLine(field), field, "%s 有 %d 个字，超过上限 %d", field, n, limit)
	}
}

func (c *checker) checkCover() {
	src := c.a.Meta.Cover
	line := c.fieldLine("cover")
	if src == "" {
		c.add(levelError, 0, "cover", "缺少封面：在 frontmatter 写 cover，或提交时用 --cover / --thumb")
		return
	}
	c.checkImage(line, "cover", src, maxCoverImage)
}

// checkImage 本地图片检查存在、能解码、能压到限制以内；外链只提示会在提交时下载
func (c *checker) checkImage(line int, field, src string, limit int) {
	if IsWechat(src) {
		return
	}
	if isRemote(src) {
		c.add(levelWarn, line, field, "外链图片 %s 会在提交时下载后上传，下载失败会中断提交", src)
		return
	}
	p := c.a.resolveLocal(src)
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			c.add(levelError, line, field, "图片不存在：%s", p)
		} else {
			c.add(levelError, line, field, "读取图片失败：%v", err)
		}
		return
	}
	if _, _, err := image.DecodeConfig(bytes.NewReader(data)); err != nil {
		c.add(levelError, line, field, "不是支持的图片格式（jpg/png/gif/webp/bmp）：%s", p)
		return
	}
	if len(data) > limit {
		if _, _, err := fitImage(data, p, limit); err != nil {
			c.add(levelError, line, field, "图片 %s 压缩不到 %dKB 以内：%v", p, limit>>10, err)
		} else {
			c.add(levelWarn, line, field, "图片 %s 有 %dKB，提交时会自动压缩", p, len(data)>>10)
		}
	}
}

var (
	rawTagRe  = regexp.MustCompile(`</?[a-zA-Z][a-zA-Z0-9]*(\s[^<>]*)?/?>`)
	linkRe    = regexp.MustCompile(`(?:^|[^!])\[[^\]]*\]\(\s*<?(https?://[^)\s>]+)`)
	h1BodyRe  = regexp.MustCompile(`(?m)^#[ \t]+\S`)
	fenceOpen = regexp.MustCompile("(?m)^(```|~~~)")
)

func (c *checker) checkBody() {
	md := c.a.Markdown
	if strings.Contains(md, tplBodyHint) || strings.Contains(md, tplSectionHint) {
		c.add(levelError, c.bodyLine(max(strings.Index(md, tplBodyHint), 0)), "", "正文还有模板示例文字，请替换")
	}
	if n := utf8.RuneCountInString(md); n > maxContent {
		c.add(levelWarn, 0, "", "正文约 %d 字符，超过微信上限 %d（渲染后的 HTML 也计入），可能被拒绝", n, maxContent)
	}
	if len(fenceOpen.FindAllStringIndex(md, -1))%2 == 1 {
		c.add(levelError, 0, "", "代码块的 ``` 没有成对出现，后面的内容会全部变成代码")
	}

	seen := map[string]bool{}
	for _, ref := range c.a.imageRefs() {
		if seen[ref.src] {
			continue
		}
		seen[ref.src] = true
		if strings.HasSuffix(ref.src, "images/example.png") {
			c.add(levelError, c.bodyLine(ref.off), "", "还有模板示例图片 %s，请替换或删除", ref.src)
			continue
		}
		c.checkImage(c.bodyLine(ref.off), "", ref.src, maxDownload)
	}

	outsideCode(md, func(s string, off int) {
		for _, loc := range rawTagRe.FindAllStringIndex(s, -1) {
			c.add(levelWarn, c.bodyLine(off+loc[0]), "", "HTML 标签 %s 会按文字原样显示，图片请用 ![]() 语法", s[loc[0]:loc[1]])
		}
		for _, m := range linkRe.FindAllStringSubmatchIndex(s, -1) {
			u := s[m[2]:m[3]]
			if !strings.Contains(u, "mp.weixin.qq.com") {
				c.add(levelWarn, c.bodyLine(off+m[2]), "", "外部链接 %s 在公众号里不能点击", u)
			}
		}
		if loc := h1BodyRe.FindStringIndex(s); loc != nil && c.a.titleFrom != "h1" {
			c.add(levelWarn, c.bodyLine(off+loc[0]), "", "正文里有一级标题，会和文章标题重复显示")
		}
	})
}

type imageRef struct {
	src string
	off int // 在 Markdown 中的字节偏移
}

// imageRefs 列出代码之外的图片引用及位置
func (a *Article) imageRefs() []imageRef {
	var out []imageRef
	outsideCode(a.Markdown, func(s string, off int) {
		for _, m := range imageRe.FindAllStringSubmatchIndex(s, -1) {
			src := strings.TrimSuffix(strings.TrimPrefix(s[m[4]:m[5]], "<"), ">")
			out = append(out, imageRef{src: src, off: off + m[0]})
		}
	})
	return out
}

// outsideCode 对代码块和行内代码之外的每一段调用 fn，附带该段的起始偏移
func outsideCode(md string, fn func(s string, off int)) {
	each := func(s string, base int, re *regexp.Regexp, fn func(string, int)) {
		last := 0
		for _, m := range re.FindAllStringIndex(s, -1) {
			fn(s[last:m[0]], base+last)
			last = m[1]
		}
		fn(s[last:], base+last)
	}
	each(md, 0, fenceRe, func(s string, off int) {
		each(s, off, inlineCodeRe, fn)
	})
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// fetchSkins 取服务端皮肤名；不可用时返回 nil（跳过皮肤检查）
func (e *env) fetchSkins(ctx context.Context) []string {
	var out struct {
		Skins []struct {
			Name string `json:"name"`
		} `json:"skins"`
	}
	if e.client.getJSON(ctx, "/api/v1/skins", &out) != nil {
		return nil
	}
	names := make([]string, 0, len(out.Skins))
	for _, s := range out.Skins {
		names = append(names, s.Name)
	}
	return names
}

// errReported 结果已经输出过，Run 只需返回退出码 1
var errReported = errors.New("reported")

func countLevel(issues []Issue, level string) int {
	n := 0
	for _, i := range issues {
		if i.Level == level {
			n++
		}
	}
	return n
}

func formatIssues(file string, issues []Issue) string {
	var b strings.Builder
	for _, i := range issues {
		loc := file
		if i.Line > 0 {
			loc = fmt.Sprintf("%s:%d", file, i.Line)
		}
		fmt.Fprintf(&b, "%s: %s: %s\n", loc, i.Level, i.Msg)
	}
	fmt.Fprintf(&b, "%d 个错误，%d 个警告", countLevel(issues, levelError), countLevel(issues, levelWarn))
	return b.String()
}

func cmdCheck(e *env, args []string) error {
	fs := newFlagSet(e, "check", "<md|-> [--offline] [--strict]")
	offline := fs.Bool("offline", false, "不连服务端（跳过皮肤检查）")
	strict := fs.Bool("strict", false, "有警告也返回失败")
	pos, err := parseArgs(fs, args, 1)
	if err != nil {
		return err
	}
	a, err := e.readArticle(pos[0])
	if err != nil {
		return err
	}
	var skins []string
	if !*offline && e.cfg.Token != "" {
		skins = e.fetchSkins(e.ctx)
	}
	issues := CheckArticle(a, skins)
	failed := countLevel(issues, levelError) > 0 || (*strict && len(issues) > 0)
	text := formatIssues(pos[0], issues)
	if len(issues) == 0 {
		text = "检查通过：" + a.Meta.Title
	}
	if !e.json {
		w := e.out
		if failed {
			w = e.errw
		}
		fmt.Fprintln(w, text)
	} else {
		e.writeJSON(map[string]any{"ok": !failed, "title": a.Meta.Title, "issues": issues})
	}
	if failed {
		return errReported
	}
	return nil
}
