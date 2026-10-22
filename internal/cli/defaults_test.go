package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigSetDefaults(t *testing.T) {
	cfgPath := isolate(t)
	for _, kv := range [][2]string{{"author", "菠菜"}, {"open_comment", "false"}, {"fans_comment", "true"}} {
		if code, out, errOut := run(t, "", "--config", cfgPath, "config", "set", kv[0], kv[1]); code != 0 {
			t.Fatalf("set %s: %d %s %s", kv[0], code, out, errOut)
		}
	}
	code, out, _ := run(t, "", "--config", cfgPath, "--json", "config")
	var v struct {
		Defaults struct {
			Author      string `json:"author"`
			OpenComment *bool  `json:"open_comment"`
			FansComment *bool  `json:"fans_comment"`
		} `json:"defaults"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &v) != nil || v.Defaults.Author != "菠菜" ||
		v.Defaults.OpenComment == nil || *v.Defaults.OpenComment || v.Defaults.FansComment == nil || !*v.Defaults.FansComment {
		t.Fatalf("config: %d %s", code, out)
	}

	// 清除、错误的键和值
	if code, _, _ := run(t, "", "--config", cfgPath, "config", "set", "author", ""); code != 0 {
		t.Fatalf("clear author: %d", code)
	}
	if data, _ := os.ReadFile(cfgPath); strings.Contains(string(data), "菠菜") {
		t.Fatalf("author not cleared: %s", data)
	}
	if code, _, _ := run(t, "", "--config", cfgPath, "config", "set", "nope", "x"); code != 2 {
		t.Fatalf("unknown key code=%d", code)
	}
	if code, _, _ := run(t, "", "--config", cfgPath, "config", "set", "open_comment", "maybe"); code != 2 {
		t.Fatalf("bad bool code=%d", code)
	}
	if code, _, _ := run(t, "", "--config", cfgPath, "config", "set", "author", strings.Repeat("长", 17)); code != 2 {
		t.Fatalf("long author code=%d", code)
	}
}

func TestNewByTitleUsesDefaults(t *testing.T) {
	cfgPath := isolate(t)
	os.WriteFile(cfgPath, []byte("defaults:\n  author: 菠菜\n  open_comment: false\n"), 0o600)
	dir := t.TempDir()
	t.Chdir(dir)

	if code, out, errOut := run(t, "", "--config", cfgPath, "new", "我的: 新文章"); code != 0 {
		t.Fatalf("new: %d %s %s", code, out, errOut)
	}
	data, err := os.ReadFile(filepath.Join(dir, "我的- 新文章.md"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := ParseArticle(data, dir)
	if err != nil {
		t.Fatal(err)
	}
	m := a.Meta
	if m.Title != "我的: 新文章" || m.Author != "菠菜" || m.OpenComment == nil || *m.OpenComment ||
		m.FansComment == nil || *m.FansComment {
		t.Fatalf("meta: %+v", m)
	}
	// --author 优先于配置
	if code, _, _ := run(t, "", "--config", cfgPath, "new", "另一篇", "--author", "别人"); code != 0 {
		t.Fatal("new with author")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "另一篇.md")); !strings.Contains(string(data), "author: 别人") {
		t.Fatalf("author flag: %s", data)
	}
}

func TestFormatPlainMarkdown(t *testing.T) {
	cfgPath := isolate(t)
	os.WriteFile(cfgPath, []byte("defaults:\n  author: 菠菜\n"), 0o600)
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "img"), 0o755)
	os.WriteFile(filepath.Join(dir, "img", "a.png"), pngBytes(t, 8, 8, false), 0o644)
	md := filepath.Join(dir, "notes.md")
	os.WriteFile(md, []byte("# 普通文档\n\n第一段\n\n![图](img/a.png)\n"), 0o644)

	if code, out, errOut := run(t, "", "--config", cfgPath, "format", md); code != 0 {
		t.Fatalf("format: %d %s %s", code, out, errOut)
	}
	first, _ := os.ReadFile(md)
	a, err := ParseArticle(first, dir)
	if err != nil {
		t.Fatal(err)
	}
	m := a.Meta
	if m.Title != "普通文档" || m.Author != "菠菜" || m.Cover != "img/a.png" ||
		m.OpenComment == nil || !*m.OpenComment || strings.Contains(a.Markdown, "# 普通文档") ||
		!strings.Contains(a.Markdown, "第一段") {
		t.Fatalf("formatted: %+v\n%s", m, first)
	}
	// 格式化后的文件能通过检查
	if code, out, errOut := run(t, "", "--config", cfgPath, "check", md, "--offline"); code != 0 {
		t.Fatalf("check: %d %s %s", code, out, errOut)
	}

	// 重复执行结果不变；已有字段不被配置覆盖
	if code, _, _ := run(t, "", "--config", cfgPath, "format", md); code != 0 {
		t.Fatal("format again")
	}
	if again, _ := os.ReadFile(md); string(again) != string(first) {
		t.Fatalf("not idempotent:\n%s\n---\n%s", first, again)
	}
	os.WriteFile(cfgPath, []byte("defaults:\n  author: 别人\n"), 0o600)
	run(t, "", "--config", cfgPath, "format", md)
	if data, _ := os.ReadFile(md); !strings.Contains(string(data), "author: 菠菜") {
		t.Fatalf("existing author overwritten: %s", data)
	}

	// -o - 输出到标准输出，不改原文件
	plain := filepath.Join(dir, "plain.md")
	os.WriteFile(plain, []byte("正文\n"), 0o644)
	code, out, _ := run(t, "", "--config", cfgPath, "format", plain, "-o", "-", "--cover", "c.png")
	if code != 0 || !strings.Contains(out, "title: plain") || !strings.Contains(out, "cover: c.png") {
		t.Fatalf("stdout: %d %s", code, out)
	}
	if data, _ := os.ReadFile(plain); string(data) != "正文\n" {
		t.Fatalf("source modified: %s", data)
	}
}

func TestDraftUsesConfigDefaults(t *testing.T) {
	cfgPath := isolate(t)
	os.WriteFile(cfgPath, []byte("defaults:\n  author: 菠菜\n  open_comment: false\n"), 0o600)
	m := &mockAPI{}
	ts := m.server(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "cover.png"), pngBytes(t, 9, 9, false), 0o644)
	md := filepath.Join(dir, "post.md")
	os.WriteFile(md, []byte("---\ntitle: 标题\ncover: cover.png\n---\n正文\n"), 0o644)

	args := []string{"--config", cfgPath, "--url", ts.URL, "--token", testToken, "--json", "draft", md}
	if code, out, errOut := run(t, "", args...); code != 0 {
		t.Fatalf("draft: %d %s %s", code, out, errOut)
	}
	art := m.lastDraft["articles"].([]any)[0].(map[string]any)
	if art["author"] != "菠菜" || art["need_open_comment"] != float64(0) {
		t.Fatalf("article: %v", art)
	}

	// frontmatter 优先于配置
	os.WriteFile(md, []byte("---\ntitle: 标题\nauthor: 我\ncover: cover.png\nopen_comment: true\n---\n正文\n"), 0o644)
	if code, out, errOut := run(t, "", args...); code != 0 {
		t.Fatalf("draft: %d %s %s", code, out, errOut)
	}
	art = m.lastDraft["articles"].([]any)[0].(map[string]any)
	if art["author"] != "我" || art["need_open_comment"] != float64(1) {
		t.Fatalf("article: %v", art)
	}
}
