# 桌面启动器（Windows）

LeafWiki 有一个可双击启动的 Windows 版本：窗口里显示地址、初始密码和日志，
一个按钮打开百科，关掉窗口就停止服务。适合不想碰命令行、也不想记密码的人。

它是**同一个** LeafWiki 服务端，加了一个窗口壳：页面、索引、备份、检索全部一样，
数据落在程序旁边的 `data` 文件夹里。

## 双击使用

1. 解压整个 `LeafWiki` 文件夹（不要只把 exe 拖出来）。
2. 双击 `leafwiki.exe`。首次启动会自己生成凭据、启动服务，并打开一次浏览器。
3. 之后再次打开：窗口里点“打开百科”即可，浏览器不会自动弹出第二次。

关掉窗口会停止服务，百科页面里已经保存的内容保持不变。

## 为什么要求整个文件夹一起移动

数据目录固定为 **exe 所在目录下的 `data`**；凭据文件（`jwt.secret`、
`admin.password`）放在 **exe 旁边**，也就是 `data` 的上一层。把 exe 单独拷到别处，
等于换了一个空实例；把整个文件夹移到桌面或“文档”，页面、用户、密码都跟着走。

这个布局与旧的命令行发行版一致：把一个已有的 LeafWiki 文件夹换成这个 exe，
它直接复用原来的 `jwt.secret`、`admin.password` 和 `data`，窗口里显示的就是原来那个密码。

文件夹必须可写。放在 `C:\Program Files` 这类需要管理员权限的位置时，窗口会
提示把整个文件夹移到可写位置。

## Agent 访问（控制通道）

面板“基本”页有一行**「AI 助手」**，带复制按钮，内容是一段可以直接粘贴给 AI 的提示词：

> 这是一个本地 LeafWiki 文档站。请先读 "`…\doc\AGENT.md`"（里面有命令、退出码和规则），
> 然后用 "`…\doc\leafwiki.exe`" 帮我读写页面。

启动器每次启动都会重写 `AGENT.md`（端口与 socket 都是当前值），里面是常用命令、退出码、权限规则与
“不要直接改文件”的约定。可执行文件会自己找到旁边的 `data\` 目录，所以提示词里不需要任何参数路径。

桌面版与服务端一样会打开本地控制通道（`<数据目录>/control.sock` + `control.json`，见
[Agent control channel](../development/architecture.md#agent-control-channel)）。启动器窗口里的“打开 wiki”是普通
HTTP 页面，与本机 CLI（`leafwiki page get|write|apply|delete`）并不同路：CLI 走的是那条 socket。

高级设置里 `control-socket` 与 `control-auth` 都可编辑。注意启动器**会丢弃空值参数**，所以
“留空 = 关闭通道”在桌面版里做不到：要关闭通道，请绕开启动器自行运行服务端并加
`--control-socket=`。把 `control-auth` 设为 `required` 则写入必须带账号。

## 参数页与“显示高级项”

第二页“高级设置”把服务端所有命令行参数做成了表格，可以搜索、只看改动项，
默认只露出常用的几项（端口、样式表、语义检索、快照间隔等），其余点“显示高级项”。

- 改动**先“测试解析”再“保存”**：校验不过不会写进配置文件，错误原文直接显示在表单下方。
- 保存后需要重启服务才生效，页面会给出“重启并应用”。
- 数据目录、测试凭据、监听地址等由启动器自己管理的项显示为只读。
- `--host`、`--unix-socket`、`--log-format` 不出现在可编辑项里：窗口固定只监听
  127.0.0.1，日志格式是窗口解析用的。

## 排障

| 现象 | 处理 |
|---|---|
| 窗口里状态是“失败” | 展开状态行下面的技术原文；数据目录不可写、端口冲突、数据库损坏各有对应提示 |
| 提示“另一个实例正在使用这个数据目录” | 先看任务管理器里是否还有 `leafwiki.exe`；确认没有后重试 |
| 端口冲突 | 高级设置里把 `port` 改成别的端口，或关掉占用它的程序 |
| 打不开浏览器 | 点“地址”旁边的“复制”，手动粘到浏览器里 |
| 忘了改过的密码 | 删掉 `admin.password` 不会恢复密码（密码是 `data\users.db` 里的哈希）；用窗口里的“重置管理员密码” |

日志有三处、内容相同：窗口底部的日志区、程序文件夹下的
`data\logs\launcher.log`（超过 1MiB 滚动为 `launcher.log.1`）、以及从命令行
启动时的标准输出。数据目录不可写时日志会退到系统临时目录
`%TEMP%\leafwiki-launcher.log`，路径同时写在窗口里。

## 构建

```
make desktop-windows         # releases/LeafWiki/leafwiki.exe，含内嵌前端
make desktop-windows-vectors # 同上 + 语义检索（cgo，附带 models/ 与 onnxruntime.dll）
make verify-launcher         # 只编译检查，不产出文件（CI 用）
```

## 语义检索版（本 fork 的发布形态）

桌面版默认开启语义检索：参数页里 `vectors` 的默认值是 `auto`，服务端启动后
自动为已保存的页面建向量索引。它需要两样**随包发布、但不嵌进 exe** 的东西：

```
LeafWiki\
  leafwiki.exe            ← 带 launcher + vectors 两个构建标签（cgo）
  onnxruntime.dll         ← ONNX Runtime 1.31.x（win-x64），版本必须匹配
  models\
    bge-small-zh-v1.5\     ← model.onnx / tokenizer.json / manifest.json
  jwt.secret
  admin.password
  data\
```

启动器会自动发现 exe 旁边的 `onnxruntime.dll` 与 `models\`，并传给服务端
（`--vectors-library` / `--vectors-model-dir`）；高级设置里也可以显式指定。

构建：

```
make desktop-windows-vectors VECTORS_ORT_DLL=D:\path\to\onnxruntime.dll
```

- 需要 C 工具链（Windows 上用 MinGW）；服务端产物仍是 `CGO_ENABLED=0`，不受影响
- `onnxruntime.dll` 必须与绑定的 C API 版本一致：**1.31.x**。1.27 会报
  `Error setting ORT API base: 2`，1.17 报 `Error setting ORT API base`
- 缺 dll 或模型缺失不会让服务不可用：日志里有一条
  `semantic search unavailable`，检索自动退回关键词
- 换模型只需换 `models/<id>/`（`manifest.json` 描述文件与校验和），不用重新编译

**这两样东西不在仓库里**（`.gitignore` 忽略 `releases/*`、`assets/tokenizer/`、
`assets/tokenizer-*/`），所以从干净检出打包前要先把它们放回本地。获取方式、目录布局与
`manifest.json` 的校验规则见 [语义检索的模型与推理库](../development/vectors-assets.md)。

| 文件 | 从哪来 | 版本约束 |
|---|---|---|
| `assets/tokenizer/ort/onnxruntime-win-x64-1.31.1/lib/onnxruntime.dll` | ONNX Runtime 官方 release，`onnxruntime-win-x64-1.31.x.zip` | 必须 1.31.x：绑定的是 ORT C API 1.31，低版本报 `Error setting ORT API base` |
| `assets/tokenizer-bge/`（`model.onnx` 95MB、`tokenizer.json`、`manifest.json`） | bge-small-zh-v1.5 的 ONNX 导出（本地留档同上） | `manifest.json` 里的 `model_id`/`dim`/`pooling`/`query_prefix` 必须与模型一致，换模型只换这个目录 |

发布前自检（三条都要成立）：

1. 启动日志出现 `semantic search enabled mode=auto model=…`，且**没有**
   `semantic search unavailable`
2. `GET /api/admin/search/vectors/status`（管理员登录后）返回
   `"state":"fresh"`、`"pending":0`
3. 发一条“改写”查询（不是页面原文里的词），结果里 `semantic` 为 `ok` 且能命中目标页
   记录。阈值是 `internal/wiki/vectors/recall.go` 的 `SemanticMinSimilarity = 0.6`，
   低于它的片段不会被召回

窗口代码在 `launcher` build tag 之后，服务端产物完全不含 GUI 依赖；
启动器只承诺 Windows（Gio 的 darwin/linux 后端仍需要 cgo）。