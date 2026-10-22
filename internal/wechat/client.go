// Package wechat 封装微信公众号服务端接口调用。
package wechat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	DefaultBaseURL = "https://api.weixin.qq.com"

	maxRespSize  = 32 << 20
	refreshAhead = 5 * time.Minute
)

// token 失效类错误码：遇到后刷新 token 重试一次
var tokenErrCodes = map[int]bool{40001: true, 40014: true, 42001: true}

// Error 微信接口返回的业务错误（errcode != 0）
type Error struct {
	Code int    `json:"errcode"`
	Msg  string `json:"errmsg"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("wechat: errcode=%d errmsg=%s", e.Code, e.Msg)
}

type Client struct {
	appID   string
	secret  string
	baseURL string
	hc      *http.Client

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

type Option func(*Client)

func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") }
}

func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.hc = hc }
}

func New(appID, secret string, opts ...Option) *Client {
	c := &Client{
		appID:   appID,
		secret:  secret,
		baseURL: DefaultBaseURL,
		hc:      &http.Client{Timeout: 60 * time.Second},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// AccessToken 获取稳定版 access_token（普通模式），本地缓存并在过期前 5 分钟刷新。
func (c *Client) AccessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expiresAt) {
		return c.token, nil
	}

	body, err := marshal(map[string]any{
		"grant_type":    "client_credential",
		"appid":         c.appID,
		"secret":        c.secret,
		"force_refresh": false,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/cgi-bin/stable_token", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("wechat: get stable token: %w", err)
	}
	defer resp.Body.Close()

	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", fmt.Errorf("wechat: decode stable token: %w", err)
	}
	if out.ErrCode != 0 {
		return "", &Error{Code: out.ErrCode, Msg: out.ErrMsg}
	}
	if out.AccessToken == "" {
		return "", errors.New("wechat: empty access_token")
	}

	ttl := time.Duration(out.ExpiresIn)*time.Second - refreshAhead
	if ttl < time.Minute {
		ttl = time.Minute
	}
	c.token = out.AccessToken
	c.expiresAt = time.Now().Add(ttl)
	return c.token, nil
}

// invalidate 丢弃失效的 token；如果已被其他请求刷新过则保留新 token
func (c *Client) invalidate(stale string) {
	c.mu.Lock()
	if c.token == stale {
		c.token = ""
	}
	c.mu.Unlock()
}

type request struct {
	method      string
	path        string
	query       url.Values
	contentType string
	body        []byte
}

// do 带 access_token 调用接口，token 失效时刷新并重试一次
func (c *Client) do(ctx context.Context, r request) (http.Header, []byte, error) {
	for attempt := 0; ; attempt++ {
		token, err := c.AccessToken(ctx)
		if err != nil {
			return nil, nil, err
		}
		header, data, err := c.send(ctx, r, token)
		var we *Error
		if attempt == 0 && errors.As(err, &we) && tokenErrCodes[we.Code] {
			c.invalidate(token)
			continue
		}
		return header, data, err
	}
}

func (c *Client) send(ctx context.Context, r request, token string) (http.Header, []byte, error) {
	q := url.Values{}
	for k, v := range r.query {
		q[k] = v
	}
	q.Set("access_token", token)

	var body io.Reader
	if r.body != nil {
		body = bytes.NewReader(r.body)
	}
	req, err := http.NewRequestWithContext(ctx, r.method, c.baseURL+r.path+"?"+q.Encode(), body)
	if err != nil {
		return nil, nil, err
	}
	if r.contentType != "" {
		req.Header.Set("Content-Type", r.contentType)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("wechat: %s: %w", r.path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxRespSize+1))
	if err != nil {
		return nil, nil, fmt.Errorf("wechat: %s: read body: %w", r.path, err)
	}
	if len(data) > maxRespSize {
		return nil, nil, fmt.Errorf("wechat: %s: response too large", r.path)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("wechat: %s: http status %d", r.path, resp.StatusCode)
	}
	if err := checkErr(data); err != nil {
		return nil, nil, err
	}
	return resp.Header, data, nil
}

// checkErr 从 JSON 响应中识别 errcode；非 JSON（如素材二进制）直接放行
func checkErr(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	var e Error
	if json.Unmarshal(trimmed, &e) != nil {
		return nil
	}
	if e.Code != 0 {
		return &e
	}
	return nil
}

// marshal 编码 JSON 且不转义 HTML 字符，避免文章正文中的 <、>、& 被写成 < 等
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (c *Client) postRaw(ctx context.Context, path string, v any) (http.Header, []byte, error) {
	body, err := marshal(v)
	if err != nil {
		return nil, nil, err
	}
	return c.do(ctx, request{
		method:      http.MethodPost,
		path:        path,
		contentType: "application/json; charset=utf-8",
		body:        body,
	})
}

func (c *Client) postJSON(ctx context.Context, path string, v any) (json.RawMessage, error) {
	_, data, err := c.postRaw(ctx, path, v)
	return data, err
}

func (c *Client) getJSON(ctx context.Context, path string) (json.RawMessage, error) {
	_, data, err := c.do(ctx, request{method: http.MethodGet, path: path})
	return data, err
}

// File 待上传的文件
type File struct {
	Name string
	Data []byte
}

// upload 以 multipart/form-data 上传，文件字段名固定为 media
func (c *Client) upload(ctx context.Context, path string, query url.Values, f File, fields map[string]string) (json.RawMessage, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return nil, err
		}
	}

	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="media"; filename=%q`, f.Name))
	h.Set("Content-Type", http.DetectContentType(f.Data))
	pw, err := mw.CreatePart(h)
	if err != nil {
		return nil, err
	}
	if _, err := pw.Write(f.Data); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	_, data, err := c.do(ctx, request{
		method:      http.MethodPost,
		path:        path,
		query:       query,
		contentType: mw.FormDataContentType(),
		body:        buf.Bytes(),
	})
	return data, err
}
