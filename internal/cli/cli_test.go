package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const testToken = "tk-123456789"

// mockAPI 模拟 wx-bot 服务，记录上传次数和最后一次草稿请求
type mockAPI struct {
	mu        sync.Mutex
	uploads   int
	covers    int
	lastDraft map[string]any
	draftPath string
}

func (m *mockAPI) server(t *testing.T) *httptest.Server {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"errcode":-1,"errmsg":"unauthorized"}`))
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		switch {
		case r.URL.Path == "/api/v1/ping":
			w.Write([]byte(`{"errcode":0}`))
		case r.URL.Path == "/api/v1/materials/images":
			m.uploads++
			w.Write([]byte(`{"url":"http://mmbiz.qpic.cn/img/` + string(rune('0'+m.uploads)) + `"}`))
		case r.URL.Path == "/api/v1/materials":
			m.covers++
			if r.FormValue("type") != "image" {
				t.Errorf("cover type = %q", r.FormValue("type"))
			}
			w.Write([]byte(`{"media_id":"THUMB"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/drafts":
			if q := r.URL.Query(); q.Get("no_content") != "true" || q.Get("offset") != "2" || q.Get("count") != "5" {
				t.Errorf("drafts query = %s", r.URL.RawQuery)
			}
			w.Write([]byte(`{"total_count":3,"item_count":2,"item":[
				{"media_id":"M1","content":{"news_item":[{"title":"第一篇"}]},"update_time":1},
				{"media_id":"M2","content":{"news_item":[{"title":"甲"},{"title":"乙"}]},"update_time":2}]}`))
		case strings.HasPrefix(r.URL.Path, "/api/v1/drafts") && strings.HasSuffix(r.URL.Path, "/publish"):
			w.Write([]byte(`{"errcode":0,"publish_id":2247483}`))
		case strings.HasPrefix(r.URL.Path, "/api/v1/drafts"):
			m.draftPath = r.Method + " " + r.URL.Path
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			m.lastDraft = body
			w.Write([]byte(`{"media_id":"DRAFT1"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func pngBytes(t *testing.T, w, h int, noise bool) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rnd := rand.New(rand.NewSource(1))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{200, 100, 50, 255}
			if noise {
				c = color.RGBA{uint8(rnd.Intn(256)), uint8(rnd.Intn(256)), uint8(rnd.Intn(256)), 255}
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// isolate 清掉会影响配置的环境变量
func isolate(t *testing.T) string {
	for _, k := range []string{"WXBOT_CONFIG", "WXBOT_MODE", "WXBOT_URL", "WXBOT_TOKEN", "WXBOT_SKIN", "GZH_TOKEN"} {
		t.Setenv(k, "")
	}
	return filepath.Join(t.TempDir(), "config.yaml")
}

func run(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errw bytes.Buffer
	code := Run(context.Background(), args, strings.NewReader(stdin), &out, &errw)
	return code, out.String(), errw.String()
}

func TestConfigPrecedence(t *testing.T) {
	cfgPath := isolate(t)
	os.WriteFile(cfgPath, []byte("url: https://file.example\ntoken: filetoken\nskin: a\n"), 0o600)

	c, err := LoadConfig(Flags{Config: cfgPath})
	if err != nil {
		t.Fatal(err)
	}
	if c.Mode != ModeLocal || c.URL != "https://file.example" || c.Token != "filetoken" || c.Skin != "a" {
		t.Fatalf("file values: %+v", c)
	}

	t.Setenv("WXBOT_TOKEN", "envtoken")
	c, _ = LoadConfig(Flags{Config: cfgPath, Token: "flagtoken"})
	if c.Token != "flagtoken" {
		t.Fatalf("flag should win: %s", c.Token)
	}
	c, _ = LoadConfig(Flags{Config: cfgPath})
	if c.Token != "envtoken" {
		t.Fatalf("env should beat file: %s", c.Token)
	}

	// server 模式默认地址为本机，并回退到 GZH_TOKEN
	os.WriteFile(cfgPath, []byte("mode: server\n"), 0o600)
	t.Setenv("WXBOT_TOKEN", "")
	t.Setenv("GZH_TOKEN", "svc")
	c, _ = LoadConfig(Flags{Config: cfgPath})
	if c.URL != defaultServerURL || c.Token != "svc" {
		t.Fatalf("server defaults: %+v", c)
	}

	if _, err := LoadConfig(Flags{Config: cfgPath, Mode: "bad"}); err == nil {
		t.Fatal("bad mode should error")
	}
}

func TestLoginSavesOnlyFileValues(t *testing.T) {
	cfgPath := isolate(t)
	ts := (&mockAPI{}).server(t)
	t.Setenv("WXBOT_SKIN", "from-env")

	code, _, errOut := run(t, "wrong\n", "--config", cfgPath, "--url", ts.URL, "login")
	if code != 1 {
		t.Fatalf("wrong token should fail, code=%d %s", code, errOut)
	}
	if _, err := os.Stat(cfgPath); err == nil {
		t.Fatal("config written for invalid token")
	}

	code, _, errOut = run(t, testToken+"\n", "--config", cfgPath, "--url", ts.URL, "login")
	if code != 0 {
		t.Fatalf("login: %d %s", code, errOut)
	}
	st, _ := os.Stat(cfgPath)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %v", st.Mode().Perm())
	}
	data, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(data), testToken) || !strings.Contains(string(data), ts.URL) {
		t.Fatalf("config: %s", data)
	}
	if strings.Contains(string(data), "from-env") {
		t.Fatalf("env value persisted: %s", data)
	}

	// 之后的命令直接用配置文件里的 token 和地址
	if code, out, errOut := run(t, "", "--config", cfgPath, "ping"); code != 0 || !strings.Contains(out, "ok") {
		t.Fatalf("ping: %d %s %s", code, out, errOut)
	}
	if code, out, _ := run(t, "", "--config", cfgPath, "config"); code != 0 || strings.Contains(out, testToken) {
		t.Fatalf("config must mask token: %s", out)
	}

	run(t, "", "--config", cfgPath, "logout")
	data, _ = os.ReadFile(cfgPath)
	if strings.Contains(string(data), testToken) {
		t.Fatalf("logout kept token: %s", data)
	}
}

func TestParseArticle(t *testing.T) {
	a, err := ParseArticle([]byte("---\ntitle: 标题A\ncover: c.png\nopen_comment: true\n---\n# 正文标题\n\n内容\n"), "/x")
	if err != nil {
		t.Fatal(err)
	}
	if a.Meta.Title != "标题A" || a.Meta.Cover != "c.png" || a.Meta.OpenComment == nil || !*a.Meta.OpenComment {
		t.Fatalf("meta: %+v", a.Meta)
	}
	if !strings.Contains(a.Markdown, "# 正文标题") { // 有 frontmatter 标题时保留正文 H1
		t.Fatalf("body: %q", a.Markdown)
	}

	a, _ = ParseArticle([]byte("# 来自 H1\n\n内容\n"), "/x")
	if a.Meta.Title != "来自 H1" || strings.Contains(a.Markdown, "来自 H1") {
		t.Fatalf("h1 title: %+v %q", a.Meta, a.Markdown)
	}

	if _, err := ParseArticle([]byte("---\ntitle: x\n---\n"), "/x"); err == nil {
		t.Fatal("empty body should error")
	}
}

func TestImagesSkipCode(t *testing.T) {
	md := "![a](img/a.png)\n\n```md\n![x](code.png)\n```\n\n`![y](inline.png)` ![b](<my pic.png> \"t\")\n![a2](img/a.png)\n"
	a, _ := ParseArticle([]byte(md), "/x")
	got := a.Images()
	if len(got) != 2 || got[0] != "img/a.png" || got[1] != "my pic.png" {
		t.Fatalf("images: %v", got)
	}
	a.ReplaceImages(func(s string) string { return "http://mmbiz.qpic.cn/" + strings.ReplaceAll(s, " ", "_") })
	for _, want := range []string{"![a](<http://mmbiz.qpic.cn/img/a.png>)", `![b](<http://mmbiz.qpic.cn/my_pic.png> "t")`, "![x](code.png)", "`![y](inline.png)`"} {
		if !strings.Contains(a.Markdown, want) {
			t.Fatalf("missing %q in %s", want, a.Markdown)
		}
	}
}

func TestFitImage(t *testing.T) {
	small := pngBytes(t, 10, 10, false)
	out, name, err := fitImage(small, "a.png", maxContentImage)
	if err != nil || !bytes.Equal(out, small) || name != "a.png" {
		t.Fatalf("small png should pass through: %v %s", err, name)
	}

	big := pngBytes(t, 1200, 1200, true) // 随机噪点，PNG 远超 1MB
	if len(big) <= maxContentImage {
		t.Fatalf("test image too small: %d", len(big))
	}
	out, name, err = fitImage(big, "big.png", maxContentImage)
	if err != nil {
		t.Fatal(err)
	}
	// PNG 优先缩小宽度并保留格式，缩不下去才转 jpeg
	ct := http.DetectContentType(out)
	if len(out) > maxContentImage || (ct != "image/png" && ct != "image/jpeg") || name != withExt("big.png", ct) {
		t.Fatalf("not compressed: %d %s %s", len(out), name, ct)
	}

	// 非 jpg/png（gif）必须转码
	var gbuf bytes.Buffer
	if err := gif.Encode(&gbuf, image.NewPaletted(image.Rect(0, 0, 4, 4), color.Palette{color.White}), nil); err != nil {
		t.Fatal(err)
	}
	out, name, err = fitImage(gbuf.Bytes(), "a.gif", maxContentImage)
	if err != nil || http.DetectContentType(out) != "image/jpeg" || name != "a.jpg" {
		t.Fatalf("gif should become jpeg: %v %s", err, name)
	}

	if _, _, err := fitImage([]byte("not an image"), "x.txt", maxContentImage); err == nil {
		t.Fatal("non-image should error")
	}
}

func TestDraftEndToEnd(t *testing.T) {
	cfgPath := isolate(t)
	m := &mockAPI{}
	ts := m.server(t)

	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "images"), 0o755)
	os.WriteFile(filepath.Join(dir, "images", "a.png"), pngBytes(t, 8, 8, false), 0o644)
	os.WriteFile(filepath.Join(dir, "cover.png"), pngBytes(t, 9, 9, false), 0o644)
	md := "---\ntitle: 测试文章\nauthor: 我\ncover: cover.png\nskin: summer-breeze\n---\n正文\n\n![图](images/a.png)\n\n![再次](./images/a.png)\n\n![已是微信](http://mmbiz.qpic.cn/keep)\n"
	mdPath := filepath.Join(dir, "post.md")
	os.WriteFile(mdPath, []byte(md), 0o644)

	args := []string{"--config", cfgPath, "--url", ts.URL, "--token", testToken, "--json"}
	code, out, errOut := run(t, "", append(args, "draft", mdPath, "--publish")...)
	if code != 0 {
		t.Fatalf("draft: %d %s %s", code, out, errOut)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("json output: %s", out)
	}
	if res["ok"] != true || res["media_id"] != "DRAFT1" || res["action"] != "created" || res["publish_id"] != "2247483" {
		t.Fatalf("result: %v", res)
	}
	// 同一文件两种写法只上传一次（内容 hash 缓存），微信地址不上传
	if m.uploads != 1 || m.covers != 1 {
		t.Fatalf("uploads=%d covers=%d", m.uploads, m.covers)
	}
	art := m.lastDraft["articles"].([]any)[0].(map[string]any)
	body := art["markdown"].(string)
	if strings.Contains(body, "images/a.png") || !strings.Contains(body, "http://mmbiz.qpic.cn/keep") ||
		strings.Count(body, "http://mmbiz.qpic.cn/img/1") != 2 {
		t.Fatalf("markdown: %s", body)
	}
	if art["title"] != "测试文章" || art["author"] != "我" || art["thumb_media_id"] != "THUMB" || art["skin"] != "summer-breeze" {
		t.Fatalf("article: %v", art)
	}

	saved, err := LoadArticle(mdPath)
	if err != nil || saved.Meta.MediaID != "DRAFT1" {
		t.Fatalf("media_id not saved: %v %v", saved, err)
	}
	code, out, errOut = run(t, "", append(args, "draft", mdPath)...)
	if code != 0 || m.draftPath != "PUT /api/v1/drafts/DRAFT1" {
		t.Fatalf("automatic update: %d %s %s, %s", code, out, errOut, m.draftPath)
	}

	// 再次提交走缓存不重复上传；带 --media-id 走更新
	code, out, errOut = run(t, "", append(args, "draft", mdPath, "--media-id", "MID9")...)
	if code != 0 {
		t.Fatalf("update: %d %s %s", code, out, errOut)
	}
	if m.uploads != 1 || m.covers != 1 {
		t.Fatalf("cache miss: uploads=%d covers=%d", m.uploads, m.covers)
	}
	if m.draftPath != "PUT /api/v1/drafts/MID9" || m.lastDraft["index"] != float64(0) {
		t.Fatalf("update request: %s %v", m.draftPath, m.lastDraft)
	}
}

func TestDraftErrors(t *testing.T) {
	cfgPath := isolate(t)
	ts := (&mockAPI{}).server(t)
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "p.md")

	// 缺封面是用法错误，退出码 2
	os.WriteFile(mdPath, []byte("# 标题\n\n正文\n"), 0o644)
	if code, _, _ := run(t, "", "--config", cfgPath, "--url", ts.URL, "--token", testToken, "draft", mdPath); code != 2 {
		t.Fatalf("missing cover code=%d", code)
	}

	// 图片不存在：server 模式下错误以 JSON 写到 stdout
	os.WriteFile(mdPath, []byte("# 标题\n\n![x](nope.png)\n"), 0o644)
	code, out, _ := run(t, "", "--config", cfgPath, "--mode", "server", "--url", ts.URL, "--token", testToken,
		"draft", mdPath, "--thumb", "T")
	var res map[string]any
	if code != 1 || json.Unmarshal([]byte(out), &res) != nil || res["ok"] != false {
		t.Fatalf("server-mode error: %d %s", code, out)
	}

	// 未登录
	if code, _, errOut := run(t, "", "--config", cfgPath, "--url", ts.URL, "ping"); code != 1 || !strings.Contains(errOut, "wxctl login") {
		t.Fatalf("no token: %d %s", code, errOut)
	}
}

func TestStdinArticle(t *testing.T) {
	cfgPath := isolate(t)
	m := &mockAPI{}
	ts := m.server(t)
	code, out, errOut := run(t, "---\ntitle: 管道\n---\n正文\n", "--config", cfgPath, "--url", ts.URL, "--token", testToken,
		"--json", "draft", "-", "--thumb", "T0")
	if code != 0 || !strings.Contains(out, `"media_id":"DRAFT1"`) {
		t.Fatalf("stdin draft: %d %s %s", code, out, errOut)
	}
	if m.covers != 0 {
		t.Fatal("--thumb should skip cover upload")
	}
}

var _ = io.Discard

func TestDraftsList(t *testing.T) {
	cfgPath := isolate(t)
	ts := (&mockAPI{}).server(t)
	base := []string{"--config", cfgPath, "--url", ts.URL, "--token", testToken}

	code, out, errOut := run(t, "", append(base, "drafts", "--offset", "2", "--count", "5")...)
	if code != 0 {
		t.Fatalf("drafts: %d %s %s", code, out, errOut)
	}
	for _, want := range []string{"M1  第一篇", "M2  甲 | 乙", "共 3 篇，本页 2 篇"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %s", want, out)
		}
	}

	code, out, _ = run(t, "", append(base, "--json", "drafts", "--offset", "2", "--count", "5")...)
	var v struct {
		OK     bool `json:"ok"`
		Total  int  `json:"total"`
		Drafts []struct {
			MediaID string `json:"media_id"`
			Title   string `json:"title"`
		} `json:"drafts"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &v) != nil || !v.OK || v.Total != 3 || len(v.Drafts) != 2 || v.Drafts[1].Title != "甲 | 乙" {
		t.Fatalf("drafts json: %d %s", code, out)
	}

	if code, _, _ := run(t, "", append(base, "drafts", "--count", "50")...); code != 2 {
		t.Fatalf("bad count should be usage error, got %d", code)
	}
}

func TestNewTemplateFailsCheckUntilFilled(t *testing.T) {
	cfgPath := isolate(t)
	dir := t.TempDir()
	md := filepath.Join(dir, "post.md")
	if code, out, errOut := run(t, "", "--config", cfgPath, "new", md, "--author", "我"); code != 0 {
		t.Fatalf("new: %d %s %s", code, out, errOut)
	}
	if code, _, _ := run(t, "", "--config", cfgPath, "new", md); code != 2 {
		t.Fatalf("existing file should not be overwritten, got %d", code)
	}

	// 刚生成的模板：示例文字、示例图片、缺封面都要报错
	code, out, _ := run(t, "", "--config", cfgPath, "--json", "format", md, "--offline")
	if code != 1 || !strings.Contains(out, `"ok":false`) {
		t.Fatalf("raw template should fail format check, got %d: %s", code, out)
	}
	for _, want := range []string{"模板示例文字", "模板示例图片", "图片不存在"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %s", want, out)
		}
	}

	// 填好后通过
	os.WriteFile(filepath.Join(dir, "cover.jpg"), pngBytes(t, 8, 8, false), 0o644)
	os.WriteFile(md, []byte("---\ntitle: 好文章\ncover: cover.jpg\n---\n真正的正文\n"), 0o644)
	if code, out, errOut := run(t, "", "--config", cfgPath, "format", md, "--offline"); code != 0 || !strings.Contains(out, "检查通过") {
		t.Fatalf("filled article should pass: %d %s %s", code, out, errOut)
	}
	// --no-check 只整理，有错误也返回 0
	os.WriteFile(md, []byte("---\ntitle: 好文章\ncover: nope.png\n---\n正文\n"), 0o644)
	if code, out, errOut := run(t, "", "--config", cfgPath, "format", md, "--no-check"); code != 0 {
		t.Fatalf("--no-check: %d %s %s", code, out, errOut)
	}
	// check 子命令已移除
	if code, _, _ := run(t, "", "--config", cfgPath, "check", md); code != 2 {
		t.Fatalf("check should be unknown command, got %d", code)
	}
}

func TestCheckIssues(t *testing.T) {
	dir := t.TempDir()
	md := "---\ntitle: " + strings.Repeat("长", 40) + "\ncover: nope.png\nskin: ghost\ntypo_field: 1\nsource_url: ftp://x\n---\n正文 <img src=\"a.png\">\n\n[链接](https://example.com)\n\n```go\n<div>代码里不算</div>\n"
	a, err := ParseArticle([]byte(md), dir)
	if err != nil {
		t.Fatal(err)
	}
	issues := CheckArticle(a, []string{"summer-breeze"})
	want := map[string]string{
		"超过上限 32":      levelError,
		"图片不存在":        levelError,
		"皮肤 \"ghost\"": levelError,
		"阅读原文":         levelError,
		"没有成对出现":       levelError,
		"未知字段":         levelWarn,
		"HTML 标签":      levelWarn,
		"不能点击":         levelWarn,
	}
	for frag, level := range want {
		found := false
		for _, is := range issues {
			if strings.Contains(is.Msg, frag) && is.Level == level {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %s issue %q in %+v", level, frag, issues)
		}
	}
	for _, is := range issues {
		if strings.Contains(is.Msg, "<div>") {
			t.Errorf("tags inside code should be ignored: %+v", is)
		}
		if strings.Contains(is.Msg, "超过上限 32") && is.Line != 2 {
			t.Errorf("title issue should point to line 2, got %d", is.Line)
		}
	}
}

func TestDraftBlockedByCheck(t *testing.T) {
	cfgPath := isolate(t)
	m := &mockAPI{}
	ts := m.server(t)
	dir := t.TempDir()
	md := filepath.Join(dir, "post.md")
	os.WriteFile(md, []byte("---\ntitle: x\ncover: missing.png\n---\n正文\n\n![图](missing-too.png)\n"), 0o644)
	code, _, errOut := run(t, "", "--config", cfgPath, "--url", ts.URL, "--token", testToken, "draft", md)
	if code != 1 || !strings.Contains(errOut, "nothing uploaded") {
		t.Fatalf("draft should be blocked: %d %s", code, errOut)
	}
	if m.uploads != 0 || m.covers != 0 || m.lastDraft != nil {
		t.Fatalf("nothing should be sent: uploads=%d covers=%d draft=%v", m.uploads, m.covers, m.lastDraft)
	}
}

func TestHelp(t *testing.T) {
	isolate(t)
	code, out, _ := run(t, "", "help")
	if code != 0 || !strings.Contains(out, "help agent") {
		t.Fatalf("help: %d %s", code, out)
	}
	// 每个命令都有详细帮助
	for _, name := range cmdOrder {
		code, out, errOut := run(t, "", "help", name)
		if code != 0 || !strings.Contains(out, "wxctl "+name) {
			t.Fatalf("help %s: %d %s %s", name, code, out, errOut)
		}
	}
	if code, out, _ := run(t, "", "help", "agent"); code != 0 || !strings.Contains(out, "--json") {
		t.Fatalf("help agent: %d %s", code, out)
	}
	if code, out, _ := run(t, "", "--json", "help", "--json", "draft"); code != 0 || !strings.Contains(out, "wxctl draft") {
		t.Fatalf("help with flags: %d %s", code, out)
	}
	if code, _, _ := run(t, "", "help", "nope"); code != 2 {
		t.Fatalf("unknown topic code=%d", code)
	}
}
