package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// 模板里的示例文字；wxctl check 发现它们还在时会报错，提醒替换
const (
	tplBodyHint    = "在这里写正文，写完运行 wxctl check 检查。"
	tplSectionHint = "小标题下面的内容。"
)

// articleTemplate 生成带完整 frontmatter 的文章模板，字段说明写在 YAML 注释里
func articleTemplate(title, author, skin string) string {
	q := func(s string) string {
		b, _ := yaml.Marshal(s)
		return strings.TrimSpace(string(b))
	}
	return fmt.Sprintf(`---
title: %s # 标题，不超过 32 个字
author: %s # 作者，不超过 16 个字
digest: "" # 摘要，不超过 128 个字；留空时微信取正文开头
cover: cover.jpg # 封面图，相对本文件的路径或 URL（必填）
skin: %s # 皮肤，留空用服务端默认，wxctl skins 查看可用皮肤
source_url: "" # 阅读原文链接
open_comment: true # 打开评论（不写也默认打开）
fans_comment: false # 仅粉丝可评论，需要 open_comment: true
# media_id: "" # 填上后 wxctl draft 会更新这篇草稿，而不是新建
---

%s

## 小标题

%s

![图片说明](images/example.png)
`, q(title), q(author), q(skin), tplBodyHint, tplSectionHint)
}

func cmdNew(e *env, args []string) error {
	fs := newFlagSet(e, "new", "<file.md|-> [--title 标题] [--author 作者] [--force]")
	title := fs.String("title", "", "标题，默认用文件名")
	author := fs.String("author", "", "作者")
	force := fs.Bool("force", false, "覆盖已存在的文件")
	pos, err := parseArgs(fs, args, 1)
	if err != nil {
		return err
	}
	path := pos[0]
	if path == "-" {
		fmt.Fprint(e.out, articleTemplate(first(*title, "标题"), *author, e.cfg.Skin))
		return nil
	}
	if filepath.Ext(path) == "" {
		path += ".md"
	}
	t := first(*title, strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if *force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if errors.Is(err, os.ErrExist) {
		return usageError(path + " already exists, use --force to overwrite")
	}
	if err != nil {
		return err
	}
	if _, err := f.WriteString(articleTemplate(t, *author, e.cfg.Skin)); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	abs, _ := filepath.Abs(path)
	return e.result(map[string]any{"path": abs, "title": t},
		fmt.Sprintf("已创建 %s\n替换正文、封面和示例图片后，运行 wxctl check %s 检查", abs, path))
}
