package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"wx-bot/internal/store"
	"wx-bot/internal/wechat"
)

type ctxKey struct{}

// tenantOf 返回认证中间件放进 context 的租户
func tenantOf(r *http.Request) *store.Tenant {
	t, _ := r.Context().Value(ctxKey{}).(*store.Tenant)
	return t
}

// clientPool 每个租户一个 wechat.Client，各自缓存 access_token。
// 以 appid+secret 为键，secret 更新后自动换新 client
type clientPool struct {
	mu   sync.Mutex
	m    map[int64]poolEntry
	opts []wechat.Option
}

type poolEntry struct {
	key string
	c   *wechat.Client
}

func (p *clientPool) get(t *store.Tenant) *wechat.Client {
	sum := sha256.Sum256([]byte(t.AppID + "\x00" + t.Secret))
	key := hex.EncodeToString(sum[:])
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.m[t.ID]; ok && e.key == key {
		return e.c
	}
	c := wechat.New(t.AppID, t.Secret, p.opts...)
	p.m[t.ID] = poolEntry{key: key, c: c}
	return c
}

func (p *clientPool) drop(id int64) {
	p.mu.Lock()
	delete(p.m, id)
	p.mu.Unlock()
}

// wx 当前请求租户的微信客户端
func (s *Server) wx(r *http.Request) *wechat.Client {
	return s.clients.get(tenantOf(r))
}

// auth /api/v1/admin/ 用管理员 token，其他 /api/ 用租户 token；
// 面板静态资源和 /healthz 免认证
func (s *Server) auth(next http.Handler) http.Handler {
	admin := []byte(s.adminToken)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || got == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/admin/") {
			if len(admin) == 0 || subtle.ConstantTimeCompare([]byte(got), admin) != 1 {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		t, err := s.store.Authenticate(r.Context(), got)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				s.log.Error("authenticate", "err", err)
			}
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, t)))
	})
}

// cachedUpload 按内容 hash 在本租户内去重：命中直接返回上次微信的结果，
// 否则调 upload 上传，成功后把文件存进租户目录并记录
func (s *Server) cachedUpload(r *http.Request, kind string, f wechat.File, upload func() ([]byte, string, error)) ([]byte, error) {
	t := tenantOf(r)
	sum := sha256.Sum256(f.Data)
	sha := hex.EncodeToString(sum[:])
	if u, err := s.store.FindUpload(r.Context(), t.ID, kind, sha); err == nil {
		return u.Result, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		s.log.Warn("find upload", "err", err)
	}
	data, mediaID, err := upload()
	if err != nil {
		return data, err
	}
	if _, err := s.store.SaveUpload(r.Context(), t, kind, sha, fileExt(f), f.Data, mediaID, data); err != nil {
		// 已经上传成功，缓存失败不影响结果
		s.log.Warn("save upload", "tenant", t.Name, "err", err)
	}
	return data, nil
}

// fileExt 由内容推断扩展名，不使用用户提供的文件名
func fileExt(f wechat.File) string {
	switch http.DetectContentType(f.Data) {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/bmp":
		return ".bmp"
	}
	if ext := strings.ToLower(filepath.Ext(f.Name)); len(ext) >= 2 && len(ext) <= 6 && isAlnum(ext[1:]) {
		return ext
	}
	return ".bin"
}

func isAlnum(s string) bool {
	for _, c := range s {
		if !('a' <= c && c <= 'z' || '0' <= c && c <= '9') {
			return false
		}
	}
	return true
}

// ---------- 管理接口 ----------

func (s *Server) writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		s.log.Error("store", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func writeValue(w http.ResponseWriter, status int, v any) {
	data, _ := json.Marshal(v)
	writeJSON(w, status, data)
}

// adminTenant 解析路径里的 {tenant}（id 或 name）
func (s *Server) adminTenant(w http.ResponseWriter, r *http.Request) (*store.Tenant, bool) {
	t, err := s.store.GetTenant(r.Context(), r.PathValue("tenant"))
	if err != nil {
		s.writeStoreError(w, err)
		return nil, false
	}
	return t, true
}

// tenantCreate 请求体：{"name","appid","secret","token_note"}，同时生成第一个 token
func (s *Server) tenantCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string `json:"name"`
		AppID     string `json:"appid"`
		Secret    string `json:"secret"`
		TokenNote string `json:"token_note"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	t, err := s.store.CreateTenant(r.Context(), req.Name, req.AppID, req.Secret)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	plain, tok, err := s.store.CreateToken(r.Context(), t.ID, req.TokenNote)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	s.log.Info("tenant created", "tenant", t.Name, "appid", t.AppID)
	writeValue(w, http.StatusCreated, map[string]any{"tenant": t, "token": plain, "token_info": tok})
}

func (s *Server) tenantList(w http.ResponseWriter, r *http.Request) {
	ts, err := s.store.ListTenants(r.Context())
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if ts == nil {
		ts = []*store.Tenant{}
	}
	writeValue(w, http.StatusOK, map[string]any{"tenants": ts})
}

func (s *Server) tenantGet(w http.ResponseWriter, r *http.Request) {
	t, ok := s.adminTenant(w, r)
	if !ok {
		return
	}
	toks, err := s.store.ListTokens(r.Context(), t.ID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeValue(w, http.StatusOK, map[string]any{"tenant": t, "tokens": toks})
}

// tenantUpdate 请求体：{"secret":"...","disabled":true}，字段可选
func (s *Server) tenantUpdate(w http.ResponseWriter, r *http.Request) {
	t, ok := s.adminTenant(w, r)
	if !ok {
		return
	}
	var req struct {
		Secret   *string `json:"secret"`
		Disabled *bool   `json:"disabled"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	nt, err := s.store.UpdateTenant(r.Context(), t.ID, req.Secret, req.Disabled)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	s.clients.drop(t.ID)
	writeValue(w, http.StatusOK, map[string]any{"tenant": nt})
}

func (s *Server) tenantDelete(w http.ResponseWriter, r *http.Request) {
	t, ok := s.adminTenant(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteTenant(r.Context(), t.ID); err != nil {
		s.writeStoreError(w, err)
		return
	}
	s.clients.drop(t.ID)
	s.log.Info("tenant deleted", "tenant", t.Name)
	writeValue(w, http.StatusOK, map[string]any{"deleted": t.Name})
}

// tokenCreate 请求体：{"note":"..."}，明文 token 只在这里返回一次
func (s *Server) tokenCreate(w http.ResponseWriter, r *http.Request) {
	t, ok := s.adminTenant(w, r)
	if !ok {
		return
	}
	var req struct {
		Note string `json:"note"`
	}
	if r.ContentLength != 0 && !decodeBody(w, r, &req) {
		return
	}
	plain, tok, err := s.store.CreateToken(r.Context(), t.ID, req.Note)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeValue(w, http.StatusCreated, map[string]any{"token": plain, "token_info": tok})
}

func (s *Server) tokenDelete(w http.ResponseWriter, r *http.Request) {
	t, ok := s.adminTenant(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("token_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid token id")
		return
	}
	if err := s.store.DeleteToken(r.Context(), t.ID, id); err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeValue(w, http.StatusOK, map[string]any{"deleted": id})
}

// whoami 租户 token 查看自己所属的公众号
func (s *Server) whoami(w http.ResponseWriter, r *http.Request) {
	t := tenantOf(r)
	writeValue(w, http.StatusOK, map[string]any{"errcode": 0, "errmsg": "ok", "tenant": t.Name, "appid": t.AppID})
}
