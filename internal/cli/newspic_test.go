package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMarkdownImageExtraction(t *testing.T) {
	md := "文字 ![嵌套 **说明**](images/a_(1).png \"title\") 后文\n\n![空格](</absolute/my photo.jpg>)\n\n![引用][pic]\n\n[pic]: images/reference.png\n\n`![行内](ignore.png)`\n\n````md\n![围栏](ignore2.png)\n```\n![仍在围栏](ignore3.png)\n````\n\n    ![缩进](ignore4.png)\n\n\\![转义](ignore5.png)\n"
	refs := markdownImages(md)
	var got []string
	for _, ref := range refs {
		got = append(got, ref.src)
		if !strings.HasPrefix(md[ref.start:ref.end], "![") {
			t.Fatalf("wrong source range: %+v", ref)
		}
	}
	want := []string{"images/a_(1).png", "/absolute/my photo.jpg", "images/reference.png"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("images = %q, want %q", got, want)
	}
	a := &Article{Dir: "/test", Markdown: md}
	a.collectNewspicImages()
	if !reflect.DeepEqual(a.Meta.Images, want) || !strings.Contains(a.Markdown, "文字  后文") || strings.Contains(a.Markdown, "![引用]") {
		t.Fatalf("collected: %+v\n%s", a.Meta, a.Markdown)
	}
	for _, want := range []string{"`![行内](ignore.png)`", "![围栏](ignore2.png)", "![仍在围栏](ignore3.png)", "    ![缩进](ignore4.png)", "\\![转义](ignore5.png)"} {
		if !strings.Contains(a.Markdown, want) {
			t.Fatalf("lost literal %q: %s", want, a.Markdown)
		}
	}
}

func TestFormatNewspicAbsolutePathsAndIdempotency(t *testing.T) {
	cfg := isolate(t)
	dir := t.TempDir()
	abs := filepath.Join(t.TempDir(), "绝对 图片.png")
	if err := os.WriteFile(abs, pngBytes(t, 8, 8, false), 0600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "post.md")
	source := fmt.Sprintf("# 摄影记录\n\n第一段文字。\n\n![照片](<%s>)\n\n第二段文字。\n", abs)
	os.WriteFile(file, []byte(source), 0600)
	args := []string{"--config", cfg, "--json", "format", file, "--offline"}
	if code, out, errOut := run(t, "", append(args, "--type", "newspic")...); code != 0 {
		t.Fatalf("format: %d %s %s", code, out, errOut)
	}
	first, _ := os.ReadFile(file)
	a, err := LoadArticle(file)
	if err != nil || a.Meta.ArticleType != "newspic" || !reflect.DeepEqual(a.Meta.Images, []string{abs}) || a.Markdown != "\n第一段文字。\n\n第二段文字。\n" {
		t.Fatalf("formatted: %+v err=%v\n%s", a, err, first)
	}
	if strings.Contains(string(first), "cover:") || strings.Contains(string(first), "theme:") {
		t.Fatalf("unexpected news metadata: %s", first)
	}
	if code, out, errOut := run(t, "", args...); code != 0 {
		t.Fatalf("repeat format: %d %s %s", code, out, errOut)
	}
	second, _ := os.ReadFile(file)
	if string(first) != string(second) {
		t.Fatalf("format is not idempotent:\n%s\n%s", first, second)
	}
	// 已有列表不被正文提取覆盖；同一来源不重复追加。
	os.WriteFile(file, append(second, []byte(fmt.Sprintf("\n![再引用](<%s>)\n\n![新图](https://example.com/new.png)\n", abs))...), 0600)
	if code, out, errOut := run(t, "", args...); code != 0 {
		t.Fatalf("append format: %d %s %s", code, out, errOut)
	}
	a, _ = LoadArticle(file)
	if !reflect.DeepEqual(a.Meta.Images, []string{abs, "https://example.com/new.png"}) {
		t.Fatalf("wrong append order: %v", a.Meta.Images)
	}
}

func TestFormatNewspicRebasesRelativePaths(t *testing.T) {
	cfg := isolate(t)
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "out"), 0700)
	os.WriteFile(filepath.Join(dir, "a.png"), pngBytes(t, 8, 8, false), 0600)
	file := filepath.Join(dir, "post.md")
	dst := filepath.Join(dir, "out", "post.md")
	source := "# 图片\n\n说明\n\n![图](a.png)\n"
	os.WriteFile(file, []byte(source), 0600)
	if code, out, errOut := run(t, "", "--config", cfg, "format", file, "--type", "newspic", "-o", dst, "--offline"); code != 0 {
		t.Fatalf("format -o: %d %s %s", code, out, errOut)
	}
	a, err := LoadArticle(dst)
	if err != nil || !reflect.DeepEqual(a.Meta.Images, []string{"../a.png"}) {
		t.Fatalf("rebased: %+v %v", a, err)
	}
	if original, _ := os.ReadFile(file); string(original) != source {
		t.Fatal("source changed with -o")
	}
}

func TestNewspicDraftEndToEnd(t *testing.T) {
	cfg := isolate(t)
	m := &mockAPI{uniqueMaterials: true}
	ts := m.server(t)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("sent account token to image host")
		}
		w.Write(pngBytes(t, 9, 9, false))
	}))
	defer remote.Close()
	dir := t.TempDir()
	abs := filepath.Join(t.TempDir(), "absolute.png")
	os.WriteFile(abs, pngBytes(t, 8, 8, false), 0600)
	file := filepath.Join(dir, "post.md")
	// 全局主题文件即使不存在，图片消息也不读取。
	os.WriteFile(cfg, []byte("theme: /missing/theme.css\n"), 0600)
	source := fmt.Sprintf("---\narticle_type: newspic\ntitle: 图集\nimages:\n  - %s\n  - %s/photo.png\n  - media_id:EXISTING\n  - %s\nopen_comment: false\nfans_comment: true\n---\n纯文本 **不渲染**\n第二行\n", abs, remote.URL, abs)
	os.WriteFile(file, []byte(source), 0600)
	base := []string{"--config", cfg, "--url", ts.URL, "--token", testToken, "--json", "draft", file}
	if code, out, errOut := run(t, "", append(base, "--publish")...); code != 0 || !strings.Contains(out, `"image_count":4`) || !strings.Contains(out, `"publish_id":"2247483"`) {
		t.Fatalf("draft: %d %s %s", code, out, errOut)
	}
	art := m.lastDraft["articles"].([]any)[0].(map[string]any)
	if art["article_type"] != "newspic" || art["content"] != "纯文本 **不渲染**\n第二行\n" || art["need_open_comment"] != float64(0) || art["only_fans_can_comment"] != float64(1) {
		t.Fatalf("wrong article: %v", art)
	}
	for _, key := range []string{"markdown", "theme", "theme_css", "thumb_media_id"} {
		if _, ok := art[key]; ok {
			t.Fatalf("unexpected %s in %v", key, art)
		}
	}
	list := art["image_info"].(map[string]any)["image_list"].([]any)
	for i, want := range []string{"MATERIAL1", "MATERIAL2", "EXISTING", "MATERIAL1"} {
		if list[i].(map[string]any)["image_media_id"] != want {
			t.Fatalf("wrong image order: %v", list)
		}
	}
	if m.uploads != 0 || m.covers != 2 {
		t.Fatalf("wrong upload API/count: content=%d material=%d", m.uploads, m.covers)
	}
	if code, out, errOut := run(t, "", base...); code != 0 || m.draftPath != "PUT /api/v1/drafts/DRAFT1" || m.covers != 2 {
		t.Fatalf("update: %d %s %s %s uploads=%d", code, out, errOut, m.draftPath, m.covers)
	}
	a, err := LoadArticle(file)
	if err != nil || a.Meta.MediaID != "DRAFT1" || a.Meta.ArticleType != "newspic" {
		t.Fatalf("writeback: %+v %v", a, err)
	}
}

func TestNewspicValidationBeforeUploads(t *testing.T) {
	cfg := isolate(t)
	m := &mockAPI{}
	ts := m.server(t)
	dir := t.TempDir()
	validImage := filepath.Join(dir, "valid.png")
	os.WriteFile(validImage, pngBytes(t, 8, 8, false), 0600)
	for _, tc := range []struct{ name, images, body string }{
		{"no images", "[]", "正文"},
		{"too many", "[" + strings.Repeat("media_id:ID,", 20) + "media_id:LAST]", "正文"},
		{"empty id", "[media_id:]", "正文"},
		{"missing file", "[" + validImage + ", missing.png]", "正文"},
		{"unsupported protocol", "[ftp://example.com/a.png]", "正文"},
		{"body image", "[media_id:ID]", "正文 ![图](a.png)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(dir, "post.md")
			os.WriteFile(file, []byte("---\narticle_type: newspic\ntitle: 标题\nimages: "+tc.images+"\n---\n"+tc.body+"\n"), 0600)
			code, out, errOut := run(t, "", "--config", cfg, "--url", ts.URL, "--token", testToken, "draft", file, "--no-check")
			if code == 0 || m.covers != 0 || m.uploads != 0 || m.lastDraft != nil {
				t.Fatalf("should fail without uploads: %d %s %s, %+v", code, out, errOut, m)
			}
		})
	}
}

func TestMaterialCacheAccountIsolation(t *testing.T) {
	uploads := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads++
		json.NewEncoder(w).Encode(map[string]string{"media_id": fmt.Sprintf("ID%d", uploads)})
	}))
	defer ts.Close()
	dir := t.TempDir()
	image := filepath.Join(dir, "absolute.png")
	os.WriteFile(image, pngBytes(t, 8, 8, false), 0600)
	cfg := &Config{URL: ts.URL, Token: "account-a", CacheDir: dir}
	for i, token := range []string{"account-a", "account-a", "account-b"} {
		cfg.Token = token
		id, err := NewUploader(NewClient(cfg), cfg).MaterialImage(context.Background(), &Article{Dir: t.TempDir()}, image)
		want := "ID1"
		if i == 2 {
			want = "ID2"
		}
		if err != nil || id != want {
			t.Fatalf("token %s: %q %v", token, id, err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(dir, "images.json"))
	if uploads != 2 || strings.Contains(string(data), "account-") {
		t.Fatalf("cache isolation: uploads=%d cache=%s", uploads, data)
	}
}

func TestNewspicTypeDefaultsOverridesAndWriteback(t *testing.T) {
	cfg := isolate(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "post.md")
	plain := "# 标题\n\n正文\n\n![图](a.png)\n"
	os.WriteFile(filepath.Join(dir, "a.png"), pngBytes(t, 8, 8, false), 0600)
	os.WriteFile(file, []byte(plain), 0600)
	if code, out, errOut := run(t, "", "--config", cfg, "format", file, "--offline"); code != 0 {
		t.Fatalf("default news: %d %s %s", code, out, errOut)
	}
	a, _ := LoadArticle(file)
	if a.Meta.articleType() != "news" || len(a.Meta.Images) != 0 || !strings.Contains(a.Markdown, "![图](a.png)") {
		t.Fatalf("default unexpectedly changed to newspic: %+v", a)
	}
	for _, cmd := range []string{"new", "format", "draft"} {
		if code, _, _ := run(t, "", "--config", cfg, cmd, file, "--type", "invalid"); code != 2 {
			t.Fatalf("%s accepted invalid type, code=%d", cmd, code)
		}
	}
	// --type 优先于头信息，成功保存时写回有效类型和草稿 ID。
	m := &mockAPI{}
	ts := m.server(t)
	os.WriteFile(file, []byte("---\narticle_type: news\ntitle: 标题\nimages: [media_id:EXISTING]\n---\n正文\n"), 0600)
	args := []string{"--config", cfg, "--url", ts.URL, "--token", testToken, "draft", file}
	if code, out, errOut := run(t, "", append(args, "--type", "newspic")...); code != 0 {
		t.Fatalf("type override: %d %s %s", code, out, errOut)
	}
	a, _ = LoadArticle(file)
	if a.Meta.ArticleType != "newspic" || a.Meta.MediaID != "DRAFT1" {
		t.Fatalf("type not written back: %+v", a.Meta)
	}
	if code, out, errOut := run(t, "", args...); code != 0 {
		t.Fatalf("repeat without --type: %d %s %s", code, out, errOut)
	}
	// 即使本地文件被改了类型，远端类型检查也会阻止覆盖。
	os.WriteFile(file, []byte("---\ntitle: 标题\nmedia_id: DRAFT1\ncover: a.png\n---\n正文\n"), 0600)
	if code, out, errOut := run(t, "", args...); code == 0 || !strings.Contains(out+errOut, "不能更新为 news") || m.covers != 0 {
		t.Fatalf("type mismatch not blocked before upload: %d %s %s uploads=%d", code, out, errOut, m.covers)
	}
}

func TestNewspicTemplateAndImageOnlyStdin(t *testing.T) {
	cfg := isolate(t)
	code, out, errOut := run(t, "", "--config", cfg, "new", "-", "--type", "newspic", "--title", "图集")
	if code != 0 || !strings.Contains(out, "article_type: newspic") || strings.Contains(out, "cover:") || strings.Contains(out, "theme:") {
		t.Fatalf("template: %d %s %s", code, out, errOut)
	}
	code, out, errOut = run(t, "# 图集\n\n![图](media_id:EXISTING)", "--config", cfg, "format", "-", "--type", "newspic", "--offline")
	if code != 0 || !strings.Contains(out, "media_id:EXISTING") {
		t.Fatalf("image-only format: %d %s %s", code, out, errOut)
	}
	m := &mockAPI{}
	ts := m.server(t)
	if code, res, errOut := run(t, out, "--config", cfg, "--url", ts.URL, "--token", testToken, "draft", "-"); code != 0 {
		t.Fatalf("image-only stdin: %d %s %s", code, res, errOut)
	}
	// 边界数量允许 20 张已有素材，不需要任何图片上传。
	images := strings.Repeat("  - media_id:ID\n", 20)
	stdin := "---\narticle_type: newspic\ntitle: 图集\nimages:\n" + images + "---\n"
	if code, res, errOut := run(t, stdin, "--config", cfg, "--url", ts.URL, "--token", testToken, "draft", "-"); code != 0 || m.covers != 0 {
		t.Fatalf("20 images: %d %s %s", code, res, errOut)
	}
}

func TestNewspicRemotePreflightAndUploadRetry(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.png")
	os.WriteFile(file, pngBytes(t, 8, 8, false), 0600)
	uploads := 0
	failUpload := true
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads++
		if uploads == 2 && failUpload {
			w.WriteHeader(502)
			w.Write([]byte(`{"errcode":-1,"errmsg":"try again"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"media_id": fmt.Sprintf("ID%d", uploads)})
	}))
	defer ts.Close()
	remoteOK := false
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !remoteOK {
			w.WriteHeader(404)
			return
		}
		w.Write(pngBytes(t, 9, 9, false))
	}))
	defer remote.Close()
	cfg := &Config{URL: ts.URL, Token: "account", CacheDir: dir}
	a := &Article{Dir: dir, Meta: Meta{Images: []string{file, remote.URL + "/a.png"}}}
	up := NewUploader(NewClient(cfg), cfg)
	if _, err := up.MaterialImages(context.Background(), a); err == nil || uploads != 0 {
		t.Fatalf("remote preflight uploaded partial materials: err=%v uploads=%d", err, uploads)
	}
	remoteOK = true
	if _, err := up.MaterialImages(context.Background(), a); err == nil || uploads != 2 {
		t.Fatalf("expected upload failure: err=%v uploads=%d", err, uploads)
	}
	failUpload = false
	ids, err := NewUploader(NewClient(cfg), cfg).MaterialImages(context.Background(), a)
	if err != nil || uploads != 3 || !reflect.DeepEqual(ids, []string{"ID1", "ID3"}) {
		t.Fatalf("retry did not reuse successful upload: %q %v uploads=%d", ids, err, uploads)
	}
}

func TestNewspicPreservesCodeWhitespace(t *testing.T) {
	code := "````md\n![示例](ignore.png)\n\n\n  保留缩进\n````"
	a := &Article{Dir: "/test", Markdown: "文字\n\n![图片](a.png)\n\n" + code + "\n"}
	a.collectNewspicImages()
	if !strings.Contains(a.Markdown, code) || !reflect.DeepEqual(a.Meta.Images, []string{"a.png"}) {
		t.Fatalf("code changed: %s, images=%q", a.Markdown, a.Meta.Images)
	}
}

func TestMarkdownImageBoundariesAndEscapes(t *testing.T) {
	for _, tc := range []struct{ md, src string }{
		{"![](a.png)", "a.png"},
		{"中文![图](a.png)末尾", "a.png"},
		{"![图](a\\(b\\).png)", "a(b).png"},
		{"![图](https://example.com/a.png?x=1&amp;y=2)", "https://example.com/a.png?x=1&y=2"},
		{"![图]\n\n[图]: a.png", "a.png"},
		{"![多行\n说明](a.png)", "a.png"},
		{"[![图](a.png)](https://example.com)", "a.png"},
	} {
		refs := markdownImages(tc.md)
		if len(refs) != 1 || refs[0].src != tc.src || refs[0].start < 0 || refs[0].end > len(tc.md) || refs[0].end <= refs[0].start {
			t.Fatalf("%q: %+v", tc.md, refs)
		}
	}
}
