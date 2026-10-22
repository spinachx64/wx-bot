package cli

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
)

// readArticle 读取文件，"-" 表示标准输入（图片相对路径以当前目录为基准）
func (e *env) readArticle(src string) (*Article, error) {
	if src != "-" {
		return LoadArticle(src)
	}
	data, err := io.ReadAll(io.LimitReader(e.in, 16<<20))
	if err != nil {
		return nil, err
	}
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return ParseArticle(data, wd)
}

func (e *env) uploader() *Uploader {
	u := NewUploader(e.client, e.cfg)
	u.Log = e.logf
	return u
}

func cmdRender(e *env, args []string) error {
	fs := newFlagSet(e, "render", "<md|-> [--skin name] [--upload-images]")
	skin := fs.String("skin", "", "皮肤，默认取 frontmatter / 配置 / 服务端默认")
	upload := fs.Bool("upload-images", false, "先上传图片并替换为微信地址")
	pos, err := parseArgs(fs, args, 1)
	if err != nil {
		return err
	}
	a, err := e.readArticle(pos[0])
	if err != nil {
		return err
	}
	if *upload {
		if err := e.uploader().PrepareArticle(e.ctx, a); err != nil {
			return err
		}
	}
	html, err := e.client.Render(e.ctx, a.Markdown, first(*skin, a.Meta.Skin, e.cfg.Skin))
	if err != nil {
		return err
	}
	return e.result(map[string]any{"title": a.Meta.Title, "html": html}, html)
}

func cmdDraft(e *env, args []string) error {
	fs := newFlagSet(e, "draft", "<md|-> [参数]")
	skin := fs.String("skin", "", "皮肤")
	cover := fs.String("cover", "", "封面图路径或 URL（覆盖 frontmatter 的 cover）")
	thumb := fs.String("thumb", "", "直接使用已有的封面 media_id")
	mediaID := fs.String("media-id", "", "更新指定草稿，不传则新建")
	index := fs.Int("index", 0, "更新草稿中的第几篇（从 0 开始）")
	title := fs.String("title", "", "标题")
	author := fs.String("author", "", "作者")
	digest := fs.String("digest", "", "摘要")
	publish := fs.Bool("publish", false, "保存草稿后立即发布")
	noCheck := fs.Bool("no-check", false, "跳过提交前的检查")
	pos, err := parseArgs(fs, args, 1)
	if err != nil {
		return err
	}
	a, err := e.readArticle(pos[0])
	if err != nil {
		return err
	}
	m := &a.Meta
	m.Title = first(*title, m.Title)
	m.Author = first(*author, m.Author)
	m.Digest = first(*digest, m.Digest)
	if m.Title == "" {
		return usageError("title is required: add frontmatter title, a leading # heading, or --title")
	}
	m.Cover = first(*cover, m.Cover)
	if m.Cover == "" && *thumb == "" {
		return usageError("cover is required: add frontmatter cover, --cover or --thumb")
	}

	// 上传前先做离线检查，有错误就不动线上数据
	if !*noCheck {
		var errs []Issue
		for _, is := range CheckArticle(a, nil) {
			if is.Level == levelError && !(is.Field == "cover" && *thumb != "") {
				errs = append(errs, is)
			}
		}
		if len(errs) > 0 {
			return fmt.Errorf("check failed, nothing uploaded (use --no-check to skip):\n%s", formatIssues(pos[0], errs))
		}
	}

	up := e.uploader()
	if err := up.PrepareArticle(e.ctx, a); err != nil {
		return err
	}
	thumbID := *thumb
	if thumbID == "" {
		src := first(*cover, m.Cover)
		if src == "" {
			return usageError("cover is required: add frontmatter cover, --cover or --thumb")
		}
		if thumbID, err = up.Cover(e.ctx, a, src); err != nil {
			return fmt.Errorf("cover: %w", err)
		}
	}

	article := map[string]any{
		"article_type":          "news",
		"title":                 m.Title,
		"author":                m.Author,
		"digest":                m.Digest,
		"markdown":              a.Markdown,
		"skin":                  first(*skin, m.Skin, e.cfg.Skin),
		"thumb_media_id":        thumbID,
		"content_source_url":    m.SourceURL,
		"only_fans_can_comment": boolInt(m.FansComment),
	}
	if m.OpenComment != nil {
		article["need_open_comment"] = boolInt(*m.OpenComment)
	}

	id := first(*mediaID, m.MediaID)
	action := "updated"
	if id == "" {
		action = "created"
		if id, err = e.client.DraftAdd(e.ctx, article); err != nil {
			return err
		}
		if id == "" {
			return errors.New("draft add: empty media_id")
		}
	} else if err := e.client.DraftUpdate(e.ctx, id, *index, article); err != nil {
		return err
	}

	v := map[string]any{"action": action, "media_id": id, "title": m.Title, "thumb_media_id": thumbID}
	text := fmt.Sprintf("草稿已%s：%s\nmedia_id: %s", map[string]string{"created": "新建", "updated": "更新"}[action], m.Title, id)
	if *publish {
		pid, err := e.client.PublishSubmit(e.ctx, id)
		if err != nil {
			return fmt.Errorf("draft saved (%s) but publish failed: %w", id, err)
		}
		v["publish_id"] = pid
		text += "\n已提交发布，publish_id: " + pid + "\n用 wxctl status " + pid + " 查询结果"
	}
	return e.result(v, text)
}

// cmdDrafts 列出草稿，只取 media_id 和标题；多图文草稿的标题用「 | 」连接
func cmdDrafts(e *env, args []string) error {
	fs := newFlagSet(e, "drafts", "[--offset n] [--count n]")
	offset := fs.Int("offset", 0, "起始位置")
	count := fs.Int("count", 20, "数量，1-20")
	if _, err := parseArgs(fs, args, 0); err != nil {
		return err
	}
	if *offset < 0 || *count < 1 || *count > 20 {
		return usageError("offset must be >= 0, count must be 1-20")
	}
	var out struct {
		Total int `json:"total_count"`
		Item  []struct {
			MediaID string `json:"media_id"`
			Content struct {
				NewsItem []struct {
					Title string `json:"title"`
				} `json:"news_item"`
			} `json:"content"`
		} `json:"item"`
	}
	path := fmt.Sprintf("/api/v1/drafts?offset=%d&count=%d&no_content=true", *offset, *count)
	if err := e.client.getJSON(e.ctx, path, &out); err != nil {
		return err
	}

	type draft struct {
		MediaID string `json:"media_id"`
		Title   string `json:"title"`
	}
	drafts := make([]draft, 0, len(out.Item))
	var b strings.Builder
	for _, it := range out.Item {
		var titles []string
		for _, n := range it.Content.NewsItem {
			titles = append(titles, n.Title)
		}
		d := draft{MediaID: it.MediaID, Title: strings.Join(titles, " | ")}
		drafts = append(drafts, d)
		fmt.Fprintf(&b, "%s  %s\n", d.MediaID, d.Title)
	}
	fmt.Fprintf(&b, "共 %d 篇，本页 %d 篇", out.Total, len(drafts))
	return e.result(map[string]any{"total": out.Total, "drafts": drafts}, b.String())
}

func cmdPublish(e *env, args []string) error {
	fs := newFlagSet(e, "publish", "<media_id>")
	pos, err := parseArgs(fs, args, 1)
	if err != nil {
		return err
	}
	pid, err := e.client.PublishSubmit(e.ctx, pos[0])
	if err != nil {
		return err
	}
	return e.result(map[string]any{"media_id": pos[0], "publish_id": pid}, "已提交发布，publish_id: "+pid)
}

var publishStatus = map[int]string{0: "成功", 1: "发布中", 2: "原创失败", 3: "常规失败", 4: "平台审核不通过", 5: "成功后用户删除", 6: "成功后系统封禁"}

func cmdStatus(e *env, args []string) error {
	fs := newFlagSet(e, "status", "<publish_id>")
	pos, err := parseArgs(fs, args, 1)
	if err != nil {
		return err
	}
	var out struct {
		Status    int    `json:"publish_status"`
		ArticleID string `json:"article_id"`
		Detail    struct {
			Item []struct {
				URL string `json:"article_url"`
			} `json:"item"`
		} `json:"article_detail"`
	}
	if err := e.client.getJSON(e.ctx, "/api/v1/publish/"+url.PathEscape(pos[0]), &out); err != nil {
		return err
	}
	var urls []string
	for _, it := range out.Detail.Item {
		urls = append(urls, it.URL)
	}
	text := fmt.Sprintf("状态: %d %s", out.Status, publishStatus[out.Status])
	if out.ArticleID != "" {
		text += "\narticle_id: " + out.ArticleID
	}
	for _, u := range urls {
		text += "\n" + u
	}
	return e.result(map[string]any{
		"publish_id": pos[0], "publish_status": out.Status, "status_text": publishStatus[out.Status],
		"article_id": out.ArticleID, "urls": urls,
	}, text)
}

func cmdUpload(e *env, args []string) error {
	fs := newFlagSet(e, "upload", "<image> [--cover]")
	cover := fs.Bool("cover", false, "上传为永久图片素材（可作封面），返回 media_id")
	pos, err := parseArgs(fs, args, 1)
	if err != nil {
		return err
	}
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	a := &Article{Dir: wd}
	up := e.uploader()
	if *cover {
		id, err := up.Cover(e.ctx, a, pos[0])
		if err != nil {
			return err
		}
		return e.result(map[string]any{"media_id": id}, id)
	}
	u, err := up.ContentImage(e.ctx, a, pos[0])
	if err != nil {
		return err
	}
	return e.result(map[string]any{"url": strings.TrimSpace(u)}, u)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
