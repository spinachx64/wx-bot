package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// 模板里的示例文字；wxctl format 发现它们还在时会报错，提醒替换
const (
	tplBodyHint    = "在这里写正文，写完运行 wxctl format 整理并检查。"
	tplSectionHint = "小标题下面的内容。"
)

// renderFront 生成带注释的 frontmatter，字段说明写在 YAML 注释里。
// 评论设置没写时按服务端默认（打开评论、不限粉丝）显式写出来
func renderFront(m Meta, skin string) string {
	q := func(s string) string {
		b, _ := yaml.Marshal(s)
		return strings.TrimSpace(string(b))
	}
	open := m.OpenComment == nil || *m.OpenComment
	fans := m.FansComment != nil && *m.FansComment
	mediaID := `# media_id: "" # 填上后 wxctl draft 会更新这篇草稿，而不是新建`
	if m.MediaID != "" {
		mediaID = "media_id: " + q(m.MediaID) + " # 更新这篇草稿；删掉则新建"
	}
	return fmt.Sprintf(`---
title: %s # 标题，不超过 32 个字
author: %s # 作者，不超过 16 个字
digest: %s # 摘要，不超过 128 个字；留空时微信取正文开头
cover: %s # 封面图，相对本文件的路径或 URL（必填）
skin: %s # 皮肤，留空用服务端默认，wxctl skins 查看可用皮肤
source_url: %s # 阅读原文链接
open_comment: %t # 打开评论
fans_comment: %t # 仅粉丝可评论，需要 open_comment: true
%s
---
`, q(m.Title), q(m.Author), q(m.Digest), q(m.Cover), q(skin), q(m.SourceURL), open, fans, mediaID)
}

// articleTemplate 新文章模板：frontmatter 取配置里的默认值，正文是示例文字
func articleTemplate(title, author string, d Defaults, skin string) string {
	m := Meta{Title: title, Author: author, Cover: "cover.jpg"}
	d.apply(&m)
	return renderFront(m, skin) + fmt.Sprintf(`
%s

## 小标题

%s

![图片说明](images/example.png)
`, tplBodyHint, tplSectionHint)
}

// fileNameFor 把标题变成安全的文件名
func fileNameFor(title string) string {
	name := strings.Map(func(r rune) rune {
		if r < 0x20 || strings.ContainsRune(`\/:*?"<>|`, r) {
			return '-'
		}
		return r
	}, title)
	name = strings.Trim(strings.TrimSpace(name), ".")
	if name == "" {
		return "post"
	}
	return name
}

// writeNew 写新文件，不覆盖已有文件（除非 force）
func writeNew(path, content string, force bool) error {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if errors.Is(err, os.ErrExist) {
		return usageError(path + " already exists, use --force to overwrite")
	}
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func cmdNew(e *env, args []string) error {
	fs := newFlagSet(e, "new", "<标题|file.md|-> [--title 标题] [--author 作者] [--force]")
	title := fs.String("title", "", "标题，默认用参数本身（是路径时用文件名）")
	author := fs.String("author", "", "作者，默认取配置 defaults.author")
	force := fs.Bool("force", false, "覆盖已存在的文件")
	pos, err := parseArgs(fs, args, 1)
	if err != nil {
		return err
	}
	arg := pos[0]
	if arg == "-" {
		fmt.Fprint(e.out, articleTemplate(first(*title, "标题"), *author, e.cfg.Defaults, e.cfg.Skin))
		return nil
	}

	// 以 .md 结尾或带目录的按路径处理，否则当作标题，在当前目录生成 <标题>.md
	path, t := arg, *title
	if strings.HasSuffix(strings.ToLower(arg), ".md") || strings.ContainsAny(arg, `/\`) {
		if filepath.Ext(path) == "" {
			path += ".md"
		}
		t = first(t, strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	} else {
		t = first(t, arg)
		path = fileNameFor(arg) + ".md"
	}

	if err := writeNew(path, articleTemplate(t, *author, e.cfg.Defaults, e.cfg.Skin), *force); err != nil {
		return err
	}
	abs, _ := filepath.Abs(path)
	return e.result(map[string]any{"path": abs, "title": t},
		fmt.Sprintf("已创建 %s\n替换正文、封面和示例图片后，运行 wxctl format %s 整理并检查", abs, path))
}

// findCover 猜封面：同目录的 cover.*，否则正文第一张图片
func findCover(a *Article) string {
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp", ".gif", ".bmp"} {
		if st, err := os.Stat(filepath.Join(a.Dir, "cover"+ext)); err == nil && !st.IsDir() {
			return "cover" + ext
		}
	}
	if imgs := a.Images(); len(imgs) > 0 {
		return imgs[0]
	}
	return ""
}

// cmdFormat 把普通 markdown 整理成带完整 frontmatter 的文章：已有字段保留，
// 没写的取配置默认值；开头的 # 标题移到 title。可重复执行。
// 整理后对结果做提交前检查，有错误时文件照样写出，但退出码为 1
func cmdFormat(e *env, args []string) error {
	fs := newFlagSet(e, "format", "<file.md|-> [-o 输出文件|-] [--cover 封面] [--offline] [--strict] [--no-check]")
	out := fs.String("o", "", "输出到另一个文件，- 为标准输出；默认原地改写")
	cover := fs.String("cover", "", "封面图，默认取同目录 cover.* 或正文第一张图")
	offline := fs.Bool("offline", false, "检查时不连服务端（跳过皮肤检查）")
	strict := fs.Bool("strict", false, "有警告也返回失败")
	noCheck := fs.Bool("no-check", false, "只整理，不检查")
	pos, err := parseArgs(fs, args, 1)
	if err != nil {
		return err
	}
	a, err := e.readArticle(pos[0])
	if err != nil {
		return err
	}
	m := a.Meta
	e.cfg.Defaults.apply(&m)
	m.Cover = first(*cover, m.Cover, findCover(a))
	text := renderFront(m, first(m.Skin, e.cfg.Skin)) + "\n" + strings.TrimLeft(a.Markdown, "\r\n")

	// 检查整理后的内容，行号对应写出的文件
	issues := []Issue{}
	if !*noCheck {
		fa, err := ParseArticle([]byte(text), a.Dir)
		if err != nil {
			return err
		}
		var skins []string
		if !*offline && e.cfg.Token != "" {
			skins = e.fetchSkins(e.ctx)
		}
		if found := CheckArticle(fa, skins); found != nil {
			issues = found // 没问题时保持空切片，JSON 输出 [] 而不是 null
		}
	}

	dst := first(*out, pos[0])
	if dst == "-" {
		if _, err := io.WriteString(e.out, text); err != nil {
			return err
		}
		return e.reportIssues(dst, issues, *strict, true)
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	abs, _ := filepath.Abs(dst)
	failed := countLevel(issues, levelError) > 0 || (*strict && len(issues) > 0)
	if e.json {
		if err := e.writeJSON(map[string]any{"ok": !failed, "path": abs, "title": m.Title, "cover": m.Cover, "issues": issues}); err != nil {
			return err
		}
		if failed {
			return errReported
		}
		return nil
	}
	msg := fmt.Sprintf("已整理 %s（标题：%s）", abs, m.Title)
	if *noCheck {
		fmt.Fprintln(e.out, msg)
		return nil
	}
	if len(issues) == 0 {
		msg += "\n检查通过"
	}
	fmt.Fprintln(e.out, msg)
	return e.reportIssues(dst, issues, *strict, false)
}
