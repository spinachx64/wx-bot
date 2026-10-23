# wx-bot

把 Markdown 文章发到微信公众号的小工具。包含一个服务端 `wx-bot` 和一个命令行 `wxctl`。

## 能做什么

- 用 Markdown 写文章，套用主题渲染成公众号排版，一条命令建成草稿或直接发布
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
