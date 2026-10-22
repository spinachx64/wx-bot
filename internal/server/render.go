package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"wx-bot/internal/render"
)

// skinList 返回可用皮肤及其配置
func (s *Server) skinList(w http.ResponseWriter, _ *http.Request) {
	data, _ := json.Marshal(map[string]any{
		"default": render.DefaultSkin,
		"skins":   s.render.Skins().List(),
	})
	writeJSON(w, http.StatusOK, data)
}

// renderMarkdown 请求体：{"markdown":"...","skin":"summer-breeze"}，返回 {"html":"..."}
func (s *Server) renderMarkdown(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Markdown string `json:"markdown"`
		Skin     string `json:"skin"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	out, err := s.render.Render(req.Markdown, req.Skin)
	if err != nil {
		s.writeRenderError(w, err)
		return
	}
	data, _ := marshalNoEscape(map[string]string{"html": out})
	writeJSON(w, http.StatusOK, data)
}

func (s *Server) writeRenderError(w http.ResponseWriter, err error) {
	if errors.Is(err, render.ErrUnknownSkin) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.log.Error("render failed", "err", err)
	writeError(w, http.StatusInternalServerError, "render failed")
}

// renderArticle 文章里带 markdown 字段时渲染成 content，并去掉 markdown/skin，
// 其余字段原样透传给微信
func (s *Server) renderArticle(raw json.RawMessage) (json.RawMessage, error) {
	var art map[string]json.RawMessage
	if err := json.Unmarshal(raw, &art); err != nil {
		return nil, errBadArticle
	}
	mdRaw, ok := art["markdown"]
	if !ok {
		return raw, nil
	}
	var md, skin string
	if err := json.Unmarshal(mdRaw, &md); err != nil {
		return nil, fmt.Errorf("%w: markdown must be a string", errBadArticle)
	}
	if v, ok := art["skin"]; ok {
		if err := json.Unmarshal(v, &skin); err != nil {
			return nil, fmt.Errorf("%w: skin must be a string", errBadArticle)
		}
	}
	out, err := s.render.Render(md, skin)
	if err != nil {
		return nil, err
	}
	content, err := marshalNoEscape(out)
	if err != nil {
		return nil, err
	}
	art["content"] = content
	delete(art, "markdown")
	delete(art, "skin")
	return marshalNoEscape(art)
}

var errBadArticle = errors.New("invalid article")

// writeArticleError 区分请求错误（400）和渲染内部错误
func (s *Server) writeArticleError(w http.ResponseWriter, err error) {
	if errors.Is(err, errBadArticle) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeRenderError(w, err)
}
