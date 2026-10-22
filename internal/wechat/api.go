package wechat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// 以下接口均直接返回微信的原始 JSON 响应。

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---------- 草稿管理 ----------

// DraftAdd 新增草稿，articles 为文章数组
func (c *Client) DraftAdd(ctx context.Context, articles json.RawMessage) (json.RawMessage, error) {
	return c.postJSON(ctx, "/cgi-bin/draft/add", map[string]any{"articles": articles})
}

// DraftUpdate 更新草稿中第 index 篇文章（从 0 开始）
func (c *Client) DraftUpdate(ctx context.Context, mediaID string, index int, article json.RawMessage) (json.RawMessage, error) {
	return c.postJSON(ctx, "/cgi-bin/draft/update", map[string]any{
		"media_id": mediaID,
		"index":    index,
		"articles": article,
	})
}

// DraftBatchGet 获取草稿列表
func (c *Client) DraftBatchGet(ctx context.Context, offset, count int, noContent bool) (json.RawMessage, error) {
	return c.postJSON(ctx, "/cgi-bin/draft/batchget", map[string]any{
		"offset":     offset,
		"count":      count,
		"no_content": boolInt(noContent),
	})
}

// DraftCount 获取草稿总数
func (c *Client) DraftCount(ctx context.Context) (json.RawMessage, error) {
	return c.getJSON(ctx, "/cgi-bin/draft/count")
}

// DraftDelete 删除草稿
func (c *Client) DraftDelete(ctx context.Context, mediaID string) (json.RawMessage, error) {
	return c.postJSON(ctx, "/cgi-bin/draft/delete", map[string]any{"media_id": mediaID})
}

// DraftGet 获取草稿详情
func (c *Client) DraftGet(ctx context.Context, mediaID string) (json.RawMessage, error) {
	return c.postJSON(ctx, "/cgi-bin/draft/get", map[string]any{"media_id": mediaID})
}

// ---------- 素材管理 ----------

// MaterialGet 获取永久素材。图文/视频素材返回 JSON，其他类型返回文件二进制内容
func (c *Client) MaterialGet(ctx context.Context, mediaID string) (http.Header, []byte, error) {
	return c.postRaw(ctx, "/cgi-bin/material/get_material", map[string]any{"media_id": mediaID})
}

// MaterialCount 获取永久素材总数
func (c *Client) MaterialCount(ctx context.Context) (json.RawMessage, error) {
	return c.getJSON(ctx, "/cgi-bin/material/get_materialcount")
}

// MaterialBatchGet 获取永久素材列表，typ 为 image/video/voice/news
func (c *Client) MaterialBatchGet(ctx context.Context, typ string, offset, count int) (json.RawMessage, error) {
	return c.postJSON(ctx, "/cgi-bin/material/batchget_material", map[string]any{
		"type":   typ,
		"offset": offset,
		"count":  count,
	})
}

// UploadImage 上传图文消息内的图片，返回可在正文中使用的 url
func (c *Client) UploadImage(ctx context.Context, f File) (json.RawMessage, error) {
	return c.upload(ctx, "/cgi-bin/media/uploadimg", nil, f, nil)
}

// MaterialAdd 上传永久素材，typ 为 image/voice/video/thumb；视频素材需提供标题和简介
func (c *Client) MaterialAdd(ctx context.Context, typ string, f File, title, introduction string) (json.RawMessage, error) {
	var fields map[string]string
	if typ == "video" {
		desc, err := marshal(map[string]string{"title": title, "introduction": introduction})
		if err != nil {
			return nil, err
		}
		fields = map[string]string{"description": string(desc)}
	}
	return c.upload(ctx, "/cgi-bin/material/add_material", url.Values{"type": {typ}}, f, fields)
}

// MaterialDelete 删除永久素材
func (c *Client) MaterialDelete(ctx context.Context, mediaID string) (json.RawMessage, error) {
	return c.postJSON(ctx, "/cgi-bin/material/del_material", map[string]any{"media_id": mediaID})
}

// ---------- 发布能力 ----------

// PublishSubmit 发布草稿，返回 publish_id
func (c *Client) PublishSubmit(ctx context.Context, mediaID string) (json.RawMessage, error) {
	return c.postJSON(ctx, "/cgi-bin/freepublish/submit", map[string]any{"media_id": mediaID})
}

// PublishGet 发布状态查询
func (c *Client) PublishGet(ctx context.Context, publishID string) (json.RawMessage, error) {
	return c.postJSON(ctx, "/cgi-bin/freepublish/get", map[string]any{"publish_id": publishID})
}

// PublishDelete 删除发布的文章；index 从 1 开始，0 表示删除全部
func (c *Client) PublishDelete(ctx context.Context, articleID string, index int) (json.RawMessage, error) {
	return c.postJSON(ctx, "/cgi-bin/freepublish/delete", map[string]any{
		"article_id": articleID,
		"index":      index,
	})
}

// PublishBatchGet 获取已发布的消息列表
func (c *Client) PublishBatchGet(ctx context.Context, offset, count int, noContent bool) (json.RawMessage, error) {
	return c.postJSON(ctx, "/cgi-bin/freepublish/batchget", map[string]any{
		"offset":     offset,
		"count":      count,
		"no_content": boolInt(noContent),
	})
}

// PublishGetArticle 获取已发布的图文信息
func (c *Client) PublishGetArticle(ctx context.Context, articleID string) (json.RawMessage, error) {
	return c.postJSON(ctx, "/cgi-bin/freepublish/getarticle", map[string]any{"article_id": articleID})
}
