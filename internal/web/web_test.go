package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// 静态资源带 ETag，内容未变时协商返回 304，浏览器不必重新下载
func TestETagRevalidate(t *testing.T) {
	h := Handler()
	for _, p := range []string{"/", "/app.js"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		tag := rec.Header().Get("ETag")
		if rec.Code != http.StatusOK || tag == "" {
			t.Fatalf("%s: code %d etag %q", p, rec.Code, tag)
		}
		req := httptest.NewRequest("GET", p, nil)
		req.Header.Set("If-None-Match", tag)
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotModified {
			t.Fatalf("%s revalidate: got %d", p, rec.Code)
		}
	}
}
