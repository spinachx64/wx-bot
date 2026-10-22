package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"wx-bot/internal/render"
	"wx-bot/internal/wechat"
)

const testToken = "secret-token"

// mockWX 模拟微信接口，记录最后一次请求
type mockWX struct {
	tokenCalls atomic.Int32
	lastPath   string
	lastBody   []byte
	lastCT     string
	handler    func(w http.ResponseWriter, r *http.Request)
}

func newEnv(t *testing.T, m *mockWX) http.Handler {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cgi-bin/stable_token" {
			n := m.tokenCalls.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"access_token": "tk" + string(rune('0'+n)), "expires_in": 7200})
			return
		}
		m.lastPath = r.URL.Path
		m.lastCT = r.Header.Get("Content-Type")
		m.lastBody, _ = io.ReadAll(r.Body)
		if m.handler != nil {
			m.handler(w, r)
			return
		}
		w.Write([]byte(`{"errcode":0,"media_id":"MID"}`))
	}))
	t.Cleanup(ts.Close)
	wx := wechat.New("appid", "secret", wechat.WithBaseURL(ts.URL))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	skins, err := render.LoadRegistry("")
	if err != nil {
		t.Fatal(err)
	}
	return New(wx, render.New(skins), testToken, log).Handler()
}

func call(h http.Handler, method, path, ct string, body io.Reader, auth bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+testToken)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAuth(t *testing.T) {
	h := newEnv(t, &mockWX{})
	if rec := call(h, "GET", "/api/v1/drafts/count", "", nil, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: got %d", rec.Code)
	}
	req := httptest.NewRequest("GET", "/api/v1/drafts/count", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: got %d", rec.Code)
	}
	if rec := call(h, "GET", "/healthz", "", nil, false); rec.Code != http.StatusOK {
		t.Fatalf("healthz: got %d", rec.Code)
	}
	if rec := call(h, "GET", "/api/v1/drafts/count", "", nil, true); rec.Code != http.StatusOK {
		t.Fatalf("valid token: got %d", rec.Code)
	}
}

func TestPingAndPanel(t *testing.T) {
	h := newEnv(t, &mockWX{})
	if rec := call(h, "GET", "/api/v1/ping", "", nil, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("ping without token: got %d", rec.Code)
	}
	if rec := call(h, "GET", "/api/v1/ping", "", nil, true); rec.Code != http.StatusOK {
		t.Fatalf("ping with token: got %d", rec.Code)
	}
	for _, p := range []string{"/", "/app.js", "/style.css"} {
		rec := call(h, "GET", p, "", nil, false)
		if rec.Code != http.StatusOK {
			t.Fatalf("panel %s: got %d", p, rec.Code)
		}
		if rec.Header().Get("Content-Security-Policy") == "" {
			t.Fatalf("panel %s: missing CSP header", p)
		}
	}
	if rec := call(h, "GET", "/api/v1/nope", "", nil, true); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown api: got %d", rec.Code)
	}
}

func TestDraftAddKeepsHTML(t *testing.T) {
	m := &mockWX{}
	h := newEnv(t, m)
	body := `{"articles":[{"title":"t","content":"<p>a&b</p>","thumb_media_id":"x"}]}`
	rec := call(h, "POST", "/api/v1/drafts", "application/json", strings.NewReader(body), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if m.lastPath != "/cgi-bin/draft/add" {
		t.Fatalf("path %s", m.lastPath)
	}
	if !bytes.Contains(m.lastBody, []byte("<p>a&b</p>")) {
		t.Fatalf("html escaped: %s", m.lastBody)
	}
}

func TestDraftValidation(t *testing.T) {
	h := newEnv(t, &mockWX{})
	cases := []struct{ method, path, body string }{
		{"POST", "/api/v1/drafts", `{"articles":{}}`},
		{"POST", "/api/v1/drafts", `not json`},
		{"PUT", "/api/v1/drafts/MID", `{"articles":{}}`},
		{"PUT", "/api/v1/drafts/MID", `{"index":0,"articles":[]}`},
	}
	for _, c := range cases {
		rec := call(h, c.method, c.path, "application/json", strings.NewReader(c.body), true)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s %s: got %d", c.method, c.path, c.body, rec.Code)
		}
	}
	for _, q := range []string{"count=0", "count=21", "offset=-1", "offset=x"} {
		if rec := call(h, "GET", "/api/v1/drafts?"+q, "", nil, true); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d", q, rec.Code)
		}
	}
}

func TestPublishSubmit(t *testing.T) {
	m := &mockWX{}
	h := newEnv(t, m)
	rec := call(h, "POST", "/api/v1/drafts/MID123/publish", "", nil, true)
	if rec.Code != http.StatusOK || m.lastPath != "/cgi-bin/freepublish/submit" {
		t.Fatalf("status %d path %s", rec.Code, m.lastPath)
	}
	var got map[string]string
	json.Unmarshal(m.lastBody, &got)
	if got["media_id"] != "MID123" {
		t.Fatalf("body %s", m.lastBody)
	}
}

func TestTokenRetry(t *testing.T) {
	m := &mockWX{}
	var calls atomic.Int32
	m.handler = func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Write([]byte(`{"errcode":40001,"errmsg":"invalid credential"}`))
			return
		}
		w.Write([]byte(`{"total_count":3}`))
	}
	h := newEnv(t, m)
	rec := call(h, "GET", "/api/v1/drafts/count", "", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if m.tokenCalls.Load() != 2 {
		t.Fatalf("token calls = %d, want 2", m.tokenCalls.Load())
	}
	// 再次请求复用缓存 token
	call(h, "GET", "/api/v1/drafts/count", "", nil, true)
	if m.tokenCalls.Load() != 2 {
		t.Fatalf("token not cached: %d", m.tokenCalls.Load())
	}
}

func TestWechatErrorPassthrough(t *testing.T) {
	m := &mockWX{handler: func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"errcode":40007,"errmsg":"invalid media_id"}`))
	}}
	h := newEnv(t, m)
	rec := call(h, "DELETE", "/api/v1/drafts/bad", "", nil, true)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "40007") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
}

func TestMaterialAdd(t *testing.T) {
	m := &mockWX{}
	h := newEnv(t, m)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("type", "image")
	fw, _ := mw.CreateFormFile("media", "a.png")
	fw.Write([]byte("\x89PNG\r\n\x1a\nfake"))
	mw.Close()

	rec := call(h, "POST", "/api/v1/materials", mw.FormDataContentType(), &buf, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if m.lastPath != "/cgi-bin/material/add_material" || !strings.HasPrefix(m.lastCT, "multipart/form-data") {
		t.Fatalf("path %s ct %s", m.lastPath, m.lastCT)
	}
	if !bytes.Contains(m.lastBody, []byte(`name="media"; filename="a.png"`)) {
		t.Fatalf("missing media part: %s", m.lastBody)
	}
}

func TestMaterialGetBinary(t *testing.T) {
	m := &mockWX{handler: func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("\xff\xd8\xffbinary"))
	}}
	h := newEnv(t, m)
	rec := call(h, "GET", "/api/v1/materials/MID", "", nil, true)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("status %d ct %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec.Body.String() != "\xff\xd8\xffbinary" {
		t.Fatalf("body mismatch")
	}
}
