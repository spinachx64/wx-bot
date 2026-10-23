package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
)

// cmdDoc 单个命令的详细帮助：参数之外的说明、示例、JSON 输出结构
type cmdDoc struct {
	summary  string
	details  string
	examples []string
	output   string // --json 时成功输出的结构
}

var docs = map[string]cmdDoc{
	"login": {
		summary:  "校验并保存 token",
		details:  "先调 /api/v1/ping 校验，通过后写入配置文件（权限 0600）。\n传了 --token 就直接用，否则从标准输入读一行。同时传 --url / --mode 会一并保存。",
		examples: []string{"wxctl login", "echo \"$TOKEN\" | wxctl login", "wxctl --token xxx --url https://wx1.cookup.cn login"},
		output:   `{"ok":true,"config":"/path/config.yaml","url":"https://..."}`,
	},
	"logout": {
		summary:  "从配置文件删除 token",
		examples: []string{"wxctl logout"},
		output:   `{"ok":true}`,
	},
	"config": {
		summary: "显示生效的配置（token 打码），或设置文章默认值",
		details: "不带参数：显示合并了参数、环境变量、配置文件后的最终值，用来排查连错服务或 token 没生效。\n" +
			"config set <键> <值>：写入配置文件。键：author、open_comment、fans_comment（true/false）、theme。\n" +
			"值传空字符串表示清除。new、format、draft 在参数和 frontmatter 都没写时取这些默认值。\n" +
			"theme 可以是服务端主题名，也可以是本地 .css 文件（存成绝对路径）；CSS 每次提交时实时读取、随请求上传，服务端渲染完即丢弃。",
		examples: []string{"wxctl config", "wxctl config set author 菠菜", "wxctl config set theme ~/themes/my.css", "wxctl config set author \"\""},
		output:   `{"ok":true,"config":"...","mode":"local","url":"...","token":"abcd****wxyz","theme":"","cache_dir":"...","defaults":{"author":"","open_comment":null,"fans_comment":null}}`,
	},
	"ping": {
		summary:  "检查服务是否可达、token 是否有效",
		examples: []string{"wxctl ping"},
		output:   `{"ok":true,"url":"https://..."}`,
	},
	"themes": {
		summary:  "列出服务端可用主题",
		details:  "文本输出中 * 标记默认主题。warnings 是主题 CSS 里被跳过的规则。",
		examples: []string{"wxctl themes"},
		output:   `{"ok":true,"default":"summer-breeze","themes":[{"name":"summer-breeze","label":"夏日海风","warnings":null}]}`,
	},
	"new": {
		summary: "创建带 frontmatter 的文章模板",
		details: "参数是标题时，在当前目录生成 <标题>.md；以 .md 结尾或带目录时按路径处理（标题取文件名）。\n" +
			"作者、评论设置取配置 defaults（wxctl config set 设置）。不覆盖已有文件（除非 --force）；传 - 输出到标准输出。\n" +
			"模板里的示例文字和 images/example.png 没替换时，format 会报错。",
		examples: []string{"wxctl new 我的新文章", "wxctl new wx/post.md --title 标题 --author 作者", "wxctl new - > post.md"},
		output:   `{"ok":true,"path":"/abs/我的新文章.md","title":"我的新文章"}`,
	},
	"format": {
		summary: "整理成带完整 frontmatter 的文章，并做提交前检查",
		details: "已有 frontmatter 字段保留；没写的取配置 defaults；开头的 # 标题移到 title，否则用文件名。\n" +
			"封面：--cover > frontmatter cover > 同目录 cover.* > 正文第一张图。\n" +
			"默认原地改写（可重复执行），-o 写到另一个文件，-o - 输出到标准输出（检查结果写 stderr）。\n" +
			"整理后检查结果，不上传任何东西。错误（error）会导致 draft 失败，必须修；警告（warning）只是提醒。\n" +
			"检查项：标题/作者/摘要长度（32/16/128 字）、封面和正文图片是否存在且能压到限制以内、\n" +
			"模板残留、主题名、阅读原文链接、``` 是否成对、未知 frontmatter 字段、\n" +
			"HTML 标签（会按文字显示）、外部链接（公众号里不能点）、正文里重复的一级标题。\n" +
			"退出码：0 通过；1 有错误（或 --strict 时有警告），文件仍会写出。--no-check 只整理不检查。",
		examples: []string{"wxctl format notes.md", "wxctl format notes.md -o post.md --cover cover.png", "wxctl format post.md --strict", "wxctl --json format post.md --offline"},
		output:   `{"ok":false,"path":"/abs/notes.md","title":"...","cover":"cover.png","issues":[{"level":"error","line":5,"field":"cover","message":"图片不存在：..."}]}`,
	},
	"render": {
		summary:  "把 markdown 渲染成公众号 HTML（不建草稿）",
		details:  "默认不上传图片，本地图片地址原样保留；加 --upload-images 会先上传并替换成微信地址。\n文本模式只输出 HTML，方便重定向到文件。",
		examples: []string{"wxctl render post.md > post.html", "wxctl render post.md --theme summer-breeze --upload-images"},
		output:   `{"ok":true,"title":"...","html":"<section ...>"}`,
	},
	"draft": {
		summary: "上传图片和封面，新建或更新草稿",
		details: "流程：检查（与 format 相同，有错误就停止，什么都不上传）→ 上传正文图片并替换地址 → 上传封面 → 新建/更新草稿。\n" +
			"有 --media-id 或 frontmatter media_id 时更新，否则新建。新建必须有封面（cover / --cover / --thumb）。\n" +
			"图片按内容 hash 缓存，重复提交不会重复上传。参数优先于 frontmatter。\n" +
			"文件提交成功后自动把 media_id 写回原文件的 frontmatter，下次提交会更新同一篇；标准输入不回写。",
		examples: []string{
			"wxctl draft post.md",
			"wxctl draft post.md --media-id <media_id>",
			"wxctl draft post.md --thumb <永久素材 media_id>",
			"cat post.md | wxctl --mode server draft - --cover /data/cover.png",
		},
		output: `{"ok":true,"action":"created|updated","media_id":"...","title":"...","thumb_media_id":"...","publish_id":"（仅 --publish）"}`,
	},
	"drafts": {
		summary:  "列出草稿的 media_id 和标题",
		details:  "按微信返回的顺序（最近更新在前）。count 1-20，用 --offset 翻页。多图文草稿的标题用 | 连接。",
		examples: []string{"wxctl drafts", "wxctl drafts --offset 20 --count 20"},
		output:   `{"ok":true,"total":1,"drafts":[{"media_id":"...","title":"..."}]}`,
	},
	"publish": {
		summary:  "发布草稿（对粉丝可见，不可撤回）",
		details:  "发布是异步的，返回 publish_id，再用 status 查询结果。会改动线上数据，执行前请确认。",
		examples: []string{"wxctl publish <media_id>"},
		output:   `{"ok":true,"media_id":"...","publish_id":"..."}`,
	},
	"status": {
		summary:  "查询发布状态",
		details:  "publish_status：0 成功，1 发布中（稍后再查），2 原创失败，3 常规失败，4 审核不通过，5 成功后被删除，6 成功后被封禁。",
		examples: []string{"wxctl status <publish_id>"},
		output:   `{"ok":true,"publish_id":"...","publish_status":0,"status_text":"成功","article_id":"...","urls":["https://mp.weixin.qq.com/..."]}`,
	},
	"upload": {
		summary:  "单独上传图片",
		details:  "默认上传为正文图片，返回 mmbiz.qpic.cn 地址（不占素材名额）；--cover 上传为永久图片素材，返回 media_id，可作 draft --thumb。",
		examples: []string{"wxctl upload a.png", "wxctl upload cover.jpg --cover"},
		output:   `{"ok":true,"url":"http://mmbiz.qpic.cn/..."} 或 {"ok":true,"media_id":"..."}`,
	},
}

// cmdOrder help 里的命令顺序
var cmdOrder = []string{"login", "logout", "config", "ping", "themes", "new", "format", "render", "draft", "drafts", "publish", "status", "upload"}

const agentGuide = `# wxctl 使用指南（给 agent）

wxctl 把本地 markdown 发到微信公众号草稿箱。服务端负责渲染主题和调用微信接口，
wxctl 负责读文件、上传图片、组装请求。

## 调用约定
- 始终加 --json：成功和失败都输出一行 JSON 到 stdout，字段 ok 表示成败。
  失败时有 error，服务端/微信错误还会带 status、errcode、errmsg。
- 进度日志（上传了哪些图）写在 stderr，解析结果只看 stdout。
- 退出码：0 成功；1 执行失败（网络、服务端、微信错误、format 检查不通过）；2 参数用法错误。
- 文章路径可以写 -，从标准输入读；此时图片相对路径以当前目录为基准。
- 服务器上用 --mode server（默认连 http://127.0.0.1:1240，token 读 WXBOT_TOKEN 或 GZH_TOKEN）。

## 推荐流程
1. wxctl --json ping                      确认服务和 token 可用
2. wxctl --json new post.md --title 标题  （新文章）生成模板，再写入正文、封面
3. wxctl --json format post.md            补全 frontmatter 并检查；有 error 就按 line/message 修改，
                                          重复执行直到 ok:true（已有 markdown 也从这一步开始）
4. wxctl --json draft post.md             新建草稿，media_id 自动写回原文件
5. 之后修改再执行 draft 就是更新同一篇；stdin 输入需自行保存 media_id
6. wxctl --json drafts                    需要时核对草稿列表

## frontmatter 字段
title（≤32 字）、author（≤16 字）、digest（≤128 字）、cover（封面路径或 URL，新建必填）、
theme（主题名，或本地 .css 文件，相对路径以文章所在目录为准）、source_url、media_id、open_comment、fans_comment。
没有 title 时用正文开头的 # 标题（并从正文移除），再没有就用文件名。

## 写作注意
- 图片只用 ![说明](路径) 语法，HTML 标签会按文字原样显示。
- 外部链接在公众号里不能点击，需要的话写成文字或放到 source_url。
- 本地、外链图片都会自动上传并压缩（正文图 <1MB，封面 <10MB）。

## 需要先征得用户同意的操作
- publish、draft --publish：发布后粉丝可见，不能撤回。
- login / logout：会改写用户的配置文件。
`

// helpText 返回总览、某个命令或 agent 指南的帮助文本；未知主题返回 false
func helpText(topic string) (string, bool) {
	switch topic {
	case "":
		return usage, true
	case "agent", "agents":
		return agentGuide, true
	}
	d, ok := docs[topic]
	if !ok {
		return "", false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "wxctl %s — %s\n\n", topic, d.summary)
	b.WriteString(commandFlags(topic))
	if d.details != "" {
		b.WriteString("\n说明:\n")
		for _, l := range strings.Split(d.details, "\n") {
			b.WriteString("  " + l + "\n")
		}
	}
	if len(d.examples) > 0 {
		b.WriteString("\n示例:\n")
		for _, ex := range d.examples {
			b.WriteString("  " + ex + "\n")
		}
	}
	if d.output != "" {
		b.WriteString("\n--json 输出:\n  " + d.output + "\n")
	}
	b.WriteString("\n全局参数（--json、--mode、--url、--token、--config）放在命令前，见 wxctl help。\n")
	return b.String(), true
}

// commandFlags 用 -h 跑一次命令拿到它的参数列表；-h 在解析阶段返回，不会读配置或联网
func commandFlags(name string) string {
	var buf bytes.Buffer
	e := &env{ctx: context.Background(), cfg: &Config{}, client: &Client{}, in: strings.NewReader(""), out: io.Discard, errw: &buf}
	commands[name](e, []string{"-h"})
	return buf.String()
}

func cmdHelp(args []string, stdout, stderr io.Writer) int {
	// 忽略 --json 等参数，help 输出始终是文本
	topic := ""
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			topic = a
			break
		}
	}
	text, ok := helpText(topic)
	if !ok {
		names := append([]string{}, cmdOrder...)
		sort.Strings(names)
		fmt.Fprintf(stderr, "unknown help topic %q, available: agent, %s\n", topic, strings.Join(names, ", "))
		return 2
	}
	fmt.Fprint(stdout, text)
	return 0
}
