# LeafWiki

English | [中文](README.md)

Self-hosted wiki. Markdown files on disk, a SQLite index beside them, one Go binary.

This repository is a fork of [perber/leafwiki](https://github.com/perber/leafwiki), based on
commit `714e0368`. Upstream owns the project. The fork adds work that has not landed upstream,
listed in [What this fork adds](#what-this-fork-adds), and keeps the rest unchanged.

> **No releases yet.** This fork publishes no binaries and no container images. You build it
> yourself with the [install](#install) steps below. The upstream releases, the
> `ghcr.io/perber/leafwiki` image, `install.sh` and `update.sh` all serve upstream's build,
> which does not contain the changes described here.

---

## Table of Contents

- [What this fork adds](#what-this-fork-adds)
- [Install](#install)
  - [From source](#from-source)
  - [Docker](#docker)
  - [Windows desktop app](#windows-desktop-app)
  - [Admin commands](#admin-commands)
- [Search](#search)
  - [Keyword search](#keyword-search)
  - [Semantic recall](#semantic-recall)
- [Agent access (CLI)](#agent-access-cli)
- [Configuration](#configuration)
  - [Required](#required)
  - [CLI flags](#cli-flags)
  - [Environment variables](#environment-variables)
  - [Semantic search settings](#semantic-search-settings)
- [Operating modes](#operating-modes)
- [Files on disk and resync](#files-on-disk-and-resync)
- [Security](#security)
- [Development](#development)
- [Upstream, license, contributing](#upstream-license-contributing)

---

## What this fork adds

| Area | What you get | Details |
|---|---|---|
| Search | Chinese, Japanese and Korean text is searchable by word, not only by prefix. Full-width ASCII folds to ASCII. A mistyped Latin word gets a "search for this instead" suggestion. Hits report their section heading and anchor in the API. Aliases declared in frontmatter are searchable. | [Keyword search](#keyword-search) |
| Semantic recall | Optional hybrid search: pages are embedded in-process and fused with the keyword results, so a page written with different words is still found. Built for `bge-small-zh-v1.5` (Chinese) and for any model with the same manifest format. | [Semantic recall](#semantic-recall) |
| Desktop app | A Windows build with a window: address, initial password, log, a settings page for every flag, and an AI 助手 prompt, with `AGENT.md` written next to the exe. | [Windows desktop app](#windows-desktop-app) |
| Agent access | The same binary is a CLI: `status`, `page get\|write\|apply\|delete`, `check`, `vectors build\|sync\|status`. It talks to the running server over a local socket, or writes offline when the server is stopped. | [Agent access](#agent-access-cli) |

Everything else (tree navigation, editor, tags, properties, revisions, user management, snapshots,
Git backup, SMTP, reverse-proxy authentication) is upstream code, unchanged.

**Behavior changes that come with the above.** These are the places where this build acts
differently from an upstream build:

- A second server on the same data directory refuses to start. The running server holds
  `<data>/wiki.lock` for its lifetime.
- `vectors build`, `vectors sync`, `reset-admin-password` and `restore-snapshot` exit `75` while a
  server runs on that data directory.
- `--data-dir` prefers `data/` next to the executable when that directory exists.
- The Docker image creates `/app/data` as `0755` (was `0777`): the agent channel's trust boundary
  is the data directory's owner.
- No `linux/386` build: the encoder dependency does not compile for 32-bit.

---

## Install

### From source

You need Go (the `toolchain` line in `go.mod`), Node.js (`.nvmrc`) and GNU Make.

```bash
git clone https://github.com/SlAltum/leafwiki.git
cd leafwiki
make build        # installs the UI dependencies, builds the SPA, embeds it, writes ./leafwiki
./leafwiki --jwt-secret=yoursecret --admin-password=yourpassword --allow-insecure=true
```

`--allow-insecure=true` is required for plain HTTP: without it the server refuses to sign the
authentication cookie. Omit it when you serve over HTTPS and your reverse proxy forwards
`X-Forwarded-Proto: https`.

The server binds to `127.0.0.1:8080` by default. Add `--host=0.0.0.0` to expose it on the
network.

**Data directory.** `--data-dir` defaults to `./data`, or to `data/` next to the executable when
that directory exists. The second rule is what lets a shipped folder (exe, credentials, `data/`)
run from anywhere.

**Release builds for other platforms** need Docker: `make release` builds
linux-amd64, linux-arm64, windows-amd64, darwin-amd64 and darwin-arm64, and writes checksums
to `releases/`. These builds use `CGO_ENABLED=0` and the plain build tags, so they do not
include semantic recall (see [Semantic recall](#semantic-recall)).

### Docker

Build the image from this repository:

```bash
docker build -f Dockerfile -t leafwiki .
docker run -p 8080:8080 \
  -v ~/leafwiki-data:/app/data \
  leafwiki \
  --jwt-secret=yoursecret --admin-password=yourpassword --allow-insecure=true
```

- The image is built without the `vectors` tag, so it accepts only `--vectors=off`.
- `/app/data` is created as `0755`. The agent channel trusts "same uid as the data directory",
  so a mounted volume must be writable by the uid you run the container as.
- Run the agent CLI inside the container (`docker compose exec leafwiki /app/leafwiki ...`):
  the socket and the lock live in the container's data directory.

The upstream image `ghcr.io/perber/leafwiki:latest` is upstream's build. It has none of the
changes on this page.

### Windows desktop app

```bash
make desktop-windows            # window, keyword search only
make desktop-windows-vectors VECTORS_ORT_DLL=D:\path\to\onnxruntime.dll
```

Both write `releases/LeafWiki/leafwiki.exe` with the frontend embedded. The second target needs
a C toolchain (MinGW on Windows), the `onnxruntime.dll` (1.31.x, win-x64) and the model in
`assets/tokenizer-bge/`; it copies the DLL and `models/<id>/` next to the exe.

Use the folder as a whole. The exe finds `data/` next to itself, and the first start generates
`jwt.secret` and `admin.password` there. On first start the window opens the wiki in the
browser once; later starts do not.

The window is in Chinese. It has two pages: 基本 (address, password, AI 助手 prompt, start and
stop, log) and 高级设置 (every server flag as an editable row, with 测试解析 before 保存). The data
directory, the credentials and the flags the launcher decides itself (`--host`, `--unix-socket`,
`--log-format`, `--allow-insecure`, public access, authentication) are owned by the launcher and
shown read-only. Empty values are dropped when the launcher builds the command line, so you
cannot close the agent channel from the settings page; start the server yourself with
`--control-socket=` for that.

The `desktop-windows-vectors` build defaults to `--vectors=auto`; a build without the encoder
forces `--vectors=off`.

Details, layout, troubleshooting and release checks: [docs/install/desktop.md](docs/install/desktop.md).

### Admin commands

Both commands take `<data>/wiki.lock`, so they exit `75` while a server is running on that data
directory. Stop the server first.

```bash
./leafwiki --data-dir ./data reset-admin-password
./leafwiki --data-dir ./data restore-snapshot ./data/snapshots/leafwiki-snapshot-2026-01-01.zip
```

`reset-admin-password` generates a new random password and prints it, which is the way back in
when nobody remembers the old one. `restore-snapshot` replaces a data directory
from a snapshot ZIP, for disaster recovery or when seeding a fresh instance; a running server
can also restore from the admin UI (Settings → Full Backup), which gates writes while the files
are swapped. A restore drops the semantic index and rebuilds it from the restored pages.

---

## Search

### Keyword search

The keyword index is always on and needs no configuration.

- **CJK text is indexed by word.** A run of Han, Hiragana, Katakana or Hangul characters is
  indexed as adjacent two-character tokens (and as its single character when the run is one
  character long). A query goes through the same analyser, so `部署` and `回滚` are found inside
  a Chinese sentence, not only at its start. Index and query share one implementation.
- **Width folds.** Full-width ASCII (`ＡＢＣ`) folds to ASCII, and half-width katakana (`ｶﾀｶﾅ`)
  folds to full-width, so either spelling finds the text.
- **Short queries still search.** The minimum is 2 characters for a CJK query and 3 for a Latin
  one; the input box, the URL, the API call and the scroll behaviour all use the same predicate.
- **Mistyped Latin words are corrected.** A query word missing from the corpus gets candidates at
  edit distance 1, then 2, and the query matches either spelling while keeping the word as typed.
  The UI offers the corrected query as a clickable suggestion. CJK queries are not corrected.
- **Opening a hit lands on the match.** A result opens its page with the query kept in the URL;
  the viewer highlights the first match and scrolls it into view. The API also reports the
  section heading and anchor of each hit (`section`, `anchor`) for clients that want to link
  deeper.
- **Aliases are searchable.** Declare `leafwiki_aliases` in frontmatter (a string or a list) and
  a search for that name finds the page; the editor has an aliases field. A hit that came from an
  alias reports `matched: "alias"` with the alias text.
- **Tags filter results**, and the tag facets report counts per tag.

Titles, excerpts and highlights come from the page itself, not from the index: the index stores
analysed text, which is right for matching and wrong for reading.

### Semantic recall

Semantic recall is opt-in and off by default. When it is on, the page introduction and every
heading section are embedded in-process and stored in `<data>/vectors.db`. Search answers from
both indexes and fuses them with reciprocal rank fusion (k=60), so a page that shares no words
with the query can still rank first.

```bash
./leafwiki --vectors=auto \
  --vectors-model-dir=./data/models \
  --vectors-library=/path/to/libonnxruntime.so \
  --jwt-secret=yoursecret --admin-password=yourpassword
```

| Mode | Behavior |
|---|---|
| `off` (default) | No model is loaded, no `vectors.db` is created, search answers from keywords only. |
| `auto` | Saving a page queues it, and the queue embeds it in the background. |
| `manual` | Nothing is queued. The index changes only when you ask for a rebuild. |

**What it needs.** A model directory with a `manifest.json` (model id, dimension, pooling, query
prefix, file checksums) and an ONNX Runtime shared library. The manifest pins the checksums, so
a model whose files were edited or partly copied is rejected rather than embedded wrong.
Missing model or missing library does not stop the server: the log says
`semantic search unavailable` and search falls back to keywords.

**Which builds have it.** The encoder is a cgo binding behind the `vectors` build tag. The
release binaries and the Docker image are built without it and accept only `--vectors=off`. To
get semantic recall:

- use the Windows desktop build with semantic search (`make desktop-windows-vectors`), or
- build with `CGO_ENABLED=1 -tags vectors` and supply a model and the runtime library.

The default model is `bge-small-zh-v1.5` (512 dimensions, Chinese). Where to get the model and
the DLL, and how `manifest.json` is validated:
[docs/development/vectors-assets.md](docs/development/vectors-assets.md).

**Admin views.** Settings → Semantic index shows the index state, the model, pending and failed
counts, the last error, a pause switch for automatic indexing, and rebuild / clear-and-rebuild.
The page menu (admin only) has a "Semantic index" entry that shows how the current page is
indexed and can rebuild that one page.

**CLI.** `leafwiki --data-dir ./data vectors status`, `vectors build [--full]`,
`vectors sync [--dry-run] [--paths a,b]`. `build` and `sync` take the data directory lock, so
they exit `75` while a server runs.

**States.** The index is `off`, `missing`, `queued`, `running`, `stale`, `failed` or `fresh`;
`paused` is a separate switch. A query reports `semantic` as `off`, `ok`, `stale` or
`unavailable`, and a stale or unavailable index never invents results: the keyword path answers.

---

## Agent access (CLI)

LeafWiki speaks to local agents over a **control socket** in its data directory, and the same
binary is the client. This is for a coding assistant or a script on the same machine. It is not
another remote API. Design notes:
[Agent control channel](docs/development/architecture.md#agent-control-channel).

```bash
leafwiki --data-dir ./data status                       # protocol, pages, vector state
leafwiki --data-dir ./data page get platform/deploy     # path, id, title, version, tags, content
leafwiki --data-dir ./data page write platform/deploy --title "Deploy" --content-file - < deploy.md
leafwiki --data-dir ./data page apply --batch changes.jsonl --json
leafwiki --data-dir ./data page delete platform/deploy --dry-run
leafwiki --data-dir ./data check --json
```

- **Reads, creates and updates are open** to a caller that does not authenticate. That is the
  same trust level as editing the Markdown files on disk. **Deleting needs an editor or an
  admin**: pass `--user` with `--password`, `--password-file`, or
  `LEAFWIKI_AGENT_USER`/`LEAFWIKI_AGENT_PASSWORD`.
- `--control-auth=required` closes everything except `status` and `auth.login`.
- `--control-socket=""` turns the channel off. A socket that cannot be bound (a long path on
  Windows) logs a warning and the server keeps running without it.
- **One writer per data directory.** The server holds `<data>/wiki.lock` for its lifetime, so a
  second instance refuses to start instead of half-sharing the directory. With no server
  running, the write commands work offline (read the note below).
- `--json` prints exactly one JSON object on success and on a partial batch. A failing command
  prints nothing on stdout: the error goes to stderr and the exit code carries the outcome.
- A batch is atomic in its precheck. One bad item refuses the whole batch and writes nothing; a
  failure during execution stops the batch, and the items already written are reported.
- `--wait` (default 30 s) waits for the semantic index of the pages just written. It does
  nothing in `off`, `manual` or `paused` mode.
- `check` is read-only and takes no lock. It reports tree problems such as invalid filenames,
  name conflicts, duplicate ids and unparsable frontmatter.

**Offline writes.** With no server running, `page write`, `page apply` and `page delete` take
`<data>/wiki.lock` and run the server's own use cases, without revisions. `page get`, `status`
and `--dry-run` need the running server. If `<data>/vectors.db` and `<data>/models` exist the
changed pages are re-embedded; otherwise the command warns and you run `vectors sync` later.

**Exit codes:** `0` success, `1` unclassified, `2` usage, `3` not found, `4` version conflict,
`5` not allowed, `6` unreachable, timeout or restore running, `7` capability missing, `8`
partial batch, `75` data directory locked.

In a container, run the CLI inside it: the socket and the lock are in the container's data
directory.

The runbook with every command, flag and JSON shape is
[docs/development/skills/leafwiki/SKILL.md](docs/development/skills/leafwiki/SKILL.md); the
desktop launcher writes the same hints to `AGENT.md` next to the exe.

---

## Configuration

Every flag has an environment variable, listed next to it. Command line flags win over
environment variables, which win over the defaults. An environment variable that is set but
empty counts as unset.

### Required

| Flag | Description |
|---|---|
| `--jwt-secret` | Secret for signing JWTs. Keep it secure. |
| `--admin-password` | Initial admin password. Applied only if no admin exists yet. |

For plain HTTP, add `--allow-insecure=true` so the login and CSRF cookies work.

### CLI flags

Rows marked *fork* exist only in this repository.

| Flag | Description | Default | Since |
|---|---|---|---|
| `--host` | Host/IP the server binds to | `127.0.0.1` | upstream |
| `--port` | Port the server listens on | `8080` | upstream |
| `--unix-socket` | Unix domain socket path; overrides `--host` and `--port` | `""` | v0.11.3 |
| `--data-dir` | Directory where data is stored | `data/` next to the exe when it exists, else `./data` | upstream |
| `--admin-username` | Initial admin username | `admin` | v0.12.0 |
| `--admin-email` | Initial admin email | `admin@localhost` | v0.12.0 |
| `--public-access` | Allow public read-only access | `false` | upstream |
| `--base-path` | URL prefix for reverse proxy setups (e.g. `/wiki`) | `""` | v0.8.2 |
| `--public-url` | Public base URL of this wiki; required with `--smtp-host` | `""` | v0.13.0 |
| `--allow-insecure` | Enables HTTP for auth cookies (required for plain HTTP) | `false` | v0.7.0 |
| `--disable-auth` | Disable all authentication (trusted networks only) | `false` | v0.7.0 |
| `--access-token-timeout` | Access token duration (e.g. `24h`) | `15m` | v0.7.0 |
| `--refresh-token-timeout` | Refresh token duration (e.g. `168h`) | `168h` | v0.7.0 |
| `--max-asset-upload-size` | Max upload size (e.g. `50MiB`) | `50MiB` | v0.8.5 |
| `--custom-stylesheet` | Path to a `.css` file inside the data directory | `""` | v0.8.5 |
| `--default-language` | Default UI language (`en`, `de`, `es`, `fr`, `zh`) | `""` | v0.13.0 |
| `--inject-code-in-header` | Raw HTML/JS injected into `<head>` | `""` | v0.6.0 |
| `--hide-link-metadata-section` | Hide the backlinks and link status panel | `false` | upstream |
| `--enable-revision` | Enable revision history | `false` | v0.9.0 |
| `--enable-link-refactor` | Rewrite links on rename and move | `false` | v0.9.0 |
| `--enable-api-key-management` | Experimental API key management | `false` | upstream |
| `--enable-share-links` | Experimental share links (not usable yet) | `false` | upstream |
| `--editor-limit` | Max admin and editor accounts; `0` = unlimited | `0` | upstream |
| `--max-revision-history` | Max revisions per page; `0` = unlimited | `100` | v0.9.0 |
| `--revision-coalesce-window` | Coalesce rapid auto-saves by one author; `0` = off | `5m` | v0.11.0 |
| `--enable-http-remote-user` | Reverse-proxy authentication via HTTP header | `false` | v0.10.0 |
| `--http-remote-user-header-name` | Header carrying the username or email from the proxy | `Remote-User` | v0.10.0 |
| `--enable-http-remote-user-auto-create` | Auto-provision users the proxy asserts | `false` | v0.12.1 |
| `--http-remote-user-email-header-name` | Header carrying the email for auto-created users | `""` | v0.12.1 |
| `--http-remote-user-default-role` | Role for auto-created users; must not be `admin` | `viewer` | v0.12.1 |
| `--trusted-proxy-ips` | Trusted proxy IPs/CIDRs for the remote-user header | `""` | v0.10.0 |
| `--login-url` | External login URL instead of the built-in form | `""` | v0.12.0 |
| `--logout-url` | External redirect after logout | `""` | v0.12.0 |
| `--http-remote-user-logout-url` | Deprecated, use `--logout-url` | `""` | v0.10.0 |
| `--user-management-url` | External page instead of the built-in user management | `""` | v0.11.4 |
| `--disable-request-log` | Suppress per-request HTTP log lines | `false` | v0.10.1 |
| `--log-format` | Log format: `text` or `json` | `text` | v0.12.0 |
| `--totp-encryption-key` | Key to encrypt TOTP secrets at rest (min 32 bytes) | `""` | v0.12.0 |
| `--enable-metrics` | Prometheus `/metrics` on a separate listener | `false` | v0.12.0 |
| `--metrics-host` | Host/IP for the metrics listener | `127.0.0.1` | v0.12.0 |
| `--metrics-port` | Port for the metrics listener | `9091` | v0.12.0 |
| `--snapshot` | Enable snapshot ZIPs (include the SQLite database) | `true` | v0.12.0 |
| `--snapshot-interval` | Snapshot interval; `0` = manual only | `24h` | v0.12.0 |
| `--snapshot-retention` | Keep the most recent N snapshots; `<= 0` = keep all | `10` | v0.12.0 |
| `--snapshot-dir` | Directory for snapshot ZIPs | `<data-dir>/snapshots` | v0.12.0 |
| `--restore-upload-max-size` | Max size of an uploaded backup ZIP | `500MiB` | v0.12.0 |
| `--git-backup` | Enable git backup to a remote repository | `false` | v0.11.3 |
| `--git-backup-remote` | Remote URL for git backup | `""` | v0.11.3 |
| `--git-backup-branch` | Branch to push to | `main` | v0.11.3 |
| `--git-backup-path` | Directory holding `root/` and `assets/`; empty = top level | `""` | v0.13.0 |
| `--git-backup-ssh-key` | Raw SSH private key (prefer the environment variable) | `""` | v0.11.3 |
| `--git-backup-ssh-key-path` | Path to the SSH private key file | `""` | v0.11.3 |
| `--git-backup-ssh-known-hosts` | Path to `known_hosts` for MITM protection | `""` | v0.11.3 |
| `--git-backup-http-username` | Username for HTTP(S) basic auth | `""` | v0.13.0 |
| `--git-backup-http-password` | Password or token for HTTP(S) basic auth | `""` | v0.13.0 |
| `--git-backup-author-name` | Commit author name | `LeafWiki Backup` | v0.11.3 |
| `--git-backup-author-email` | Commit author email | `backup@leafwiki.local` | v0.11.3 |
| `--git-backup-interval` | Backup interval; `0` = manual only | `60m` | v0.11.3 |
| `--smtp-host` | SMTP host for password reset and invites; unset disables email | `""` | v0.13.0 |
| `--smtp-port` | SMTP port | `587` | v0.13.0 |
| `--smtp-username` | SMTP auth username | `""` | v0.13.0 |
| `--smtp-password` | SMTP auth password | `""` | v0.13.0 |
| `--smtp-from` | From address (required with `--smtp-host`) | `""` | v0.13.0 |
| `--smtp-from-name` | From display name | `LeafWiki` | v0.13.0 |
| `--smtp-security` | Transport security: `none`, `starttls` or `tls` | `starttls` | v0.13.0 |
| `--smtp-insecure-skip-verify` | Skip TLS verification (testing only) | `false` | v0.13.0 |
| `--smtp-timeout` | Timeout for one SMTP send | `10s` | v0.13.0 |
| `--vectors` | Semantic search: `off`, `auto` or `manual` | `off` | *fork* |
| `--vectors-model-dir` | Directory holding the encoder model | `<data-dir>/models` | *fork* |
| `--vectors-library` | Path to the ONNX Runtime shared library | `""` | *fork* |
| `--control-socket` | Agent socket; empty disables the channel | `<data-dir>/control.sock` | *fork* |
| `--control-auth` | `optional` or `required` for anonymous agent writes | `optional` | *fork* |

> The Docker entrypoint sets `LEAFWIKI_HOST=0.0.0.0` when neither `--host` nor `LEAFWIKI_HOST`
> is provided.

### Environment variables

| Variable | Description | Default | Since |
|---|---|---|---|
| `LEAFWIKI_HOST` | Host/IP address | `127.0.0.1` | upstream |
| `LEAFWIKI_PORT` | Port | `8080` | upstream |
| `LEAFWIKI_UNIX_SOCKET` | Unix socket path; overrides host and port | `""` | v0.11.3 |
| `LEAFWIKI_DATA_DIR` | Data directory path | `data/` next to the exe when it exists, else `./data` | upstream |
| `LEAFWIKI_ADMIN_PASSWORD` | Initial admin password (required) | none | upstream |
| `LEAFWIKI_ADMIN_USERNAME` | Initial admin username | `admin` | v0.12.0 |
| `LEAFWIKI_ADMIN_EMAIL` | Initial admin email | `admin@localhost` | v0.12.0 |
| `LEAFWIKI_JWT_SECRET` | JWT signing secret (required) | none | upstream |
| `LEAFWIKI_PUBLIC_ACCESS` | Allow public read-only access | `false` | upstream |
| `LEAFWIKI_BASE_PATH` | URL prefix for a reverse proxy | `""` | v0.8.2 |
| `LEAFWIKI_PUBLIC_URL` | Public base URL; required with `LEAFWIKI_SMTP_HOST` | `""` | v0.13.0 |
| `LEAFWIKI_ALLOW_INSECURE` | Enable HTTP auth cookies | `false` | v0.7.0 |
| `LEAFWIKI_DISABLE_AUTH` | Disable authentication | `false` | v0.7.0 |
| `LEAFWIKI_ACCESS_TOKEN_TIMEOUT` | Access token duration | `15m` | v0.7.0 |
| `LEAFWIKI_REFRESH_TOKEN_TIMEOUT` | Refresh token duration | `168h` | v0.7.0 |
| `LEAFWIKI_MAX_ASSET_UPLOAD_SIZE` | Max upload size | `50MiB` | v0.8.5 |
| `LEAFWIKI_CUSTOM_STYLESHEET` | Path to a `.css` file inside the data directory | `""` | v0.8.5 |
| `LEAFWIKI_DEFAULT_LANGUAGE` | Default UI language | `""` | v0.13.0 |
| `LEAFWIKI_INJECT_CODE_IN_HEADER` | HTML/JS injected into `<head>` | `""` | v0.6.0 |
| `LEAFWIKI_HIDE_LINK_METADATA_SECTION` | Hide the backlinks panel | `false` | upstream |
| `LEAFWIKI_ENABLE_REVISION` | Revision history | `false` | v0.9.0 |
| `LEAFWIKI_ENABLE_LINK_REFACTOR` | Link rewriting on rename and move | `false` | v0.9.0 |
| `LEAFWIKI_ENABLE_API_KEY_MANAGEMENT` | Experimental API key management | `false` | upstream |
| `LEAFWIKI_ENABLE_SHARE_LINKS` | Experimental share links (not usable yet) | `false` | upstream |
| `LEAFWIKI_EDITOR_LIMIT` | Max admin and editor accounts; `0` = unlimited | `0` | upstream |
| `LEAFWIKI_MAX_REVISION_HISTORY` | Max revisions per page | `100` | v0.9.0 |
| `LEAFWIKI_REVISION_COALESCE_WINDOW` | Coalesce rapid auto-saves; `0` = off | `5m` | v0.11.0 |
| `LEAFWIKI_ENABLE_HTTP_REMOTE_USER` | Reverse-proxy authentication | `false` | v0.10.0 |
| `LEAFWIKI_HTTP_REMOTE_USER_HEADER_NAME` | Header carrying the proxy user | `Remote-User` | v0.10.0 |
| `LEAFWIKI_ENABLE_HTTP_REMOTE_USER_AUTO_CREATE` | Auto-provision proxy users | `false` | v0.12.1 |
| `LEAFWIKI_HTTP_REMOTE_USER_EMAIL_HEADER_NAME` | Email header for auto-created users | `""` | v0.12.1 |
| `LEAFWIKI_HTTP_REMOTE_USER_DEFAULT_ROLE` | Role for auto-created users | `viewer` | v0.12.1 |
| `LEAFWIKI_TRUSTED_PROXY_IPS` | Trusted proxy IPs/CIDRs | `""` | v0.10.0 |
| `LEAFWIKI_LOGIN_URL` | External login URL | `""` | v0.12.0 |
| `LEAFWIKI_LOGOUT_URL` | External logout redirect | `""` | v0.12.0 |
| `LEAFWIKI_HTTP_REMOTE_USER_LOGOUT_URL` | Deprecated, use `LEAFWIKI_LOGOUT_URL` | `""` | v0.10.0 |
| `LEAFWIKI_USER_MANAGEMENT_URL` | External user management page | `""` | v0.11.4 |
| `LEAFWIKI_DISABLE_REQUEST_LOG` | Suppress per-request log lines | `false` | v0.10.1 |
| `LEAFWIKI_LOG_FORMAT` | Log format: `text` or `json` | `text` | v0.12.0 |
| `LEAFWIKI_LOG_LEVEL` | `debug`, `info`, `warn` or `error` (environment only) | `info` | v0.8.0 |
| `LEAFWIKI_TOTP_ENCRYPTION_KEY` | Key for TOTP secrets at rest | `""` | v0.12.0 |
| `LEAFWIKI_ENABLE_METRICS` | Prometheus `/metrics` | `false` | v0.12.0 |
| `LEAFWIKI_METRICS_HOST` | Metrics listener host | `127.0.0.1` | v0.12.0 |
| `LEAFWIKI_METRICS_PORT` | Metrics listener port | `9091` | v0.12.0 |
| `LEAFWIKI_SNAPSHOT` | Enable snapshot ZIPs | `true` | v0.12.0 |
| `LEAFWIKI_SNAPSHOT_INTERVAL` | Snapshot interval; `0` = manual only | `24h` | v0.12.0 |
| `LEAFWIKI_SNAPSHOT_RETENTION` | Snapshots to keep; `<= 0` = keep all | `10` | v0.12.0 |
| `LEAFWIKI_SNAPSHOT_DIR` | Snapshot directory | `<data-dir>/snapshots` | v0.12.0 |
| `LEAFWIKI_RESTORE_UPLOAD_MAX_SIZE` | Max uploaded backup ZIP | `500MiB` | v0.12.0 |
| `LEAFWIKI_GIT_BACKUP` | Enable git backup | `false` | v0.11.3 |
| `LEAFWIKI_GIT_BACKUP_REMOTE` | Remote URL | `""` | v0.11.3 |
| `LEAFWIKI_GIT_BACKUP_BRANCH` | Branch to push to | `main` | v0.11.3 |
| `LEAFWIKI_GIT_BACKUP_PATH` | Directory holding `root/` and `assets/` | `""` | v0.13.0 |
| `LEAFWIKI_GIT_BACKUP_SSH_KEY` | Raw SSH private key | `""` | v0.11.3 |
| `LEAFWIKI_GIT_BACKUP_SSH_KEY_PATH` | SSH private key file | `""` | v0.11.3 |
| `LEAFWIKI_GIT_BACKUP_SSH_KNOWN_HOSTS` | `known_hosts` file | `""` | v0.11.3 |
| `LEAFWIKI_GIT_BACKUP_HTTP_USERNAME` | HTTP(S) basic auth username | `""` | v0.13.0 |
| `LEAFWIKI_GIT_BACKUP_HTTP_PASSWORD` | HTTP(S) basic auth password or token | `""` | v0.13.0 |
| `LEAFWIKI_GIT_BACKUP_AUTHOR_NAME` | Commit author name | `LeafWiki Backup` | v0.11.3 |
| `LEAFWIKI_GIT_BACKUP_AUTHOR_EMAIL` | Commit author email | `backup@leafwiki.local` | v0.11.3 |
| `LEAFWIKI_GIT_BACKUP_INTERVAL` | Backup interval; `0` = manual only | `60m` | v0.11.3 |
| `LEAFWIKI_SMTP_HOST` | SMTP host; unset disables email | `""` | v0.13.0 |
| `LEAFWIKI_SMTP_PORT` | SMTP port | `587` | v0.13.0 |
| `LEAFWIKI_SMTP_USERNAME` | SMTP auth username | `""` | v0.13.0 |
| `LEAFWIKI_SMTP_PASSWORD` | SMTP auth password | `""` | v0.13.0 |
| `LEAFWIKI_SMTP_FROM` | From address (required with `LEAFWIKI_SMTP_HOST`) | `""` | v0.13.0 |
| `LEAFWIKI_SMTP_FROM_NAME` | From display name | `LeafWiki` | v0.13.0 |
| `LEAFWIKI_SMTP_SECURITY` | `none`, `starttls` or `tls` | `starttls` | v0.13.0 |
| `LEAFWIKI_SMTP_INSECURE_SKIP_VERIFY` | Skip TLS verification | `false` | v0.13.0 |
| `LEAFWIKI_SMTP_TIMEOUT` | Timeout for one SMTP send | `10s` | v0.13.0 |
| `LEAFWIKI_VECTORS` | Semantic search mode | `off` | *fork* |
| `LEAFWIKI_VECTORS_MODEL_DIR` | Encoder model directory | `<data-dir>/models` | *fork* |
| `LEAFWIKI_VECTORS_LIBRARY` | ONNX Runtime shared library | `""` | *fork* |
| `LEAFWIKI_CONTROL_SOCKET` | Agent socket; empty disables the channel | `<data-dir>/control.sock` | *fork* |
| `LEAFWIKI_CONTROL_AUTH` | `optional` or `required` | `optional` | *fork* |
| `LEAFWIKI_AGENT_USER` | Agent CLI: account to authenticate as (read by the CLI, not the server) | `""` | *fork* |
| `LEAFWIKI_AGENT_PASSWORD` | Agent CLI: password for `LEAFWIKI_AGENT_USER` | `""` | *fork* |

### Semantic search settings

- `--vectors=auto` embeds a page after it is saved, in the background. The save response reports
  `vector: "queued"`; `--wait` on the agent CLI polls until the page is `indexed`.
- `--vectors=manual` never queues. Build or refresh with the admin UI or with the CLI.
- `--vectors-model-dir` may point at one model directory or at a parent that holds exactly one
  model. A parent with two or more models is rejected as ambiguous.
- `--vectors-library` is the path to `onnxruntime.dll` (Windows) or `libonnxruntime.so`. The Go
  binding is pinned to ONNX Runtime C API 1.31: another version fails with
  `Error setting ORT API base`.
- `vectors.db` and `models/` are derived state. Snapshots and git backups do not include them.
  A restore drops the index and rebuilds it from the restored pages instead of reusing old
  vectors.
- The similarity floor for a recalled passage is a constant in
  `internal/wiki/vectors/recall.go` (`SemanticMinSimilarity`), not a flag.

---

## Operating modes

LeafWiki has three access modes. Pick the one that matches your environment.

1. **Internal wiki, login required (default).** No flags. Every visit needs an account.
   ```bash
   ./leafwiki --jwt-secret=yoursecret --admin-password=yourpassword
   ```
2. **Public read, login required to edit.** Readers need no account; editors and admins do.
   ```bash
   ./leafwiki --jwt-secret=yoursecret --admin-password=yourpassword --public-access=true
   ```
3. **No authentication (`--disable-auth`).** Anyone who can reach the server can read and write.
   ```bash
   ./leafwiki --disable-auth --host=127.0.0.1
   ```
   Use it on trusted networks or local setups only. Never expose a `--disable-auth` instance to
   the internet. On such an instance nobody can authenticate, so the agent CLI cannot delete.

Roles are admin, editor and viewer. Editing needs editor or admin.

---

## Files on disk and resync

`<data>/root/**` holds the pages as Markdown and is the source of truth. Frontmatter carries the
managed identity keys (`leafwiki_id`, `leafwiki_title`, timestamps, author ids, `leafwiki_aliases`)
and `tags`; any other key is a page property. SQLite files next to it are derived and
rebuildable.

LeafWiki writes those files itself. It does not watch them. If you change Markdown outside the
app (a text editor, a script, a bulk import, a Git pull), tell the running server:

- **Admin UI:** the Refresh button in the page-tree toolbar runs a resync over tree, links, tags,
  search and the semantic index, with live progress.
- **OS signal:** send `SIGUSR1` or `SIGHUP` to the process. Windows has no equivalent signal, so
  use the button there.

A resync is a reconciliation, not continuous two-way synchronization. A Markdown file created
outside the app without a `leafwiki_id` gets one written back during the resync, so the file
changes on disk once. If `root/` is under an external Git workflow, that id appears as a diff.

Do not edit files behind a running server for pages you expect to keep: the server will not see
the change, and the next save from the UI overwrites it. Use the [agent CLI](#agent-access-cli),
which goes through the server.

---

## Security

Enabled by default since v0.7.0:

- Secure, HttpOnly cookies for sessions
- CSRF protection on state-changing requests
- Rate limiting on auth endpoints
- Role-based access: admin, editor, viewer

Notes that matter on this fork:

- `--disable-auth` removes all authentication. Use it for local development or isolated
  networks only.
- The agent control channel is a local socket owned by the data directory's uid, mode `0660`,
  with a peer-uid check where the platform supports it. It issues no token. Reading, creating
  and updating are open to that uid, the same trust level as editing the files; deleting needs
  an editor or admin. `--control-auth=required` closes the channel to anonymous callers.
- `--allow-insecure=true` exists for plain HTTP. Serve HTTPS through a reverse proxy for
  anything that leaves the machine.
- SMTP and Git backup credentials belong in environment variables, not on the command line,
  where the process list shows them.

---

## Development

Stack: Go, React (Vite), SQLite.

```bash
make setup     # npm ci for the frontend and the e2e suite
make dev       # backend and Vite together; Ctrl-C stops both
```

Vite runs on `http://localhost:5173` (login `admin` / `devpassword`) and proxies the API to
`127.0.0.1:8080`. `make doctor` checks the Go and Node versions.

| Task | Command |
|---|---|
| Local subset of CI | `make ci` |
| Backend tests and lint | `make test`, `make lint` |
| Windows and macOS build check | `make cross` |
| Frontend install, lint, format, tests, build | `make ui-check` |
| Launcher compile check | `make verify-launcher` |
| Repository checks | `make verify` |

Tests that need the encoder read `LEAFWIKI_VECTORS_TEST_MODELS_DIR` and
`LEAFWIKI_VECTORS_TEST_LIBRARY`; without them the semantic cases are skipped. Run them with
`CGO_ENABLED=1 go test -tags vectors ./internal/embed`.

The architecture, rules and source map live in [docs/development/](docs/development/README.md).
Agent instructions and the review roles are in [AGENTS.md](AGENTS.md). UI languages are
English, German, Spanish, French and Chinese; [docs/i18n.md](docs/i18n.md) explains how to add
one.

---

## Upstream, license, contributing

- Upstream project: [perber/leafwiki](https://github.com/perber/leafwiki). Its README documents
  the features this fork inherits: install on Linux, Kubernetes, nginx and Raspberry Pi;
  snapshots and restore; Git backup; SMTP email; reverse-proxy authentication; unix sockets;
  keyboard shortcuts; page sorting.
- Fork base: commit `714e0368` on upstream `main`.
- Upstream releases, Docker images, the hosted demo, `install.sh` and `update.sh` belong to
  upstream and do not include this fork's changes.
- License: MIT, see [LICENSE](LICENSE).
- Contribution rules: [CONTRIBUTING.md](CONTRIBUTING.md) and
  [docs/development/rules.md](docs/development/rules.md). Bug reports and pull requests for
  upstream belong upstream; changes that only concern this fork stay here.
