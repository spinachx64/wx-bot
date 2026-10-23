package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"wx-bot/internal/render"
)

// themeList 返回可用主题及其配置
func (s *Server) themeList(w http.ResponseWriter, _ *http.Request) {
	data, _ := json.Marshal(map[string]any{
		"default": render.DefaultTheme,
		"themes":  s.render.Themes().List(),
	})
	writeJSON(w, http.StatusOK, data)
}

// renderMarkdown 请求体：{"markdown":"...","theme":"summer-breeze"}，返回 {"html":"..."}。
// 带 theme_css 时用这份 CSS 渲染（忽略 theme），只在本次请求内使用，不落盘
func (s *Server) renderMarkdown(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Markdown string `json:"markdown"`
		Theme    string `json:"theme"`
		ThemeCSS string `json:"theme_css"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	out, err := s.renderWith(req.Markdown, req.Theme, req.ThemeCSS)
	if err != nil {
		s.writeRenderError(w, err)
		return
	}
	data, _ := marshalNoEscape(map[string]string{"html": out})
	writeJSON(w, http.StatusOK, data)
}

// renderWith 有自定义 CSS 时用它，否则按主题名
func (s *Server) renderWith(md, theme, css string) (string, error) {
	if css == "" {
		return s.render.Render(md, theme)
	}
	t, err := render.ParseTheme("custom", []byte(css))
	if err != nil {
		return "", err
	}
	return s.render.RenderWith(md, t)
}

func (s *Server) writeRenderError(w http.ResponseWriter, err error) {
	if errors.Is(err, render.ErrUnknownTheme) || errors.Is(err, render.ErrBadCustomCSS) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.log.Error("render failed", "err", err)
	writeError(w, http.StatusInternalServerError, "render failed")
}

// renderArticle 文章里带 markdown 字段时渲染成 content，并去掉 markdown/theme，
// 其余字段原样透传给微信
func (s *Server) renderArticle(raw json.RawMessage) (json.RawMessage, error) {
	var art map[string]json.RawMessage
	if err := json.Unmarshal(raw, &art); err != nil {
		return nil, errBadArticle
	}
	// 没有显式指定时默认打开评论
	if _, ok := art["need_open_comment"]; !ok {
		art["need_open_comment"] = json.RawMessage("1")
	}
	mdRaw, ok := art["markdown"]
	if !ok {
		return marshalNoEscape(art)
	}
	var md, theme, css string
	if err := json.Unmarshal(mdRaw, &md); err != nil {
		return nil, fmt.Errorf("%w: markdown must be a string", errBadArticle)
	}
	for key, dst := range map[string]*string{"theme": &theme, "theme_css": &css} {
		if v, ok := art[key]; ok {
			if err := json.Unmarshal(v, dst); err != nil {
				return nil, fmt.Errorf("%w: %s must be a string", errBadArticle, key)
			}
		}
	}
	out, err := s.renderWith(md, theme, css)
	if err != nil {
		return nil, err
	}
	content, err := marshalNoEscape(out)
	if err != nil {
		return nil, err
	}
	art["content"] = content
	delete(art, "markdown")
	delete(art, "theme")
	delete(art, "theme_css")
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
