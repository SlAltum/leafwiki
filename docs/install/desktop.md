# Desktop app (Windows)

LeafWiki has a double-clickable Windows build. A window shows the address, the initial password
and the log, one button (打开百科) opens the wiki, and closing the window stops the server. It is
for people who do not want a command line and do not want to remember a password.

It is the same LeafWiki server with a window around it. Pages, indexes, backups and search behave
exactly as in the server build, and the data lives in a `data` folder next to the program.

## Using it

1. Extract the whole `LeafWiki` folder. Do not move the exe out of it.
2. Double-click `leafwiki.exe`. The first start generates the credentials, starts the server and
   opens the browser once.
3. Later starts: click 打开百科. The browser no longer opens by itself.

Closing the window stops the server. Pages that were saved stay on disk.

## Why the whole folder moves together

The data directory is `data` next to the exe, and the credential files (`jwt.secret`,
`admin.password`) sit beside the exe, one level above `data`. Moving the exe alone gives you an
empty instance; moving the folder takes pages, users and password along.

The layout matches the command-line distribution: replacing the exe in an existing LeafWiki
folder reuses the old `jwt.secret`, `admin.password` and `data`, and the window shows the password
that instance already had.

The folder must be writable. In a location that needs administrator rights, such as
`C:\Program Files`, the window asks you to move the folder somewhere writable.

## Agent access (control channel)

The 基本 page has an **AI 助手** row with a 复制 button. It holds one sentence in Chinese that you
paste into an AI assistant:

> 这是一个本地 LeafWiki 文档站。请先读 "`…\doc\AGENT.md`"（里面有命令、退出码和规则），然后用
> "`…\doc\leafwiki.exe`" 帮我读写页面。

Every start rewrites `AGENT.md` next to the exe. The file is in Chinese and holds the current port
and socket, the common commands, the exit codes, the permission rules and the rule that files must
not be edited directly. The executable finds `data\` next to itself, so the prompt needs no paths.

The desktop build opens the local control channel like the server build does
(`<data>/control.sock` and `control.json`; see
[Agent control channel](../development/architecture.md#agent-control-channel)). The window's
打开百科 button opens the ordinary HTTP page. The local CLI
(`leafwiki page get|write|apply|delete`) uses the socket instead, which is a different path.

On the 高级设置 page, `control-socket` and `control-auth` are editable. The launcher drops empty
values when it builds the command line, so "empty value closes the channel" is not reachable from
the desktop build. To close the channel, start the server yourself with `--control-socket=`.
Setting `control-auth` to `required` makes writes need an account.

## Settings page and "show advanced"

The 高级设置 page turns every server flag into a table you can search, filter to changed rows
(只看改动项), and edit. Only the common rows are visible at first (port, stylesheet, semantic
search, snapshot interval and a few more); the rest wait behind 显示高级项.

- A change is validated with 测试解析 before 保存. A rejected value is not written to the
  configuration file, and the parser's message appears under the form.
- A saved change needs a restart, and the page offers 重启并应用.
- Rows the launcher owns, such as the data directory, credentials and listener address, are
  read-only.
- `--host`, `--unix-socket` and `--log-format` are not editable: the window always listens on
  `127.0.0.1`, and the log format is what the window parses.

## Troubleshooting

| Symptom | What to do |
|---|---|
| The status line says 失败 | Expand the technical message under the status line. An unwritable data directory, a busy port and a corrupt database each have their own message. |
| 另一个实例正在使用这个数据目录 | Check Task Manager for a leftover `leafwiki.exe`, then retry. |
| Port conflict | Change `port` on the 高级设置 page, or stop the program that holds it. |
| The browser does not open | Use 复制 next to the address and paste the URL into a browser. |
| A changed password was forgotten | Deleting `admin.password` does not restore the password; the hash lives in `data\users.db`. Use 重置管理员密码 in the window. |

The log appears in three places with the same lines: the log area at the bottom of the window,
`data\logs\launcher.log` (rotated to `launcher.log.1` at 1 MiB), and standard output when the
program is started from a command line. If the data directory is not writable, the log falls back
to `%TEMP%\leafwiki-launcher.log`, and the window shows that path.

## Build

```
make desktop-windows         # releases/LeafWiki/leafwiki.exe, frontend embedded
make desktop-windows-vectors # the same plus semantic search (cgo, ships models/ and onnxruntime.dll)
make verify-launcher         # compile check only, writes nothing (used by CI)
```

## Semantic search build (the release shape of this fork)

The desktop build with semantic search turns it on by default: the `vectors` row on the settings
page reads `auto`, and the server indexes saved pages in the background. It needs two things that
are shipped next to the executable but **not embedded in it**:

```
LeafWiki\
  leafwiki.exe            the launcher + vectors build (cgo)
  onnxruntime.dll         ONNX Runtime 1.31.x (win-x64), version must match
  models\
    bge-small-zh-v1.5\    model.onnx / tokenizer.json / manifest.json
  jwt.secret
  admin.password
  data\
```

The launcher finds `onnxruntime.dll` and `models\` next to the exe and passes them to the server
(`--vectors-library` and `--vectors-model-dir`). You can also set both on the 高级设置 page.

Build:

```
make desktop-windows-vectors VECTORS_ORT_DLL=D:\path\to\onnxruntime.dll
```

- Needs a C toolchain (MinGW on Windows). The server build stays `CGO_ENABLED=0` and is not
  affected.
- `onnxruntime.dll` must match the bound C API version, **1.31.x**. Version 1.27 fails with
  `Error setting ORT API base: 2`, and 1.17 fails with `Error setting ORT API base`.
- A missing DLL or model does not make the server unusable: the log says
  `semantic search unavailable` and search falls back to keywords.
- Changing the model means replacing `models/<id>/` (the `manifest.json` and its checksums). No
  rebuild is needed.

**Neither file is in the repository** (`.gitignore` covers `releases/*`, `assets/tokenizer/` and
`assets/tokenizer-*/`), so a clean checkout needs them put back before packaging. Where to get
them, the directory layout and the `manifest.json` rules are in
[Semantic search assets](../development/vectors-assets.md).

| File | Where it comes from | Version constraint |
|---|---|---|
| `assets/tokenizer/ort/onnxruntime-win-x64-1.31.1/lib/onnxruntime.dll` | The official ONNX Runtime release, `onnxruntime-win-x64-1.31.x.zip` | 1.31.x only: the binding uses ORT C API 1.31, and older versions fail with `Error setting ORT API base` |
| `assets/tokenizer-bge/` (`model.onnx` about 95 MB, `tokenizer.json`, `manifest.json`) | An ONNX export of bge-small-zh-v1.5 (kept locally, same as above) | `model_id`, `dim`, `pooling` and `query_prefix` in `manifest.json` must match the model; changing the model replaces this directory only |

Check three things before shipping:

1. The startup log contains `semantic search enabled mode=auto model=…` and no
   `semantic search unavailable`.
2. After an admin login, `GET /api/admin/search/vectors/status` returns `"state":"fresh"` and
   `"pending":0`.
3. A paraphrased query (one that is not in the page's own words) returns `semantic` as `ok` and
   finds the target page. The floor is `SemanticMinSimilarity = 0.6` in
   `internal/wiki/vectors/recall.go`; passages below it are not recalled.

The window code sits behind the `launcher` build tag, so the server binary contains no GUI
dependency. The launcher only targets Windows: the darwin and linux backends of Gio still need
cgo.
