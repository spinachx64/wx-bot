package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"time"
)

// Client 调用 wx-bot 服务的 HTTP 接口
type Client struct {
	base  string
	token string
	hc    *http.Client
}

func NewClient(c *Config) *Client {
	return &Client{base: c.URL, token: c.Token, hc: &http.Client{Timeout: 120 * time.Second}}
}

// APIError 服务返回的非 2xx 响应，errcode 可能是微信的原始错误码
type APIError struct {
	Status int
	Code   int    `json:"errcode"`
	Msg    string `json:"errmsg"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("api error: http %d errcode=%d errmsg=%s", e.Status, e.Code, e.Msg)
}

func (c *Client) do(ctx context.Context, method, path, ct string, body io.Reader, out any) error {
	if c.token == "" {
		return fmt.Errorf("token not set: run `wxctl login` or set WXBOT_TOKEN")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		e := &APIError{Status: resp.StatusCode}
		if json.Unmarshal(data, e) != nil || e.Msg == "" {
			e.Msg = string(bytes.TrimSpace(data))
		}
		return e
	}
	if out == nil {
		return nil
	}
	if raw, ok := out.(*json.RawMessage); ok {
		*raw = data
		return nil
	}
	return json.Unmarshal(data, out)
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, "", nil, out)
}

func (c *Client) sendJSON(ctx context.Context, method, path string, in, out any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(in); err != nil {
		return err
	}
	return c.do(ctx, method, path, "application/json", &buf, out)
}

func (c *Client) upload(ctx context.Context, path, name string, data []byte, fields map[string]string, out any) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return err
		}
	}
	fw, err := mw.CreateFormFile("media", filepath.Base(name))
	if err != nil {
		return err
	}
	if _, err := fw.Write(data); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, path, mw.FormDataContentType(), &buf, out)
}

func (c *Client) Ping(ctx context.Context) error {
	return c.getJSON(ctx, "/api/v1/ping", nil)
}

// UploadImage 上传正文图片，返回 mmbiz.qpic.cn 地址
func (c *Client) UploadImage(ctx context.Context, name string, data []byte) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	if err := c.upload(ctx, "/api/v1/materials/images", name, data, nil, &out); err != nil {
		return "", err
	}
	if out.URL == "" {
		return "", fmt.Errorf("upload image: empty url")
	}
	return out.URL, nil
}

// AddMaterial 上传永久素材，返回 media_id
func (c *Client) AddMaterial(ctx context.Context, typ, name string, data []byte) (string, error) {
	var out struct {
		MediaID string `json:"media_id"`
	}
	if err := c.upload(ctx, "/api/v1/materials", name, data, map[string]string{"type": typ}, &out); err != nil {
		return "", err
	}
	if out.MediaID == "" {
		return "", fmt.Errorf("add material: empty media_id")
	}
	return out.MediaID, nil
}

func (c *Client) Render(ctx context.Context, markdown, skin string) (string, error) {
	var out struct {
		HTML string `json:"html"`
	}
	err := c.sendJSON(ctx, http.MethodPost, "/api/v1/render", map[string]string{"markdown": markdown, "skin": skin}, &out)
	return out.HTML, err
}

func (c *Client) DraftAdd(ctx context.Context, article map[string]any) (string, error) {
	var out struct {
		MediaID string `json:"media_id"`
	}
	err := c.sendJSON(ctx, http.MethodPost, "/api/v1/drafts", map[string]any{"articles": []any{article}}, &out)
	return out.MediaID, err
}

func (c *Client) DraftUpdate(ctx context.Context, mediaID string, index int, article map[string]any) error {
	return c.sendJSON(ctx, http.MethodPut, "/api/v1/drafts/"+url.PathEscape(mediaID),
		map[string]any{"index": index, "articles": article}, nil)
}

func (c *Client) PublishSubmit(ctx context.Context, mediaID string) (string, error) {
	var out struct {
		PublishID json.Number `json:"publish_id"`
	}
	err := c.sendJSON(ctx, http.MethodPost, "/api/v1/drafts/"+url.PathEscape(mediaID)+"/publish", struct{}{}, &out)
	return out.PublishID.String(), err
}

// Raw 直接返回服务端 JSON，用于列表、状态等透传类命令
func (c *Client) Raw(ctx context.Context, method, path string) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.do(ctx, method, path, "", nil, &out)
	return out, err
}
