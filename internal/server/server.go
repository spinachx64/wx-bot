// Package server 对外提供公众号文章相关的 HTTP 接口。
package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"wx-bot/internal/render"
	"wx-bot/internal/store"
	"wx-bot/internal/web"
	"wx-bot/internal/wechat"
)

const (
	maxJSONBody   = 8 << 20
	maxUploadBody = 20 << 20
)

type Server struct {
	store      *store.Store
	clients    *clientPool
	render     *render.Renderer
	adminToken string
	log        *slog.Logger
}

// New adminToken 为空时管理接口全部拒绝；wxOpts 传给每个租户的 wechat.Client（测试用来换 baseURL）
func New(st *store.Store, rd *render.Renderer, adminToken string, log *slog.Logger, wxOpts ...wechat.Option) *Server {
	return &Server{
		store:      st,
		clients:    &clientPool{m: map[int64]poolEntry{}, opts: wxOpts},
		render:     rd,
		adminToken: adminToken,
		log:        log,
	}
}

// Handler 返回带认证中间件的路由
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// 草稿管理
	mux.HandleFunc("POST /api/v1/drafts", s.draftAdd)
	mux.HandleFunc("GET /api/v1/drafts", s.draftList)
	mux.HandleFunc("GET /api/v1/drafts/count", s.draftCount)
	mux.HandleFunc("GET /api/v1/drafts/{media_id}", s.draftGet)
	mux.HandleFunc("PUT /api/v1/drafts/{media_id}", s.draftUpdate)
	mux.HandleFunc("DELETE /api/v1/drafts/{media_id}", s.draftDelete)
	mux.HandleFunc("POST /api/v1/drafts/{media_id}/publish", s.publishSubmit)

	// 素材管理
	mux.HandleFunc("POST /api/v1/materials", s.materialAdd)
	mux.HandleFunc("POST /api/v1/materials/images", s.uploadImage)
	mux.HandleFunc("GET /api/v1/materials", s.materialList)
	mux.HandleFunc("GET /api/v1/materials/count", s.materialCount)
	mux.HandleFunc("GET /api/v1/materials/{media_id}", s.materialGet)
	mux.HandleFunc("DELETE /api/v1/materials/{media_id}", s.materialDelete)

	// 发布能力
	mux.HandleFunc("GET /api/v1/publish", s.publishList)
	mux.HandleFunc("GET /api/v1/publish/{publish_id}", s.publishGet)
	mux.HandleFunc("GET /api/v1/articles/{article_id}", s.articleGet)
	mux.HandleFunc("DELETE /api/v1/articles/{article_id}", s.articleDelete)

	// 排版渲染
	mux.HandleFunc("GET /api/v1/skins", s.skinList)
	mux.HandleFunc("POST /api/v1/render", s.renderMarkdown)

	// 面板登录、wxctl ping 时校验 token，并返回所属公众号
	mux.HandleFunc("GET /api/v1/ping", s.whoami)

	// 租户管理（管理员 token）
	mux.HandleFunc("POST /api/v1/admin/tenants", s.tenantCreate)
	mux.HandleFunc("GET /api/v1/admin/tenants", s.tenantList)
	mux.HandleFunc("GET /api/v1/admin/tenants/{tenant}", s.tenantGet)
	mux.HandleFunc("PATCH /api/v1/admin/tenants/{tenant}", s.tenantUpdate)
	mux.HandleFunc("DELETE /api/v1/admin/tenants/{tenant}", s.tenantDelete)
	mux.HandleFunc("POST /api/v1/admin/tenants/{tenant}/tokens", s.tokenCreate)
	mux.HandleFunc("DELETE /api/v1/admin/tenants/{tenant}/tokens/{token_id}", s.tokenDelete)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	})

	// 面板静态资源
	mux.Handle("GET /", web.Handler())

	return s.auth(mux)
}

// ---------- 响应工具 ----------

func writeJSON(w http.ResponseWriter, status int, data []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	w.Write(data)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	data, _ := json.Marshal(map[string]any{"errcode": -1, "errmsg": msg})
	writeJSON(w, status, data)
}

// writeResult 透传微信响应；微信业务错误返回 502 并保留 errcode
func (s *Server) writeResult(w http.ResponseWriter, r *http.Request, data []byte, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, data)
		return
	}
	var we *wechat.Error
	if errors.As(err, &we) {
		s.log.Warn("wechat api error", "path", r.URL.Path, "errcode", we.Code, "errmsg", we.Msg)
		out, _ := json.Marshal(we)
		writeJSON(w, http.StatusBadGateway, out)
		return
	}
	s.log.Error("wechat request failed", "path", r.URL.Path, "err", err)
	writeError(w, http.StatusBadGateway, "upstream request failed")
}

// marshalNoEscape 编码 JSON 且不转义 HTML 字符，渲染出的正文原样保留
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body: "+err.Error())
		return false
	}
	return true
}

// pageParams 解析 offset/count，count 范围 1~20，默认 20
func pageParams(w http.ResponseWriter, r *http.Request) (offset, count int, ok bool) {
	q := r.URL.Query()
	offset, count = 0, 20
	var err error
	if v := q.Get("offset"); v != "" {
		if offset, err = strconv.Atoi(v); err != nil || offset < 0 {
			writeError(w, http.StatusBadRequest, "invalid offset")
			return 0, 0, false
		}
	}
	if v := q.Get("count"); v != "" {
		if count, err = strconv.Atoi(v); err != nil || count < 1 || count > 20 {
			writeError(w, http.StatusBadRequest, "count must be 1-20")
			return 0, 0, false
		}
	}
	return offset, count, true
}

func boolParam(r *http.Request, key string) bool {
	v, _ := strconv.ParseBool(r.URL.Query().Get(key))
	return v
}
