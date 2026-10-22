package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"wx-bot/internal/wechat"
)

// ---------- 草稿管理 ----------

// draftAdd 请求体：{"articles":[...]}，文章字段与微信文档一致
func (s *Server) draftAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Articles json.RawMessage `json:"articles"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if len(req.Articles) == 0 || req.Articles[0] != '[' {
		writeError(w, http.StatusBadRequest, "articles must be a non-empty array")
		return
	}
	data, err := s.wx.DraftAdd(r.Context(), req.Articles)
	s.writeResult(w, r, data, err)
}

// draftUpdate 请求体：{"index":0,"articles":{...}}
func (s *Server) draftUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Index    *int            `json:"index"`
		Articles json.RawMessage `json:"articles"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Index == nil || *req.Index < 0 {
		writeError(w, http.StatusBadRequest, "index is required and must be >= 0")
		return
	}
	if len(req.Articles) == 0 || req.Articles[0] != '{' {
		writeError(w, http.StatusBadRequest, "articles must be an object")
		return
	}
	data, err := s.wx.DraftUpdate(r.Context(), r.PathValue("media_id"), *req.Index, req.Articles)
	s.writeResult(w, r, data, err)
}

func (s *Server) draftList(w http.ResponseWriter, r *http.Request) {
	offset, count, ok := pageParams(w, r)
	if !ok {
		return
	}
	data, err := s.wx.DraftBatchGet(r.Context(), offset, count, boolParam(r, "no_content"))
	s.writeResult(w, r, data, err)
}

func (s *Server) draftCount(w http.ResponseWriter, r *http.Request) {
	data, err := s.wx.DraftCount(r.Context())
	s.writeResult(w, r, data, err)
}

func (s *Server) draftGet(w http.ResponseWriter, r *http.Request) {
	data, err := s.wx.DraftGet(r.Context(), r.PathValue("media_id"))
	s.writeResult(w, r, data, err)
}

func (s *Server) draftDelete(w http.ResponseWriter, r *http.Request) {
	data, err := s.wx.DraftDelete(r.Context(), r.PathValue("media_id"))
	s.writeResult(w, r, data, err)
}

// ---------- 发布能力 ----------

func (s *Server) publishSubmit(w http.ResponseWriter, r *http.Request) {
	data, err := s.wx.PublishSubmit(r.Context(), r.PathValue("media_id"))
	s.writeResult(w, r, data, err)
}

func (s *Server) publishList(w http.ResponseWriter, r *http.Request) {
	offset, count, ok := pageParams(w, r)
	if !ok {
		return
	}
	data, err := s.wx.PublishBatchGet(r.Context(), offset, count, boolParam(r, "no_content"))
	s.writeResult(w, r, data, err)
}

func (s *Server) publishGet(w http.ResponseWriter, r *http.Request) {
	data, err := s.wx.PublishGet(r.Context(), r.PathValue("publish_id"))
	s.writeResult(w, r, data, err)
}

func (s *Server) articleGet(w http.ResponseWriter, r *http.Request) {
	data, err := s.wx.PublishGetArticle(r.Context(), r.PathValue("article_id"))
	s.writeResult(w, r, data, err)
}

// articleDelete 可选 ?index=N（从 1 开始），缺省为 0 即删除全部
func (s *Server) articleDelete(w http.ResponseWriter, r *http.Request) {
	index := 0
	if v := r.URL.Query().Get("index"); v != "" {
		var err error
		if index, err = strconv.Atoi(v); err != nil || index < 0 {
			writeError(w, http.StatusBadRequest, "invalid index")
			return
		}
	}
	data, err := s.wx.PublishDelete(r.Context(), r.PathValue("article_id"), index)
	s.writeResult(w, r, data, err)
}

// ---------- 素材管理 ----------

var materialTypes = map[string]bool{"image": true, "voice": true, "video": true, "thumb": true}
var listTypes = map[string]bool{"image": true, "voice": true, "video": true, "news": true}

// readUpload 解析 multipart 表单中的 media 文件字段
func readUpload(w http.ResponseWriter, r *http.Request) (wechat.File, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBody)
	if err := r.ParseMultipartForm(maxUploadBody); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "file too large")
		} else {
			writeError(w, http.StatusBadRequest, "invalid multipart form: "+err.Error())
		}
		return wechat.File{}, false
	}
	f, fh, err := r.FormFile("media")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing form file field: media")
		return wechat.File{}, false
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		writeError(w, http.StatusBadRequest, "read upload failed")
		return wechat.File{}, false
	}
	return wechat.File{Name: fh.Filename, Data: data}, true
}

// materialAdd multipart 表单：type、media，视频另需 title、introduction
func (s *Server) materialAdd(w http.ResponseWriter, r *http.Request) {
	f, ok := readUpload(w, r)
	if !ok {
		return
	}
	defer r.MultipartForm.RemoveAll()
	typ := r.FormValue("type")
	if !materialTypes[typ] {
		writeError(w, http.StatusBadRequest, "type must be image/voice/video/thumb")
		return
	}
	title, intro := r.FormValue("title"), r.FormValue("introduction")
	if typ == "video" && title == "" {
		writeError(w, http.StatusBadRequest, "video requires title")
		return
	}
	data, err := s.wx.MaterialAdd(r.Context(), typ, f, title, intro)
	s.writeResult(w, r, data, err)
}

// uploadImage 上传正文图片，multipart 字段 media
func (s *Server) uploadImage(w http.ResponseWriter, r *http.Request) {
	f, ok := readUpload(w, r)
	if !ok {
		return
	}
	defer r.MultipartForm.RemoveAll()
	data, err := s.wx.UploadImage(r.Context(), f)
	s.writeResult(w, r, data, err)
}

// materialList ?type=image|voice|video|news&offset=&count=
func (s *Server) materialList(w http.ResponseWriter, r *http.Request) {
	typ := r.URL.Query().Get("type")
	if !listTypes[typ] {
		writeError(w, http.StatusBadRequest, "type must be image/voice/video/news")
		return
	}
	offset, count, ok := pageParams(w, r)
	if !ok {
		return
	}
	data, err := s.wx.MaterialBatchGet(r.Context(), typ, offset, count)
	s.writeResult(w, r, data, err)
}

func (s *Server) materialCount(w http.ResponseWriter, r *http.Request) {
	data, err := s.wx.MaterialCount(r.Context())
	s.writeResult(w, r, data, err)
}

// materialGet 图文/视频返回 JSON，其他类型透传文件内容
func (s *Server) materialGet(w http.ResponseWriter, r *http.Request) {
	header, data, err := s.wx.MaterialGet(r.Context(), r.PathValue("media_id"))
	if err != nil {
		s.writeResult(w, r, nil, err)
		return
	}
	for _, k := range []string{"Content-Type", "Content-Disposition"} {
		if v := header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", http.DetectContentType(data))
	}
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

func (s *Server) materialDelete(w http.ResponseWriter, r *http.Request) {
	data, err := s.wx.MaterialDelete(r.Context(), r.PathValue("media_id"))
	s.writeResult(w, r, data, err)
}
