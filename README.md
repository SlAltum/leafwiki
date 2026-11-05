# LeafWiki

[English](README.en.md) | 中文

自托管 wiki。Markdown 直接落在磁盘上，旁边是 SQLite 索引，交付物是一个 Go 二进制。

本仓库是 [perber/leafwiki](https://github.com/perber/leafwiki) 的 fork（派生仓库），基于提交 `714e0368`。上游拥有这个项目。本 fork 增加了尚未进入上游的工作，列在[本 fork 新增的内容](#本-fork-新增的内容)；其余部分保持不变。

> **尚无 release（发行版）。**本 fork 不发布二进制，也不发布容器镜像。请按下文[安装](#安装)自行构建。上游的 release、`ghcr.io/perber/leafwiki` 镜像、`install.sh` 与 `update.sh` 都是上游构建，不含本页描述的改动。

开发文档（`docs/development/`）与安装文档（`docs/install/`）目前为英文。

---

## 目录

- [本 fork 新增的内容](#本-fork-新增的内容)
- [安装](#安装)
  - [从源码构建](#从源码构建)
  - [Docker](#docker)
  - [Windows 桌面版](#windows-桌面版)
  - [管理命令](#管理命令)
- [检索](#检索)
  - [关键词检索](#关键词检索)
  - [语义召回](#语义召回)
- [Agent 访问](#agent-访问)
- [配置](#配置)
  - [必填](#必填)
  - [命令行参数](#命令行参数)
  - [环境变量](#环境变量)
  - [语义检索设置](#语义检索设置)
- [运行模式](#运行模式)
- [磁盘文件与重新同步](#磁盘文件与重新同步)
- [安全](#安全)
- [开发](#开发)
- [上游、许可与贡献](#上游许可与贡献)

---

## 本 fork 新增的内容

| 方面 | 内容 | 详见 |
|---|---|---|
| 检索 | 中日韩文本可按词检索，不再只匹配前缀。全角 ASCII 折叠为半角。拼错的拉丁词会得到可点击的改写建议。API（程序接口）还会返回命中位置的章节标题与锚点。frontmatter（页面元数据）里声明的别名参与检索。 | [关键词检索](#关键词检索) |
| 语义召回 | 可选的混合检索：页面在进程内向量化，与关键词结果融合，用不同措辞写的页面也能找到。为 `bge-small-zh-v1.5`（中文）构建，也支持任何同格式 manifest（模型描述文件）的模型。 | [语义召回](#语义召回) |
| 桌面版 | 带窗口的 Windows 构建：地址、初始密码、日志、覆盖全部参数的设置页，以及 AI 助手提示词（`AGENT.md` 写在 exe 旁）。 | [Windows 桌面版](#windows-桌面版) |
| Agent 访问 | 同一个二进制就是 CLI（命令行工具）：`status`、`page get\|write\|apply\|delete`、`check`、`vectors build\|sync\|status`。它通过本地 socket（套接字）与运行中的服务通信，服务停止时以离线方式写入。 | [Agent 访问](#agent-访问) |

其余功能（树导航、编辑器、标签、属性、修订历史、用户管理、快照、Git 备份、SMTP、反向代理认证）是上游代码，未改动。

**随之而来的行为变化。**以下几处与上游构建不同：

- 同一数据目录上的第二个服务不再启动。运行中的服务在整个生命周期持有 `<data>/wiki.lock`。
- 服务运行时，`vectors build`、`vectors sync`、`reset-admin-password` 与 `restore-snapshot` 退出码为 `75`。
- 可执行文件旁存在 `data/` 时，`--data-dir` 优先使用它。
- Docker 镜像把 `/app/data` 建成 `0755`（原来是 `0777`）：控制通道的信任边界是数据目录的属主。
- 没有 `linux/386` 构建：编码器依赖无法为 32 位编译。

---

## 安装

### 从源码构建

需要 Go（版本见 `go.mod` 的 `toolchain` 行）、Node.js（见 `.nvmrc`）和 GNU Make。

```bash
git clone https://github.com/SlAltum/leafwiki.git
cd leafwiki
make build        # 安装前端依赖、构建 SPA 并嵌入，产出 ./leafwiki
./leafwiki --jwt-secret=yoursecret --admin-password=yourpassword --allow-insecure=true
```

明文 HTTP 必须加 `--allow-insecure=true`，否则服务端拒绝签发登录 cookie。用 HTTPS 且反向代理转发 `X-Forwarded-Proto: https` 时不要加。

服务默认监听 `127.0.0.1:8080`。加 `--host=0.0.0.0` 可对外暴露。

**数据目录。**`--data-dir` 默认是 `./data`；如果可执行文件旁已存在 `data/` 目录，则用那个目录。第二条规则让整个文件夹可以放在任意位置运行：可执行文件、凭据与 `data/` 一起搬走即可。

**其他平台的发布构建**需要 Docker：`make release` 构建 linux-amd64、linux-arm64、windows-amd64、darwin-amd64 与 darwin-arm64，并把校验和写入 `releases/`。这些构建使用 `CGO_ENABLED=0` 和默认构建标签，因此不含语义召回（见[语义召回](#语义召回)）。

### Docker

从本仓库构建镜像：

```bash
docker build -f Dockerfile -t leafwiki .
docker run -p 8080:8080 \
  -v ~/leafwiki-data:/app/data \
  leafwiki \
  --jwt-secret=yoursecret --admin-password=yourpassword --allow-insecure=true
```

- 镜像不带 `vectors` 构建标签，只接受 `--vectors=off`。
- `/app/data` 以 `0755` 创建。控制通道的信任边界是“与数据目录同一 uid”，因此挂载的卷必须对运行容器的 uid 可写。
- 在容器内运行 Agent CLI（`docker compose exec leafwiki /app/leafwiki ...`）：socket 与锁都在容器的数据目录里。

上游镜像 `ghcr.io/perber/leafwiki:latest` 是上游的构建，不含本页描述的改动。

### Windows 桌面版

```bash
make desktop-windows            # 窗口版，仅关键词检索
make desktop-windows-vectors VECTORS_ORT_DLL=D:\path\to\onnxruntime.dll
```

两者都产出 `releases/LeafWiki/leafwiki.exe`，前端已嵌入。第二个目标需要 C 工具链（Windows 上用 MinGW）、`onnxruntime.dll`（1.31.x，win-x64）以及 `assets/tokenizer-bge/` 里的模型；它会把 DLL 和 `models/<id>/` 复制到 exe 旁边。

整个文件夹一起使用。exe 会找自己旁边的 `data/`，首次启动在那里生成 `jwt.secret` 与 `admin.password`。首次启动时窗口会在浏览器中打开一次百科，之后不再自动打开。

窗口界面为中文，分两页：“基本”（地址、密码、AI 助手提示词、启停、日志）与“高级设置”（每个服务端参数一行，先用“测试解析”再“保存”）。数据目录、凭据，以及启动器自己决定的参数（`--host`、`--unix-socket`、`--log-format`、`--allow-insecure`、公开访问、认证）由启动器管理，显示为只读。构建命令行时启动器会丢弃空值，所以无法在设置页关闭控制通道；要关闭请自己用 `--control-socket=` 启动服务端。

`desktop-windows-vectors` 构建默认 `--vectors=auto`；不带编码器的构建强制 `--vectors=off`。

布局、排障与发布自检见 [docs/install/desktop.md](docs/install/desktop.md)。

### 管理命令

两个命令都会取 `<data>/wiki.lock`，因此在该数据目录上有服务运行时退出码为 `75`。先停止服务。

```bash
./leafwiki --data-dir ./data reset-admin-password
./leafwiki --data-dir ./data restore-snapshot ./data/snapshots/leafwiki-snapshot-2026-01-01.zip
```

`reset-admin-password` 生成新的随机密码并打印出来，是忘记旧密码时的入口。`restore-snapshot` 用快照 ZIP 替换数据目录，用于灾难恢复或初始化新实例；运行中的服务也可以从管理界面恢复（设置 → 完整备份），恢复期间会拦截写入。恢复会丢弃语义索引，并在恢复后的页面上重建。

---

## 检索

### 关键词检索

关键词索引始终开启，无需配置。

- **CJK（中日韩文字）按词索引。**连续的中日韩文字（汉字、平假名、片假名、谚文）按相邻两字切分入索引（单字段落按单字入索引）。查询走同一套分析，所以 `部署`、`回滚` 这类词在中文句子中间也能命中，不只在开头。索引与查询共用同一实现。
- **宽度折叠。**全角 ASCII（`ＡＢＣ`）折叠为半角，半角片假名（`ｶﾀｶﾅ`）折叠为全角，两种写法都能命中。
- **短查询也能检索。**CJK 查询最少 2 个字符，拉丁查询最少 3 个；输入框、URL、API 调用与滚动行为共用同一个判定。
- **拉丁词拼写自动纠正。**语料里不存在的查询词按编辑距离 1、再按 2 生成候选，查询同时匹配原词与纠正词，并保留原词。界面把纠正后的查询作为可点击建议给出。CJK 查询不做纠正。
- **打开结果落在命中处。**结果会带着查询在 URL（网址）中打开页面，正文里高亮第一个命中并滚动到视野内。API 还会返回命中位置的章节标题与锚点（`section`、`anchor`），供需要更深链接的客户端使用。
- **别名可检索。**在 frontmatter 中声明 `leafwiki_aliases`（字符串或列表）后，用该名字检索能命中页面；编辑器里有别名字段。由别名命中的结果返回 `matched: "alias"` 与别名原文。
- **标签过滤结果**，标签分面给出每个标签的计数。

标题、摘要与高亮都取自页面原文，而不是索引：索引存的是分析后的文本，适合匹配，不适合阅读。

### 语义召回

语义召回需要显式开启，默认关闭。开启后，页面引言与每个标题小节在进程内做向量化，存入 `<data>/vectors.db`。检索同时使用两套索引，并用倒数排名融合（RRF，k=60）合并，因此与查询没有共同用词的页面也可能排在前面。

```bash
./leafwiki --vectors=auto \
  --vectors-model-dir=./data/models \
  --vectors-library=/path/to/libonnxruntime.so \
  --jwt-secret=yoursecret --admin-password=yourpassword
```

| 模式 | 行为 |
|---|---|
| `off`（默认） | 不加载模型，不创建 `vectors.db`，检索只走关键词。 |
| `auto` | 保存页面后入队，后台完成向量化。 |
| `manual` | 不入队，只有显式重建时索引才变化。 |

**依赖什么：**一个带 `manifest.json` 的模型目录（模型 id、维度、池化方式、查询前缀、文件校验和）以及 ONNX Runtime 共享库。manifest 固定了校验和，因此服务端会拒绝加载改动过或复制不完整的模型，也不会写出错误向量。缺模型或缺库不会让服务起不来：日志出现 `semantic search unavailable`，检索退回关键词。

**哪些构建有它：**编码器是 cgo 绑定，位于 `vectors` 构建标签之后。发布二进制与 Docker 镜像都不带它，只接受 `--vectors=off`。要用语义召回：

- 使用带语义检索的 Windows 桌面构建（`make desktop-windows-vectors`），或
- 用 `CGO_ENABLED=1 -tags vectors` 构建，并提供模型与推理库。

默认模型是 `bge-small-zh-v1.5`（512 维，中文）。模型与 DLL 的获取方式、`manifest.json` 的校验规则见 [docs/development/vectors-assets.md](docs/development/vectors-assets.md)。

**管理界面：**设置 → 语义索引页显示索引状态、模型、排队与失败数量、最近错误、自动索引的暂停开关，以及重建与清空重建。页面菜单（仅管理员）有“语义索引”入口，显示当前页面的索引情况，并可只重建这一页。

**命令行：**`leafwiki --data-dir ./data vectors status`、`vectors build [--full]`、`vectors sync [--dry-run] [--paths a,b]`。`build` 与 `sync` 会取数据目录锁，服务运行期间退出码为 `75`。

**状态：**索引状态为 `off`、`missing`、`queued`、`running`、`stale`、`failed` 或 `fresh`；`paused` 是独立开关。查询返回的 `semantic` 为 `off`、`ok`、`stale` 或 `unavailable`；索引为 stale 或 unavailable 时不会编造结果，由关键词路径作答。

---

## Agent 访问

LeafWiki 通过数据目录里的**控制 socket** 与本地 agent（智能体）通信，客户端就是同一个二进制。它面向同机的编程助手或脚本，不是另一个远程 API。设计说明见
[Agent control channel](docs/development/architecture.md#agent-control-channel)。

```bash
leafwiki --data-dir ./data status                       # 协议、页面数、向量状态
leafwiki --data-dir ./data page get platform/deploy     # 路径、id、标题、版本、标签、正文
leafwiki --data-dir ./data page write platform/deploy --title "Deploy" --content-file - < deploy.md
leafwiki --data-dir ./data page apply --batch changes.jsonl --json
leafwiki --data-dir ./data page delete platform/deploy --dry-run
leafwiki --data-dir ./data check --json
```

- **读、新建、修改对未认证的调用方开放**，信任级别等同于直接改磁盘上的 Markdown。**删除需要编辑者或管理员**：用 `--user` 配 `--password`、`--password-file`，或 `LEAFWIKI_AGENT_USER` 与 `LEAFWIKI_AGENT_PASSWORD`。
- `--control-auth=required` 关闭除 `status` 与 `auth.login` 之外的一切。
- `--control-socket=""` 关闭通道。socket 绑定失败（Windows 上路径过长）只记警告，服务继续运行。
- **每个数据目录只有一个写者。**服务在生命周期内持有 `<data>/wiki.lock`，第二个实例会拒绝启动，而不是半共享目录。没有服务运行时，写命令以离线方式工作（见下文）。
- `--json` 在成功和部分批次时精确输出一个 JSON 对象。失败命令的 stdout（标准输出）为空：错误写到 stderr（标准错误），结果由退出码表达。
- 批次在预检阶段是原子的。任一坏条目会拒绝整个批次且不写盘；执行阶段失败会停止批次，并报告已写入的条目。
- `--wait`（默认 30 秒）等待刚写入页面的语义索引。`off`、`manual`、`paused` 模式下它什么都不做。
- `check` 只读且不取锁。它报告树结构问题，例如非法文件名、重名、重复 id、无法解析的 frontmatter。

**离线写入：**没有服务运行时，`page write`、`page apply`、`page delete` 取 `<data>/wiki.lock`，复用服务端自己的用例，不记录修订。`page get`、`status` 与 `--dry-run` 需要运行中的服务。如果 `<data>/vectors.db` 与 `<data>/models` 都存在，改动过的页面会重新向量化；否则命令给出警告，之后需要运行 `vectors sync`。

**退出码：**`0` 成功，`1` 未分类，`2` 用法，`3` 未找到，`4` 版本冲突，`5` 无权限，`6` 连不上、超时或正在恢复，`7` 能力缺失，`8` 部分成功，`75` 数据目录被占用。

在容器里请在容器内运行 CLI：socket 与锁都在容器的数据目录中。

每条命令、参数与 JSON 形状见 [docs/development/skills/leafwiki/SKILL.md](docs/development/skills/leafwiki/SKILL.md)；桌面启动器会把同样的提示写进 exe 旁的 `AGENT.md`。

---

## 配置

每个参数都有对应的环境变量，列在参数旁边。命令行参数优先于环境变量，环境变量优先于默认值。设置成空值的环境变量按未设置处理。

### 必填

| 参数 | 说明 |
|---|---|
| `--jwt-secret` | 签发 JWT 的密钥。妥善保管。 |
| `--admin-password` | 初始管理员密码。仅在没有管理员时生效。 |

明文 HTTP 请加 `--allow-insecure=true`，登录与 CSRF cookie 才能工作。

### 命令行参数

标为*本 fork*的行只存在于本仓库。

| 参数 | 说明 | 默认值 | 来源 |
|---|---|---|---|
| `--host` | 服务绑定的主机/IP | `127.0.0.1` | 上游 |
| `--port` | 监听端口 | `8080` | 上游 |
| `--unix-socket` | Unix domain socket 路径；覆盖 `--host` 与 `--port` | `""` | v0.11.3 |
| `--data-dir` | 数据存放目录 | exe 旁存在 `data/` 时用它，否则 `./data` | 上游 |
| `--admin-username` | 初始管理员用户名 | `admin` | v0.12.0 |
| `--admin-email` | 初始管理员邮箱 | `admin@localhost` | v0.12.0 |
| `--public-access` | 允许公开只读访问 | `false` | 上游 |
| `--base-path` | 反向代理下的 URL 前缀（例如 `/wiki`） | `""` | v0.8.2 |
| `--public-url` | 本 wiki 的公开基础 URL；设置 `--smtp-host` 时必填 | `""` | v0.13.0 |
| `--allow-insecure` | 允许 HTTP 认证 cookie（明文 HTTP 必填） | `false` | v0.7.0 |
| `--disable-auth` | 关闭全部认证（仅可信网络） | `false` | v0.7.0 |
| `--access-token-timeout` | 访问令牌有效期（例如 `24h`） | `15m` | v0.7.0 |
| `--refresh-token-timeout` | 刷新令牌有效期（例如 `168h`） | `168h` | v0.7.0 |
| `--max-asset-upload-size` | 单次上传上限（例如 `50MiB`） | `50MiB` | v0.8.5 |
| `--custom-stylesheet` | 数据目录内 `.css` 文件的路径 | `""` | v0.8.5 |
| `--default-language` | 默认界面语言（`en`、`de`、`es`、`fr`、`zh`） | `""` | v0.13.0 |
| `--inject-code-in-header` | 注入 `<head>` 的原始 HTML/JS | `""` | v0.6.0 |
| `--hide-link-metadata-section` | 隐藏反向链接与链接状态面板 | `false` | 上游 |
| `--enable-revision` | 启用修订历史 | `false` | v0.9.0 |
| `--enable-link-refactor` | 重命名与移动时改写链接 | `false` | v0.9.0 |
| `--enable-api-key-management` | 实验性 API 密钥管理 | `false` | 上游 |
| `--enable-share-links` | 实验性分享链接（尚不可用） | `false` | 上游 |
| `--editor-limit` | 管理员与编辑者账号上限；`0` 表示不限 | `0` | 上游 |
| `--max-revision-history` | 每页最大修订数；`0` 表示不限 | `100` | v0.9.0 |
| `--revision-coalesce-window` | 合并同一作者连续自动保存的时间窗；`0` 表示关闭 | `5m` | v0.11.0 |
| `--enable-http-remote-user` | 通过 HTTP 头做反向代理认证 | `false` | v0.10.0 |
| `--http-remote-user-header-name` | 代理传递用户名或邮箱的头 | `Remote-User` | v0.10.0 |
| `--enable-http-remote-user-auto-create` | 自动创建代理声明的用户 | `false` | v0.12.1 |
| `--http-remote-user-email-header-name` | 自动创建用户所用邮箱的头 | `""` | v0.12.1 |
| `--http-remote-user-default-role` | 自动创建用户的角色；不能是 `admin` | `viewer` | v0.12.1 |
| `--trusted-proxy-ips` | 信任的代理 IP/CIDR | `""` | v0.10.0 |
| `--login-url` | 外部登录地址，替代内置表单 | `""` | v0.12.0 |
| `--logout-url` | 退出后的外部跳转地址 | `""` | v0.12.0 |
| `--http-remote-user-logout-url` | 已废弃，改用 `--logout-url` | `""` | v0.10.0 |
| `--user-management-url` | 外部用户管理页面，替代内置页面 | `""` | v0.11.4 |
| `--disable-request-log` | 关闭每请求的访问日志 | `false` | v0.10.1 |
| `--log-format` | 日志格式：`text` 或 `json` | `text` | v0.12.0 |
| `--totp-encryption-key` | 静态加密 TOTP 密钥的密钥（至少 32 字节） | `""` | v0.12.0 |
| `--enable-metrics` | 独立监听器上的 Prometheus `/metrics` | `false` | v0.12.0 |
| `--metrics-host` | 指标监听主机/IP | `127.0.0.1` | v0.12.0 |
| `--metrics-port` | 指标监听端口 | `9091` | v0.12.0 |
| `--snapshot` | 启用快照 ZIP（含 SQLite 数据库） | `true` | v0.12.0 |
| `--snapshot-interval` | 快照间隔；`0` 表示仅手动 | `24h` | v0.12.0 |
| `--snapshot-retention` | 保留最近 N 份快照；`<= 0` 表示全保留 | `10` | v0.12.0 |
| `--snapshot-dir` | 快照 ZIP 目录 | `<data-dir>/snapshots` | v0.12.0 |
| `--restore-upload-max-size` | 上传恢复 ZIP 的大小上限 | `500MiB` | v0.12.0 |
| `--git-backup` | 启用 Git 备份到远端仓库 | `false` | v0.11.3 |
| `--git-backup-remote` | Git 备份远端 URL | `""` | v0.11.3 |
| `--git-backup-branch` | 推送目标分支 | `main` | v0.11.3 |
| `--git-backup-path` | 存放 `root/` 与 `assets/` 的目录；空值表示顶层 | `""` | v0.13.0 |
| `--git-backup-ssh-key` | 原始 SSH 私钥（优先用环境变量） | `""` | v0.11.3 |
| `--git-backup-ssh-key-path` | SSH 私钥文件路径 | `""` | v0.11.3 |
| `--git-backup-ssh-known-hosts` | 用于防 MITM 的 `known_hosts` 路径 | `""` | v0.11.3 |
| `--git-backup-http-username` | HTTP(S) basic auth 用户名 | `""` | v0.13.0 |
| `--git-backup-http-password` | HTTP(S) basic auth 密码或令牌 | `""` | v0.13.0 |
| `--git-backup-author-name` | 提交作者名 | `LeafWiki Backup` | v0.11.3 |
| `--git-backup-author-email` | 提交作者邮箱 | `backup@leafwiki.local` | v0.11.3 |
| `--git-backup-interval` | 备份间隔；`0` 表示仅手动 | `60m` | v0.11.3 |
| `--smtp-host` | 用于重置密码与邀请邮件的 SMTP 主机；不设置则关闭邮件 | `""` | v0.13.0 |
| `--smtp-port` | SMTP 端口 | `587` | v0.13.0 |
| `--smtp-username` | SMTP 认证用户名 | `""` | v0.13.0 |
| `--smtp-password` | SMTP 认证密码 | `""` | v0.13.0 |
| `--smtp-from` | 发件地址（设置 `--smtp-host` 时必填） | `""` | v0.13.0 |
| `--smtp-from-name` | 发件显示名 | `LeafWiki` | v0.13.0 |
| `--smtp-security` | 传输安全：`none`、`starttls` 或 `tls` | `starttls` | v0.13.0 |
| `--smtp-insecure-skip-verify` | 跳过 TLS 证书校验（仅测试） | `false` | v0.13.0 |
| `--smtp-timeout` | 单次 SMTP 发送超时 | `10s` | v0.13.0 |
| `--vectors` | 语义检索：`off`、`auto` 或 `manual` | `off` | *本 fork* |
| `--vectors-model-dir` | 编码器模型目录 | `<data-dir>/models` | *本 fork* |
| `--vectors-library` | ONNX Runtime 共享库路径 | `""` | *本 fork* |
| `--control-socket` | Agent socket；空值关闭通道 | `<data-dir>/control.sock` | *本 fork* |
| `--control-auth` | 匿名 agent 写入策略：`optional` 或 `required` | `optional` | *本 fork* |

> 既没有 `--host` 也没有 `LEAFWIKI_HOST` 时，Docker 入口脚本会设置 `LEAFWIKI_HOST=0.0.0.0`。

### 环境变量

| 变量 | 说明 | 默认值 | 来源 |
|---|---|---|---|
| `LEAFWIKI_HOST` | 主机/IP 地址 | `127.0.0.1` | 上游 |
| `LEAFWIKI_PORT` | 端口 | `8080` | 上游 |
| `LEAFWIKI_UNIX_SOCKET` | Unix socket 路径；覆盖主机与端口 | `""` | v0.11.3 |
| `LEAFWIKI_DATA_DIR` | 数据目录路径 | exe 旁存在 `data/` 时用它，否则 `./data` | 上游 |
| `LEAFWIKI_ADMIN_PASSWORD` | 初始管理员密码（必填） | 无 | 上游 |
| `LEAFWIKI_ADMIN_USERNAME` | 初始管理员用户名 | `admin` | v0.12.0 |
| `LEAFWIKI_ADMIN_EMAIL` | 初始管理员邮箱 | `admin@localhost` | v0.12.0 |
| `LEAFWIKI_JWT_SECRET` | JWT 签名密钥（必填） | 无 | 上游 |
| `LEAFWIKI_PUBLIC_ACCESS` | 允许公开只读访问 | `false` | 上游 |
| `LEAFWIKI_BASE_PATH` | 反向代理的 URL 前缀 | `""` | v0.8.2 |
| `LEAFWIKI_PUBLIC_URL` | 公开基础 URL；设置 `LEAFWIKI_SMTP_HOST` 时必填 | `""` | v0.13.0 |
| `LEAFWIKI_ALLOW_INSECURE` | 允许 HTTP 认证 cookie | `false` | v0.7.0 |
| `LEAFWIKI_DISABLE_AUTH` | 关闭认证 | `false` | v0.7.0 |
| `LEAFWIKI_ACCESS_TOKEN_TIMEOUT` | 访问令牌有效期 | `15m` | v0.7.0 |
| `LEAFWIKI_REFRESH_TOKEN_TIMEOUT` | 刷新令牌有效期 | `168h` | v0.7.0 |
| `LEAFWIKI_MAX_ASSET_UPLOAD_SIZE` | 上传大小上限 | `50MiB` | v0.8.5 |
| `LEAFWIKI_CUSTOM_STYLESHEET` | 数据目录内 `.css` 文件的路径 | `""` | v0.8.5 |
| `LEAFWIKI_DEFAULT_LANGUAGE` | 默认界面语言 | `""` | v0.13.0 |
| `LEAFWIKI_INJECT_CODE_IN_HEADER` | 注入 `<head>` 的 HTML/JS | `""` | v0.6.0 |
| `LEAFWIKI_HIDE_LINK_METADATA_SECTION` | 隐藏反向链接面板 | `false` | 上游 |
| `LEAFWIKI_ENABLE_REVISION` | 修订历史 | `false` | v0.9.0 |
| `LEAFWIKI_ENABLE_LINK_REFACTOR` | 重命名与移动时改写链接 | `false` | v0.9.0 |
| `LEAFWIKI_ENABLE_API_KEY_MANAGEMENT` | 实验性 API 密钥管理 | `false` | 上游 |
| `LEAFWIKI_ENABLE_SHARE_LINKS` | 实验性分享链接（尚不可用） | `false` | 上游 |
| `LEAFWIKI_EDITOR_LIMIT` | 管理员与编辑者账号上限；`0` 表示不限 | `0` | 上游 |
| `LEAFWIKI_MAX_REVISION_HISTORY` | 每页最大修订数 | `100` | v0.9.0 |
| `LEAFWIKI_REVISION_COALESCE_WINDOW` | 合并连续自动保存；`0` 表示关闭 | `5m` | v0.11.0 |
| `LEAFWIKI_ENABLE_HTTP_REMOTE_USER` | 反向代理认证 | `false` | v0.10.0 |
| `LEAFWIKI_HTTP_REMOTE_USER_HEADER_NAME` | 携带代理用户的头 | `Remote-User` | v0.10.0 |
| `LEAFWIKI_ENABLE_HTTP_REMOTE_USER_AUTO_CREATE` | 自动创建代理用户 | `false` | v0.12.1 |
| `LEAFWIKI_HTTP_REMOTE_USER_EMAIL_HEADER_NAME` | 自动创建用户的邮箱头 | `""` | v0.12.1 |
| `LEAFWIKI_HTTP_REMOTE_USER_DEFAULT_ROLE` | 自动创建用户的角色 | `viewer` | v0.12.1 |
| `LEAFWIKI_TRUSTED_PROXY_IPS` | 信任的代理 IP/CIDR | `""` | v0.10.0 |
| `LEAFWIKI_LOGIN_URL` | 外部登录地址 | `""` | v0.12.0 |
| `LEAFWIKI_LOGOUT_URL` | 外部退出跳转 | `""` | v0.12.0 |
| `LEAFWIKI_HTTP_REMOTE_USER_LOGOUT_URL` | 已废弃，改用 `LEAFWIKI_LOGOUT_URL` | `""` | v0.10.0 |
| `LEAFWIKI_USER_MANAGEMENT_URL` | 外部用户管理页面 | `""` | v0.11.4 |
| `LEAFWIKI_DISABLE_REQUEST_LOG` | 关闭每请求日志行 | `false` | v0.10.1 |
| `LEAFWIKI_LOG_FORMAT` | 日志格式：`text` 或 `json` | `text` | v0.12.0 |
| `LEAFWIKI_LOG_LEVEL` | `debug`、`info`、`warn` 或 `error`（仅环境变量） | `info` | v0.8.0 |
| `LEAFWIKI_TOTP_ENCRYPTION_KEY` | 静态加密 TOTP 密钥的密钥 | `""` | v0.12.0 |
| `LEAFWIKI_ENABLE_METRICS` | Prometheus `/metrics` | `false` | v0.12.0 |
| `LEAFWIKI_METRICS_HOST` | 指标监听主机 | `127.0.0.1` | v0.12.0 |
| `LEAFWIKI_METRICS_PORT` | 指标监听端口 | `9091` | v0.12.0 |
| `LEAFWIKI_SNAPSHOT` | 启用快照 ZIP | `true` | v0.12.0 |
| `LEAFWIKI_SNAPSHOT_INTERVAL` | 快照间隔；`0` 表示仅手动 | `24h` | v0.12.0 |
| `LEAFWIKI_SNAPSHOT_RETENTION` | 保留的快照数；`<= 0` 表示全保留 | `10` | v0.12.0 |
| `LEAFWIKI_SNAPSHOT_DIR` | 快照目录 | `<data-dir>/snapshots` | v0.12.0 |
| `LEAFWIKI_RESTORE_UPLOAD_MAX_SIZE` | 上传恢复 ZIP 的大小上限 | `500MiB` | v0.12.0 |
| `LEAFWIKI_GIT_BACKUP` | 启用 Git 备份 | `false` | v0.11.3 |
| `LEAFWIKI_GIT_BACKUP_REMOTE` | 远端 URL | `""` | v0.11.3 |
| `LEAFWIKI_GIT_BACKUP_BRANCH` | 推送目标分支 | `main` | v0.11.3 |
| `LEAFWIKI_GIT_BACKUP_PATH` | 存放 `root/` 与 `assets/` 的目录 | `""` | v0.13.0 |
| `LEAFWIKI_GIT_BACKUP_SSH_KEY` | 原始 SSH 私钥 | `""` | v0.11.3 |
| `LEAFWIKI_GIT_BACKUP_SSH_KEY_PATH` | SSH 私钥文件 | `""` | v0.11.3 |
| `LEAFWIKI_GIT_BACKUP_SSH_KNOWN_HOSTS` | `known_hosts` 文件 | `""` | v0.11.3 |
| `LEAFWIKI_GIT_BACKUP_HTTP_USERNAME` | HTTP(S) basic auth 用户名 | `""` | v0.13.0 |
| `LEAFWIKI_GIT_BACKUP_HTTP_PASSWORD` | HTTP(S) basic auth 密码或令牌 | `""` | v0.13.0 |
| `LEAFWIKI_GIT_BACKUP_AUTHOR_NAME` | 提交作者名 | `LeafWiki Backup` | v0.11.3 |
| `LEAFWIKI_GIT_BACKUP_AUTHOR_EMAIL` | 提交作者邮箱 | `backup@leafwiki.local` | v0.11.3 |
| `LEAFWIKI_GIT_BACKUP_INTERVAL` | 备份间隔；`0` 表示仅手动 | `60m` | v0.11.3 |
| `LEAFWIKI_SMTP_HOST` | SMTP 主机；不设置则关闭邮件 | `""` | v0.13.0 |
| `LEAFWIKI_SMTP_PORT` | SMTP 端口 | `587` | v0.13.0 |
| `LEAFWIKI_SMTP_USERNAME` | SMTP 认证用户名 | `""` | v0.13.0 |
| `LEAFWIKI_SMTP_PASSWORD` | SMTP 认证密码 | `""` | v0.13.0 |
| `LEAFWIKI_SMTP_FROM` | 发件地址（设置 `LEAFWIKI_SMTP_HOST` 时必填） | `""` | v0.13.0 |
| `LEAFWIKI_SMTP_FROM_NAME` | 发件显示名 | `LeafWiki` | v0.13.0 |
| `LEAFWIKI_SMTP_SECURITY` | `none`、`starttls` 或 `tls` | `starttls` | v0.13.0 |
| `LEAFWIKI_SMTP_INSECURE_SKIP_VERIFY` | 跳过 TLS 校验 | `false` | v0.13.0 |
| `LEAFWIKI_SMTP_TIMEOUT` | 单次 SMTP 发送超时 | `10s` | v0.13.0 |
| `LEAFWIKI_VECTORS` | 语义检索模式 | `off` | *本 fork* |
| `LEAFWIKI_VECTORS_MODEL_DIR` | 编码器模型目录 | `<data-dir>/models` | *本 fork* |
| `LEAFWIKI_VECTORS_LIBRARY` | ONNX Runtime 共享库 | `""` | *本 fork* |
| `LEAFWIKI_CONTROL_SOCKET` | Agent socket；空值关闭通道 | `<data-dir>/control.sock` | *本 fork* |
| `LEAFWIKI_CONTROL_AUTH` | `optional` 或 `required` | `optional` | *本 fork* |
| `LEAFWIKI_AGENT_USER` | Agent CLI 认证账号（由 CLI 读取，服务端不用） | `""` | *本 fork* |
| `LEAFWIKI_AGENT_PASSWORD` | `LEAFWIKI_AGENT_USER` 的密码 | `""` | *本 fork* |

### 语义检索设置

- `--vectors=auto` 在页面保存后于后台向量化。保存响应返回 `vector: "queued"`；Agent CLI 的 `--wait` 会轮询到页面为 `indexed`。
- `--vectors=manual` 从不入队。用管理界面或 CLI 构建、刷新。
- `--vectors-model-dir` 可以指向单个模型目录，或只含一个模型的父目录。父目录下有多个模型会以歧义报错。
- `--vectors-library` 是 `onnxruntime.dll`（Windows）或 `libonnxruntime.so` 的路径。Go 绑定锁定 ONNX Runtime C API 1.31：其他版本报 `Error setting ORT API base`。
- `vectors.db` 与 `models/` 是派生状态。快照与 Git 备份不含它们。恢复会丢弃索引，并从恢复后的页面重建，而不是复用旧向量。
- 召回片段的相似度下限是 `internal/wiki/vectors/recall.go` 里的常量（`SemanticMinSimilarity`），不是参数。

---

## 运行模式

LeafWiki 有三种访问模式。按环境选择。

1. **内部百科，必须登录（默认）。**不加参数。每次访问都需要账号。
   ```bash
   ./leafwiki --jwt-secret=yoursecret --admin-password=yourpassword
   ```
2. **公开可读，编辑需登录。**读者不需要账号，编辑者与管理员需要。
   ```bash
   ./leafwiki --jwt-secret=yoursecret --admin-password=yourpassword --public-access=true
   ```
3. **关闭认证（`--disable-auth`）。**任何能访问服务的人都能读和写。
   ```bash
   ./leafwiki --disable-auth --host=127.0.0.1
   ```
   仅用于可信网络或本地环境，不要把 `--disable-auth` 实例暴露到公网。这种实例上没人能通过认证，Agent CLI 也无法删除。

角色为 admin、editor、viewer。编辑需要编辑者或管理员。

---

## 磁盘文件与重新同步

`<data>/root/**` 以 Markdown 存页面，是事实来源。frontmatter 保存托管的身份键（`leafwiki_id`、`leafwiki_title`、时间戳、作者 id、`leafwiki_aliases`）与 `tags`；其他键是页面属性。旁边的 SQLite 文件是派生的，可重建。

这些文件由 LeafWiki 自己写入。它不监听文件变化。如果在应用之外改动 Markdown（文本编辑器、脚本、批量导入、`git pull`），要通知运行中的服务：

- **管理界面：**页面树工具栏的刷新按钮会重新同步树、链接、标签、检索与语义索引，并显示实时进度。
- **系统信号：**向进程发 `SIGUSR1` 或 `SIGHUP`。Windows 没有等价信号，用按钮。

重新同步是核对，不是持续双向同步。应用之外新建、没有 `leafwiki_id` 的 Markdown 文件会在同步时写回一个 id，所以磁盘上会变化一次。如果 `root/` 还在外部 Git 流程里，这个 id 会出现在 diff（差异）中。

不要在服务运行期间直接改希望保留的页面文件：服务看不到改动，下一次从界面保存会覆盖它。改用 [Agent 访问](#agent-访问)，通过服务写入。

---

## 安全

自 v0.7.0 起默认启用：

- 会话使用 Secure、HttpOnly cookie（会话凭据）
- 所有会改变状态的请求都有 CSRF 保护
- 认证接口限流
- 基于角色的访问：admin、editor、viewer

本 fork 相关的注意点：

- `--disable-auth` 移除所有认证。仅用于本地开发或隔离网络。
- 控制通道是数据目录属主拥有的本地 socket，权限 `0660`，平台支持时校验对端 uid。它不签发 token（令牌）。读、新建、修改对该 uid 开放，信任级别等同于改文件；删除需要编辑者或管理员。`--control-auth=required` 对匿名调用方关闭通道。
- `--allow-insecure=true` 用于明文 HTTP。离开本机的流量请用反向代理提供 HTTPS。
- SMTP 与 Git 备份的凭据放在环境变量里，不要放在命令行上，进程列表会显示。

---

## 开发

技术栈：Go、React（Vite）、SQLite。

```bash
make setup     # 前端与 e2e 的 npm 依赖
make dev       # 后端与 Vite 一起启动；Ctrl-C 同时停止
```

Vite 在 `http://localhost:5173` 启动（用户名 `admin`，密码 `devpassword`），并把 API 代理到 `127.0.0.1:8080`。`make doctor` 检查 Go 与 Node 版本。

| 任务 | 命令 |
|---|---|
| 本地 CI 子集 | `make ci` |
| 后端测试与 lint | `make test`、`make lint` |
| Windows 与 macOS 构建检查 | `make cross` |
| 前端安装、lint、格式、测试、构建 | `make ui-check` |
| 启动器编译检查 | `make verify-launcher` |
| 仓库检查 | `make verify` |

需要编码器的测试读取 `LEAFWIKI_VECTORS_TEST_MODELS_DIR` 与 `LEAFWIKI_VECTORS_TEST_LIBRARY`；没有它们时相关用例跳过。用 `CGO_ENABLED=1 go test -tags vectors ./internal/embed` 运行。

架构、规则与源码地图在 [docs/development/](docs/development/README.md)。Agent 指令与评审角色在 [AGENTS.md](AGENTS.md)。界面语言有英语、德语、西班牙语、法语与中文；新增语言见 [docs/i18n.md](docs/i18n.md)。

---

## 上游、许可与贡献

- 上游项目：[perber/leafwiki](https://github.com/perber/leafwiki)。它的 README 记录了本 fork 继承的功能：Linux、Kubernetes、nginx 与树莓派安装；快照与恢复；Git 备份；SMTP 邮件；反向代理认证；Unix socket；键盘快捷键；页面排序。
- fork 基线：上游 `main` 的提交 `714e0368`。
- 上游的 release、Docker 镜像、在线 demo、`install.sh` 与 `update.sh` 属于上游，不含本 fork 的改动。
- 许可：MIT，见 [LICENSE](LICENSE)。
- 贡献规则：[CONTRIBUTING.md](CONTRIBUTING.md) 与 [docs/development/rules.md](docs/development/rules.md)。上游相关的问题与 PR 请提到上游；只涉及本 fork 的改动留在这里。
