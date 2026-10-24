# wx-bot

把 Markdown 文章发到微信公众号的小工具。包含一个服务端 `wx-bot` 和一个命令行 `wxctl`。

## 能做什么

- 用 Markdown 写文章，套用主题渲染成公众号排版，一条命令建成草稿或直接发布
- 支持图片消息（newspic）：按平时习惯在正文引用图片、写文字，format 自动把图片移入头信息
- 正文图片、封面自动上传和压缩，同一张图不重复上传
- Web 面板：卡片式草稿箱，点击浮层直接查看手机尺寸预览，关闭后保留列表位置；支持发布、删除，素材库查看总数、上传、删除
- 多公众号（多租户）：每个公众号独立 token，secret 加密存储，图片互相隔离
- 作者、评论等常用设置写进配置文件，新文章自动带上

## 支持的平台

- 服务端：Linux（静态编译，无外部依赖），数据存在 SQLite
- wxctl：macOS、Linux（Windows 理论可用，未测试）
- 需要 Go 1.26 编译

## 快速开始

### 服务端

```bash
go generate ./internal/web          # 编译写作页用的 WASM 排版引擎，嵌进面板
go build -o wx-bot ./cmd/wx-bot

export WXBOT_MASTER_KEY=$(openssl rand -hex 32)   # 加密公众号 secret，务必备份
export WXBOT_ADMIN_TOKEN=$(openssl rand -hex 24)  # 管理接口用
export DATA_DIR=./data
export LISTEN_ADDR=127.0.0.1:1240
./wx-bot
```

通过管理接口登记公众号，返回该公众号的访问 token：

```bash
curl -X POST http://127.0.0.1:1240/api/v1/admin/tenants \
  -H "Authorization: Bearer $WXBOT_ADMIN_TOKEN" \
  -d '{"name":"myblog","appid":"wx...","secret":"...","token_note":"laptop"}'
```

公众号后台需要把服务器 IP 加入白名单。浏览器打开服务地址，用 token 登录即可使用 Web 面板。

### wxctl

```bash
go build -o ~/.local/bin/wxctl ./cmd/wxctl

wxctl login                       # 粘贴访问 token
wxctl config set author 你的名字   # 设置默认作者
wxctl config set theme ~/my.css   # 可选：用本地 CSS 主题（每次提交实时上传，服务端不保存）

wxctl new 我的文章                 # 生成 我的文章.md 模板
wxctl format 我的文章.md           # 整理 frontmatter 并做提交前检查，已有的 md 也适用
wxctl draft 我的文章.md            # 上传图片并建草稿，media_id 自动写回原文件
wxctl draft 我的文章.md            # 修改后再次执行，更新同一篇草稿
wxctl publish <media_id>          # 发布（粉丝可见，不可撤回）
```

更多命令和参数：`wxctl help`，给脚本或 agent 调用：`wxctl help agent`。

### 图片消息

默认类型仍是图文 `news`。图片消息可以直接写普通 Markdown，图片支持绝对路径和相对文章目录的路径：

```markdown
# 周末摄影记录

今天拍下的几个瞬间。

![街景](/Users/me/Pictures/01.jpg)

这张是在傍晚拍的。

![落日](images/02.jpg)
```

```bash
wxctl new 摄影记录 --type newspic # 可选：先创建图片消息模板
wxctl format 摄影记录.md --type newspic
wxctl draft 摄影记录.md           # 之后按文件中的 article_type 创建或更新
wxctl publish <media_id>          # 提交发布，再用 status 查询最终结果
```

`format` 按出现顺序把正文图片移到 `images`，留下文字并整理空行：

```yaml
---
article_type: newspic
title: 周末摄影记录
images:
  - /Users/me/Pictures/01.jpg
  - images/02.jpg
open_comment: true
fans_comment: false
---

今天拍下的几个瞬间。

这张是在傍晚拍的。
```

- 类型优先级：`--type` > frontmatter `article_type` > `news`。再次 format 无需重复指定类型。
- `images` 每项统一为字符串：本地绝对/相对路径、`http(s)` URL，或 `media_id:永久素材ID`。含空格的正文图片路径用 `![说明](</完整路径/my photo.jpg>)`。
- 图片数量为 1～20 张，第一张即封面。已有列表保留顺序，新引用的图片追加；重复 format 不重复追加，代码示例中的图片不提取。
- 图片上传为永久素材，按公众号凭证和文件内容缓存；微信图床 URL 也需要取得永久素材 ID。
- 正文按纯文本提交，保留换行，Markdown 标记不排版；图片消息不使用 CSS 主题、独立封面或 `render`。
- `format -o 其他目录/post.md` 会调整图片相对路径，绝对路径保持原样。
- 更新前校验远端草稿类型；需要切换类型时新建草稿。发布是否成功以 `wxctl status <publish_id>` 的最终结果为准。
