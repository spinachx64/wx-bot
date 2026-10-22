// Package server 对外提供公众号文章相关的 HTTP 接口。
package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"wx-bot/internal/web"
	"wx-bot/internal/wechat"
)

const (
	maxJSONBody   = 8 << 20
	maxUploadBody = 20 << 20
)

type Server struct {
	wx    *wechat.Client
	token string
	log   *slog.Logger
}

func New(wx *wechat.Client, token string, log *slog.Logger) *Server {
	return &Server{wx: wx, token: token, log: log}
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

	// 面板登录时校验 token
	mux.HandleFunc("GET /api/v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, []byte(`{"errcode":0,"errmsg":"ok"}`))
	})

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	})

	// 面板静态资源
	mux.Handle("GET /", web.Handler())

	return s.auth(mux)
}

// auth 校验 /api/ 下请求的 Authorization: Bearer <token>，面板静态资源和 /healthz 免认证
func (s *Server) auth(next http.Handler) http.Handler {
	want := []byte(s.token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
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
