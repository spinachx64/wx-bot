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
	"sync"
	"testing"

	"wx-bot/internal/render"
	"wx-bot/internal/store"
	"wx-bot/internal/wechat"
)

// multiEnv 多租户测试环境：mock 微信按 appid 发不同的 access_token，
// 记录每个业务请求用的 token，用来确认请求落在正确的公众号上
type multiEnv struct {
	h       http.Handler
	mu      sync.Mutex
	used    []string // 业务请求携带的 access_token
	uploads int      // 微信收到的图片上传次数
}

func newMultiEnv(t *testing.T) *multiEnv {
	t.Helper()
	e := &multiEnv{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cgi-bin/stable_token" {
			var body struct {
				AppID string `json:"appid"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			json.NewEncoder(w).Encode(map[string]any{"access_token": "at-" + body.AppID, "expires_in": 7200})
			return
		}
		e.mu.Lock()
		e.used = append(e.used, r.URL.Query().Get("access_token"))
		if r.URL.Path == "/cgi-bin/media/uploadimg" {
			e.uploads++
		}
		e.mu.Unlock()
		io.Copy(io.Discard, r.Body)
		if r.URL.Path == "/cgi-bin/media/uploadimg" {
			w.Write([]byte(`{"url":"http://mmbiz.qpic.cn/x"}`))
			return
		}
		w.Write([]byte(`{"total_count":1}`))
	}))
	t.Cleanup(ts.Close)
	st, err := store.Open(t.TempDir(), []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	themes, err := render.LoadRegistry("")
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e.h = New(st, render.New(themes), testAdminToken, log, wechat.WithBaseURL(ts.URL)).Handler()
	return e
}

func (e *multiEnv) do(method, path, token, ct string, body io.Reader) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *multiEnv) lastUsed() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.used) == 0 {
		return ""
	}
	return e.used[len(e.used)-1]
}

// createTenant 通过管理接口建租户，返回明文 token
func (e *multiEnv) createTenant(t *testing.T, name, appID string) string {
	t.Helper()
	body := `{"name":"` + name + `","appid":"` + appID + `","secret":"s-` + name + `","token_note":"t"}`
	rec := e.do("POST", "/api/v1/admin/tenants", testAdminToken, "application/json", strings.NewReader(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create tenant %s: %d %s", name, rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "s-"+name) {
		t.Fatalf("secret leaked in response: %s", rec.Body)
	}
	var out struct {
		Token string `json:"token"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if !strings.HasPrefix(out.Token, "wxt_") {
		t.Fatalf("token: %s", rec.Body)
	}
	return out.Token
}

func imageForm(data []byte) (*bytes.Buffer, string) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("media", "../../evil.png")
	fw.Write(data)
	mw.Close()
	return &buf, mw.FormDataContentType()
}

func TestAdminAuth(t *testing.T) {
	e := newMultiEnv(t)
	tokA := e.createTenant(t, "alpha", "wx0000000000000001")
	for _, tok := range []string{"", "wrong", tokA} {
		if rec := e.do("GET", "/api/v1/admin/tenants", tok, "", nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("admin with %q: got %d", tok, rec.Code)
		}
	}
	// 管理员 token 不能当租户 token 用
	if rec := e.do("GET", "/api/v1/drafts/count", testAdminToken, "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("admin token on tenant api: got %d", rec.Code)
	}
	rec := e.do("POST", "/api/v1/admin/tenants", testAdminToken, "application/json",
		strings.NewReader(`{"name":"Bad Name","appid":"wx0000000000000002","secret":"x"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid name: %d", rec.Code)
	}
	rec = e.do("POST", "/api/v1/admin/tenants", testAdminToken, "application/json",
		strings.NewReader(`{"name":"alpha","appid":"wx0000000000000003","secret":"x"}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate: %d", rec.Code)
	}
}

func TestTenantIsolation(t *testing.T) {
	e := newMultiEnv(t)
	tokA := e.createTenant(t, "alpha", "wx0000000000000001")
	tokB := e.createTenant(t, "beta", "wx0000000000000002")

	for tok, want := range map[string]string{tokA: "at-wx0000000000000001", tokB: "at-wx0000000000000002"} {
		if rec := e.do("GET", "/api/v1/drafts/count", tok, "", nil); rec.Code != http.StatusOK {
			t.Fatalf("drafts count: %d %s", rec.Code, rec.Body)
		}
		if got := e.lastUsed(); got != want {
			t.Fatalf("request used %s, want %s", got, want)
		}
	}
	rec := e.do("GET", "/api/v1/ping", tokB, "", nil)
	if !strings.Contains(rec.Body.String(), `"tenant":"beta"`) {
		t.Fatalf("ping: %s", rec.Body)
	}

	// 同一张图：同租户第二次命中缓存，另一个租户要重新上传
	img := []byte("\x89PNG\r\n\x1a\nsame-image")
	for i, tok := range []string{tokA, tokA, tokB} {
		body, ct := imageForm(img)
		if rec := e.do("POST", "/api/v1/materials/images", tok, ct, body); rec.Code != http.StatusOK {
			t.Fatalf("upload %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	if e.uploads != 2 {
		t.Fatalf("wechat uploads = %d, want 2 (dedup per tenant)", e.uploads)
	}

	// 停用 alpha：token 失效，beta 不受影响
	rec = e.do("PATCH", "/api/v1/admin/tenants/alpha", testAdminToken, "application/json", strings.NewReader(`{"disabled":true}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("GET", "/api/v1/ping", tokA, "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("disabled tenant: %d", rec.Code)
	}
	if rec := e.do("GET", "/api/v1/ping", tokB, "", nil); rec.Code != http.StatusOK {
		t.Fatalf("other tenant: %d", rec.Code)
	}
}

func TestTokenRevoke(t *testing.T) {
	e := newMultiEnv(t)
	e.createTenant(t, "alpha", "wx0000000000000001")
	rec := e.do("POST", "/api/v1/admin/tenants/alpha/tokens", testAdminToken, "application/json", strings.NewReader(`{"note":"agent"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create token: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Token string `json:"token"`
		Info  struct {
			ID int64 `json:"id"`
		} `json:"token_info"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if rec := e.do("GET", "/api/v1/ping", out.Token, "", nil); rec.Code != http.StatusOK {
		t.Fatalf("new token: %d", rec.Code)
	}
	// 列表里不出现明文
	rec = e.do("GET", "/api/v1/admin/tenants/alpha", testAdminToken, "", nil)
	if strings.Contains(rec.Body.String(), out.Token) {
		t.Fatalf("plain token listed: %s", rec.Body)
	}
	path := "/api/v1/admin/tenants/alpha/tokens/" + jsonNum(out.Info.ID)
	if rec := e.do("DELETE", path, testAdminToken, "", nil); rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("GET", "/api/v1/ping", out.Token, "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token still works: %d", rec.Code)
	}
}

func jsonNum(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
