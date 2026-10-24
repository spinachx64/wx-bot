package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const usage = `wxctl — wx-bot 命令行

用法:
  wxctl [全局参数] <命令> [参数]

全局参数（优先级：参数 > 环境变量 > 配置文件 > 默认值）:
  --config <path>   配置文件，默认 ~/.config/wx-bot/config.yaml（WXBOT_CONFIG）
  --mode <mode>     local（默认）或 server（WXBOT_MODE）
  --url <url>       服务地址，local 默认 https://wx1.cookup.cn，
                    server 默认 http://127.0.0.1:1240（WXBOT_URL）
  --token <token>   访问 token（WXBOT_TOKEN；server 模式还会读 GZH_TOKEN）
  --json            输出 JSON（server 模式默认开启）

命令:
  login                 校验并保存 token（读 --token，否则从标准输入读一行）
  logout                从配置文件删除 token
  config                显示生效的配置
  config set <键> <值>  设置文章默认值：author、open_comment、fans_comment、theme
  ping                  检查服务和 token
  themes                列出可用主题
  new    <标题|file.md|-> 创建带 frontmatter 的文章模板，默认值取自配置
  format <md|->         整理成带 frontmatter 的文章并做提交前检查（-o 指定输出，--strict 警告也算失败）
  render <md|->         渲染 markdown 为 HTML
  draft  <md|->         上传图片，新建或更新草稿（--type news|newspic，默认 news）
  drafts                列出草稿的 media_id 和标题（--offset / --count）
  publish <media_id>    发布草稿
  status <publish_id>   查询发布状态
  upload <image>        上传正文图片；加 --cover 上传为永久素材

  help [命令|agent]     查看命令详情（参数、示例、JSON 输出）；agent 为自动化调用指南

各命令的详细帮助：wxctl help <命令> 或 wxctl <命令> -h。
给 agent / 脚本用：先看 wxctl help agent，调用时始终加 --json。
退出码：0 成功，1 执行失败，2 参数错误。
`

type env struct {
	ctx    context.Context
	cfg    *Config
	flags  Flags
	client *Client
	json   bool
	in     io.Reader
	out    io.Writer
	errw   io.Writer
}

var commands = map[string]func(*env, []string) error{
	"login":   cmdLogin,
	"logout":  cmdLogout,
	"config":  cmdConfig,
	"ping":    cmdPing,
	"themes":  cmdThemes,
	"render":  cmdRender,
	"draft":   cmdDraft,
	"drafts":  cmdDrafts,
	"new":     cmdNew,
	"format":  cmdFormat,
	"publish": cmdPublish,
	"status":  cmdStatus,
	"upload":  cmdUpload,
}

// Run 执行命令并返回退出码：0 成功，1 执行失败，2 用法错误
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("wxctl", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	var f Flags
	var asJSON bool
	fs.StringVar(&f.Config, "config", "", "")
	fs.StringVar(&f.Mode, "mode", "", "")
	fs.StringVar(&f.URL, "url", "", "")
	fs.StringVar(&f.Token, "token", "", "")
	fs.BoolVar(&asJSON, "json", false, "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// 主动请求的帮助写 stdout，方便 agent 读取
			fmt.Fprint(stdout, usage)
			return 0
		}
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fs.Usage()
		return 2
	}
	if rest[0] == "help" {
		return cmdHelp(rest[1:], stdout, stderr)
	}
	// <命令> -h 显示带说明、示例和 JSON 结构的完整帮助
	if _, ok := docs[rest[0]]; ok && wantsHelp(rest[1:]) {
		return cmdHelp(rest[:1], stdout, stderr)
	}
	cmd, ok := commands[rest[0]]
	if !ok {
		fmt.Fprintf(stderr, "unknown command %q\n\n", rest[0])
		fs.Usage()
		return 2
	}

	cfg, err := LoadConfig(f)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	if w := cfg.CheckPerm(); w != "" {
		fmt.Fprintln(stderr, w)
	}
	e := &env{
		ctx: ctx, cfg: cfg, flags: f, client: NewClient(cfg),
		json: asJSON || cfg.Mode == ModeServer,
		in:   stdin, out: stdout, errw: stderr,
	}
	if err := cmd(e, rest[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		if errors.Is(err, errReported) {
			return 1
		}
		var ue usageError
		if errors.As(err, &ue) {
			fmt.Fprintln(stderr, "error:", err)
			return 2
		}
		e.fail(err)
		return 1
	}
	return 0
}

func wantsHelp(args []string) bool {
	for _, a := range args {
		switch a {
		case "-h", "-help", "--help", "--h":
			return true
		case "--":
			return false
		}
	}
	return false
}

type usageError string

func (u usageError) Error() string { return string(u) }

// result 输出成功结果：JSON 模式输出对象，否则输出 text
func (e *env) result(v map[string]any, text string) error {
	if !e.json {
		fmt.Fprintln(e.out, text)
		return nil
	}
	v["ok"] = true
	return e.writeJSON(v)
}

func (e *env) writeJSON(v any) error {
	enc := json.NewEncoder(e.out)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// fail JSON 模式把错误写到 stdout 方便调用方解析，否则写 stderr
func (e *env) fail(err error) {
	if !e.json {
		fmt.Fprintln(e.errw, "error:", err)
		return
	}
	v := map[string]any{"ok": false, "error": err.Error()}
	var ae *APIError
	if errors.As(err, &ae) {
		v["status"], v["errcode"], v["errmsg"] = ae.Status, ae.Code, ae.Msg
	}
	json.NewEncoder(e.out).Encode(v)
}

func (e *env) logf(format string, args ...any) {
	fmt.Fprintf(e.errw, format+"\n", args...)
}

func newFlagSet(e *env, name, args string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(e.errw)
	fs.Usage = func() {
		fmt.Fprintf(e.errw, "用法: wxctl %s %s\n", name, args)
		fs.PrintDefaults()
	}
	return fs
}

// parseArgs 允许参数和位置参数混排，如 draft a.md --publish
func parseArgs(fs *flag.FlagSet, args []string, npos int) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
	if len(pos) != npos {
		fs.Usage()
		return nil, usageError(fmt.Sprintf("%s: expected %d argument(s), got %d", fs.Name(), npos, len(pos)))
	}
	return pos, nil
}

// ---------- 配置与 token ----------

func cmdLogin(e *env, args []string) error {
	fs := newFlagSet(e, "login", "")
	if _, err := parseArgs(fs, args, 0); err != nil {
		return err
	}
	token := e.cfg.Token
	if e.flags.Token == "" {
		fmt.Fprint(e.errw, "Token: ")
		line, err := bufio.NewReader(e.in).ReadString('\n')
		if err != nil && line == "" {
			return fmt.Errorf("read token: %w", err)
		}
		token = strings.TrimSpace(line)
	}
	if token == "" {
		return errors.New("empty token")
	}
	probe := *e.cfg
	probe.Token = token
	if err := NewClient(&probe).Ping(e.ctx); err != nil {
		return fmt.Errorf("verify token against %s: %w", e.cfg.URL, err)
	}
	err := e.cfg.Save(func(f *Config) {
		f.Token = token
		if e.flags.URL != "" {
			f.URL = e.cfg.URL
		}
		if e.flags.Mode != "" {
			f.Mode = e.cfg.Mode
		}
	})
	if err != nil {
		return err
	}
	return e.result(map[string]any{"config": e.cfg.Path(), "url": e.cfg.URL},
		fmt.Sprintf("token 已验证并保存到 %s", e.cfg.Path()))
}

func cmdLogout(e *env, args []string) error {
	fs := newFlagSet(e, "logout", "")
	if _, err := parseArgs(fs, args, 0); err != nil {
		return err
	}
	if err := e.cfg.Save(func(f *Config) { f.Token = "" }); err != nil {
		return err
	}
	return e.result(map[string]any{}, "token 已从 "+e.cfg.Path()+" 删除")
}

func cmdConfig(e *env, args []string) error {
	fs := newFlagSet(e, "config", "[set <键> <值>]")
	if len(args) > 0 && args[0] == "set" {
		// 值为空字符串表示清除，如 wxctl config set author ""
		pos, err := parseArgs(fs, args[1:], 2)
		if err != nil {
			return err
		}
		// 先在副本上校验，值不合法时不改动配置文件
		probe := *e.cfg.file
		if err := probe.setKey(pos[0], pos[1]); err != nil {
			return err
		}
		if err := e.cfg.Save(func(f *Config) { *f = probe }); err != nil {
			return err
		}
		return e.result(map[string]any{"key": pos[0], "value": pos[1], "config": e.cfg.Path()},
			fmt.Sprintf("已设置 %s = %q（%s）", pos[0], pos[1], e.cfg.Path()))
	}
	if _, err := parseArgs(fs, args, 0); err != nil {
		return err
	}
	c := e.cfg
	d := c.Defaults
	boolText := func(b *bool, def string) string {
		if b == nil {
			return def
		}
		return strconv.FormatBool(*b)
	}
	v := map[string]any{
		"config": c.Path(), "mode": c.Mode, "url": c.URL,
		"token": maskToken(c.Token), "theme": c.Theme, "cache_dir": c.CacheDir, "defaults": d,
	}
	text := fmt.Sprintf("config:    %s\nmode:      %s\nurl:       %s\ntoken:     %s\ntheme:     %s\ncache_dir: %s\n"+
		"defaults:\n  author:       %s\n  open_comment: %s\n  fans_comment: %s",
		c.Path(), c.Mode, c.URL, first(maskToken(c.Token), "(未设置)"), first(c.Theme, "(服务端默认)"), c.CacheDir,
		first(d.Author, "(未设置)"), boolText(d.OpenComment, "(未设置，服务端默认 true)"), boolText(d.FansComment, "(未设置，默认 false)"))
	return e.result(v, text)
}

func cmdPing(e *env, args []string) error {
	fs := newFlagSet(e, "ping", "")
	if _, err := parseArgs(fs, args, 0); err != nil {
		return err
	}
	if err := e.client.Ping(e.ctx); err != nil {
		return err
	}
	return e.result(map[string]any{"url": e.cfg.URL}, "ok "+e.cfg.URL)
}

func cmdThemes(e *env, args []string) error {
	fs := newFlagSet(e, "themes", "")
	if _, err := parseArgs(fs, args, 0); err != nil {
		return err
	}
	var out struct {
		Default string `json:"default"`
		Themes  []struct {
			Name     string   `json:"name"`
			Label    string   `json:"label"`
			Warnings []string `json:"warnings"`
		} `json:"themes"`
	}
	if err := e.client.getJSON(e.ctx, "/api/v1/themes", &out); err != nil {
		return err
	}
	var b strings.Builder
	for _, s := range out.Themes {
		mark := " "
		if s.Name == out.Default {
			mark = "*"
		}
		fmt.Fprintf(&b, "%s %-20s %s", mark, s.Name, s.Label)
		if n := len(s.Warnings); n > 0 {
			fmt.Fprintf(&b, "（%d 条规则被跳过）", n)
		}
		b.WriteByte('\n')
	}
	return e.result(map[string]any{"default": out.Default, "themes": out.Themes}, strings.TrimRight(b.String(), "\n"))
}
