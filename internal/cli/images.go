package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "image/gif"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
)

const (
	maxContentImage = 1 << 20  // 正文图片：jpg/png，< 1MB
	maxCoverImage   = 10 << 20 // 永久图片素材：< 10MB
	maxDownload     = 30 << 20
	maxImageWidth   = 1920
)

// Uploader 负责图片的读取、压缩、上传，并按内容 hash 缓存结果，避免重复上传
type Uploader struct {
	client *Client
	cache  *imageCache
	hc     *http.Client
	Log    func(format string, args ...any)
}

func NewUploader(c *Client, cfg *Config) *Uploader {
	return &Uploader{
		client: c,
		// token 指纹用于隔离同一服务下的不同公众号，不把 token 明文写入缓存。
		cache: &imageCache{path: filepath.Join(cfg.CacheDir, "images.json"), server: cfg.URL + "|" + hash([]byte(cfg.Token))},
		hc:    &http.Client{Timeout: 60 * time.Second},
		Log:   func(string, ...any) {},
	}
}

// PrepareArticle 上传正文里的本地 / 外链图片，并把地址替换成微信图床地址
func (u *Uploader) PrepareArticle(ctx context.Context, a *Article) error {
	repl := map[string]string{}
	for _, src := range a.Images() {
		if IsWechat(src) {
			continue
		}
		url, err := u.ContentImage(ctx, a, src)
		if err != nil {
			return fmt.Errorf("image %s: %w", src, err)
		}
		repl[src] = url
	}
	a.ReplaceImages(func(src string) string {
		if v, ok := repl[src]; ok {
			return v
		}
		return src
	})
	return nil
}

// ContentImage 上传正文图片，返回 url
func (u *Uploader) ContentImage(ctx context.Context, a *Article, src string) (string, error) {
	data, name, err := u.load(ctx, a, src)
	if err != nil {
		return "", err
	}
	key := "img:" + hash(data)
	if v := u.cache.get(key); v != "" {
		u.Log("cached  %s", src)
		return v, nil
	}
	data, name, err = fitImage(data, name, maxContentImage)
	if err != nil {
		return "", err
	}
	url, err := u.client.UploadImage(ctx, name, data)
	if err != nil {
		return "", err
	}
	u.Log("upload  %s -> %s", src, url)
	return url, u.cache.put(key, url)
}

// Cover 上传封面为永久图片素材，返回 media_id
func (u *Uploader) Cover(ctx context.Context, a *Article, src string) (string, error) {
	return u.MaterialImage(ctx, a, src)
}

type preparedMaterial struct {
	src, id, key, name string
	data               []byte
}

func (u *Uploader) prepareMaterial(ctx context.Context, a *Article, src string) (preparedMaterial, error) {
	p := preparedMaterial{src: src}
	kind, value, err := imageSource(src)
	if err != nil {
		return p, err
	}
	if kind == "media_id" {
		p.id = value
		return p, nil
	}
	data, name, err := u.load(ctx, a, src)
	if err != nil {
		return p, err
	}
	p.key = "material:image:" + hash(data)
	if p.id = u.cache.get(p.key); p.id != "" {
		return p, nil
	}
	if _, _, err := image.DecodeConfig(bytes.NewReader(data)); err != nil {
		return p, fmt.Errorf("unsupported image: %w", err)
	}
	p.data, p.name, err = fitImage(data, name, maxCoverImage)
	return p, err
}

func (u *Uploader) uploadMaterial(ctx context.Context, p preparedMaterial) (string, error) {
	if p.id != "" {
		return p.id, nil
	}
	// 同一批次里相同文件只上传一次。
	if id := u.cache.get(p.key); id != "" {
		return id, nil
	}
	id, err := u.client.AddMaterial(ctx, "image", p.name, p.data)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", fmt.Errorf("material upload: empty media_id")
	}
	u.Log("upload  material %s -> %s", p.src, id)
	return id, u.cache.put(p.key, id)
}

// MaterialImage 上传永久图片素材；本地绝对路径、相对路径、URL 和已有素材 ID 均可用。
func (u *Uploader) MaterialImage(ctx context.Context, a *Article, src string) (string, error) {
	p, err := u.prepareMaterial(ctx, a, src)
	if err != nil {
		return "", err
	}
	return u.uploadMaterial(ctx, p)
}

// MaterialImages 先读取、下载、校验并压缩全部图片，再进行任何上传。
func (u *Uploader) MaterialImages(ctx context.Context, a *Article) ([]string, error) {
	prepared := make([]preparedMaterial, len(a.Meta.Images))
	for i, src := range a.Meta.Images {
		p, err := u.prepareMaterial(ctx, a, src)
		if err != nil {
			return nil, fmt.Errorf("images[%d] %s: %w", i, src, err)
		}
		prepared[i] = p
	}
	ids := make([]string, len(prepared))
	for i, p := range prepared {
		id, err := u.uploadMaterial(ctx, p)
		if err != nil {
			return nil, fmt.Errorf("images[%d] %s: %w", i, p.src, err)
		}
		ids[i] = id
	}
	return ids, nil
}

// load 读取本地文件或下载外链图片
func (u *Uploader) load(ctx context.Context, a *Article, src string) ([]byte, string, error) {
	if !isRemote(src) {
		p := a.resolveLocal(src)
		data, err := os.ReadFile(p)
		return data, filepath.Base(p), err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := u.hc.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("download: http %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxDownload {
		return nil, "", fmt.Errorf("download: larger than %dMB", maxDownload>>20)
	}
	name := filepath.Base(req.URL.Path)
	if name == "" || name == "/" || name == "." {
		name = "image"
	}
	return data, name, nil
}

// fitImage jpg/png 且不超限时原样返回；否则转码（必要时缩小宽度、降低质量）为 jpeg
func fitImage(data []byte, name string, limit int) ([]byte, string, error) {
	ct := http.DetectContentType(data)
	if (ct == "image/jpeg" || ct == "image/png") && len(data) <= limit {
		return data, withExt(name, ct), nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("unsupported image (%s): %w", ct, err)
	}
	// PNG 转码保留原格式（截图类图片 jpeg 会糊），仅在超限时缩小
	if ct == "image/png" {
		for _, w := range widths(img.Bounds().Dx()) {
			var buf bytes.Buffer
			if err := png.Encode(&buf, resize(img, w)); err != nil {
				return nil, "", err
			}
			if buf.Len() <= limit {
				return buf.Bytes(), withExt(name, ct), nil
			}
		}
	}
	img = flatten(img) // jpeg 不支持透明，铺白底
	for _, w := range widths(img.Bounds().Dx()) {
		scaled := resize(img, w)
		for _, q := range []int{90, 80, 70, 60} {
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: q}); err != nil {
				return nil, "", err
			}
			if buf.Len() <= limit {
				return buf.Bytes(), withExt(name, "image/jpeg"), nil
			}
		}
	}
	return nil, "", fmt.Errorf("cannot compress below %dKB", limit>>10)
}

// widths 依次尝试的宽度：从原宽（不超过 maxImageWidth）开始每次缩到 3/4，
// 不低于 320；比 320 还窄的小图只尝试原宽一次
func widths(w int) []int {
	w = min(w, maxImageWidth)
	out := []int{w}
	for w = w * 3 / 4; w >= 320; w = w * 3 / 4 {
		out = append(out, w)
	}
	return out
}

func withExt(name, ct string) string {
	ext := ".jpg"
	if ct == "image/png" {
		ext = ".png"
	}
	return strings.TrimSuffix(name, filepath.Ext(name)) + ext
}

func flatten(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, image.White, image.Point{}, draw.Src)
	draw.Draw(dst, b, src, b.Min, draw.Over)
	return dst
}

// resize 按宽度等比缩放（最近邻取样足够应付压缩场景，避免引入额外依赖）
func resize(src image.Image, w int) image.Image {
	b := src.Bounds()
	if w >= b.Dx() {
		return src
	}
	h := max(1, b.Dy()*w/b.Dx())
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		sy := b.Min.Y + y*b.Dy()/h
		for x := 0; x < w; x++ {
			dst.Set(x, y, src.At(b.Min.X+x*b.Dx()/w, sy))
		}
	}
	return dst
}

func hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// imageCache 以「服务地址 + 内容 hash」为键记录上传结果，存成 JSON 文件
type imageCache struct {
	path, server string
	mu           sync.Mutex
	data         map[string]string
}

func (c *imageCache) load() {
	if c.data != nil {
		return
	}
	c.data = map[string]string{}
	if b, err := os.ReadFile(c.path); err == nil {
		json.Unmarshal(b, &c.data)
	}
}

func (c *imageCache) get(key string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	return c.data[c.server+"|"+key]
}

func (c *imageCache) put(key, val string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	c.data[c.server+"|"+key] = val
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}
