package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveMediaID(t *testing.T) {
	for _, source := range []string{
		"# 标题\n\n正文 ![图](./a.png)\n",
		"---\ntitle: 标题 # 保留注释\ncustom: value\nmedia_id: old\n---\n# 正文\n![图](a.png)\n",
		"\xef\xbb\xbf---\r\ntitle: 标题\r\nmedia_id: ''\r\n---\r\n正文\r\n",
	} {
		t.Run(source[:5], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "post.md")
			if err := os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			a, err := LoadArticle(path)
			if err != nil {
				t.Fatal(err)
			}
			if err = a.saveMediaID("NEW_ID"); err != nil {
				t.Fatal(err)
			}
			updated, err := LoadArticle(path)
			if err != nil {
				t.Fatal(err)
			}
			if updated.Meta.MediaID != "NEW_ID" || updated.Markdown != a.Markdown {
				t.Fatalf("changed body or wrong ID: %#v", updated)
			}
			data, _ := os.ReadFile(path)
			if strings.Contains(source, "# 保留注释") && !strings.Contains(string(data), "# 保留注释") {
				t.Fatal("lost comment")
			}
			if strings.HasPrefix(source, "\xef\xbb\xbf") && !strings.HasPrefix(string(data), "\xef\xbb\xbf") {
				t.Fatal("lost BOM")
			}
			if strings.Contains(source, "\r\n") && strings.Contains(strings.ReplaceAll(string(data), "\r\n", ""), "\n") {
				t.Fatal("mixed newlines")
			}
			st, _ := os.Stat(path)
			if st.Mode().Perm() != 0600 {
				t.Fatal("changed permissions")
			}
		})
	}
}

func TestSaveMediaIDRefusesConcurrentChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "post.md")
	os.WriteFile(path, []byte("# 标题\n原文"), 0644)
	a, err := LoadArticle(path)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("# 标题\n编辑后的正文"), 0644)
	if err := a.saveMediaID("ID"); err == nil {
		t.Fatal("should refuse overwrite")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "# 标题\n编辑后的正文" {
		t.Fatal("overwrote source")
	}
}
