package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestNewspicDraftAddAndUpdate(t *testing.T) {
	m := &mockWX{}
	h := newEnv(t, m)
	article := `{"article_type":"newspic","title":"图集","content":"文字 **保持原样**\n第二行 < > &","image_info":{"image_list":[{"image_media_id":"FIRST"},{"image_media_id":"SECOND"}]}}`
	for _, tc := range []struct{ method, path, body, wxPath string }{
		{"POST", "/api/v1/drafts", `{"articles":[` + article + `]}`, "/cgi-bin/draft/add"},
		{"PUT", "/api/v1/drafts/MID", `{"index":0,"articles":` + article + `}`, "/cgi-bin/draft/update"},
	} {
		rec := call(h, tc.method, tc.path, "application/json", strings.NewReader(tc.body), true)
		if rec.Code != http.StatusOK || m.lastPath != tc.wxPath {
			t.Fatalf("%s: %d %s path=%s", tc.method, rec.Code, rec.Body, m.lastPath)
		}
		var body map[string]json.RawMessage
		json.Unmarshal(m.lastBody, &body)
		var arts []map[string]any
		if tc.method == "POST" {
			json.Unmarshal(body["articles"], &arts)
		} else {
			var art map[string]any
			json.Unmarshal(body["articles"], &art)
			arts = append(arts, art)
		}
		art := arts[0]
		if art["content"] != "文字 **保持原样**\n第二行 < > &" || art["need_open_comment"] != float64(1) {
			t.Fatalf("content/default comment changed: %v", art)
		}
		list := art["image_info"].(map[string]any)["image_list"].([]any)
		if list[0].(map[string]any)["image_media_id"] != "FIRST" || list[1].(map[string]any)["image_media_id"] != "SECOND" {
			t.Fatalf("image order changed: %v", list)
		}
	}
}

func TestNewspicServerValidation(t *testing.T) {
	m := &mockWX{}
	h := newEnv(t, m)
	base := map[string]any{
		"article_type": "newspic", "title": "图集", "content": "说明",
		"image_info": map[string]any{"image_list": []any{map[string]string{"image_media_id": "ID"}}},
	}
	for _, tc := range []struct {
		name, key string
		value     any
	}{
		{"unknown type", "article_type", "image"},
		{"null type", "article_type", nil},
		{"null content", "content", nil},
		{"number content", "content", 12},
		{"empty title", "title", " "},
		{"long title", "title", strings.Repeat("字", 33)},
		{"markdown", "markdown", "正文"},
		{"theme", "theme", "summer-breeze"},
		{"theme css", "theme_css", "x{}"},
		{"independent cover", "thumb_media_id", "ID"},
		{"missing image info", "image_info", nil},
		{"empty list", "image_info", map[string]any{"image_list": []any{}}},
		{"empty id", "image_info", map[string]any{"image_list": []any{map[string]string{"image_media_id": " "}}}},
		{"null image", "image_info", map[string]any{"image_list": []any{nil}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			art := map[string]any{}
			for k, v := range base {
				art[k] = v
			}
			art[tc.key] = tc.value
			for _, update := range []bool{false, true} {
				body := map[string]any{"articles": []any{art}}
				method, path := "POST", "/api/v1/drafts"
				if update {
					method, path = "PUT", "/api/v1/drafts/MID"
					body["articles"], body["index"] = art, 0
				}
				data, _ := json.Marshal(body)
				m.lastPath = ""
				rec := call(h, method, path, "application/json", strings.NewReader(string(data)), true)
				if rec.Code != http.StatusBadRequest || m.lastPath != "" {
					t.Fatalf("%s: %d %s wx=%s", method, rec.Code, rec.Body, m.lastPath)
				}
			}
		})
	}
	for _, count := range []int{1, 20, 21} {
		var list []map[string]string
		for i := 0; i < count; i++ {
			list = append(list, map[string]string{"image_media_id": fmt.Sprintf("ID%d", i)})
		}
		base["image_info"] = map[string]any{"image_list": list}
		data, _ := json.Marshal(map[string]any{"articles": []any{base}})
		rec := call(h, "POST", "/api/v1/drafts", "application/json", strings.NewReader(string(data)), true)
		want := http.StatusOK
		if count > 20 {
			want = http.StatusBadRequest
		}
		if rec.Code != want {
			t.Fatalf("count=%d: %d %s", count, rec.Code, rec.Body)
		}
	}
}
