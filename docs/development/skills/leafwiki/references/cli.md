# CLI reference

Every command accepts `--data-dir` (env `LEAFWIKI_DATA_DIR`). A shipped bundle (`leafwiki.exe`
next to `data/`) needs no path at all: the executable finds that directory itself, and the
launcher writes the same hints into `AGENT.md` beside it. `--json` prints exactly one JSON
object on success and on a partial batch; a command that fails prints nothing on stdout: the
error goes to stderr and the exit code says what happened.

## `status`

```bash
leafwiki --data-dir ./data status [--json] [--control-socket PATH]
```

JSON: `{protocol, capabilities[], auth:{mode,disabled}, vectors:{mode,paused,pending,failed,encoder_available}, pages, indexing:{running}}`.
`capabilities` lists what this server implements: `auth.login`, `page.get` and `page.apply` on a
current build. (`vectors.wait` is part of the vocabulary but is not advertised yet.) `status` never
needs credentials.

## `page get`

```bash
leafwiki --data-dir ./data page get <path> [--id <id>] [--json]
```

JSON: `{path,id,title,slug,kind,version,tags,aliases,properties,content,vectors}`. `version` is an
RFC3339Nano string; it changes on every real write. A missing page is exit `3`
(`page_not_found`).

## `page write`

```bash
leafwiki --data-dir ./data page write <path> \
  [--create] [--title T] [--content-file FILE|-] \
  [--tags a,b] [--aliases a,b] [--property k=v]... \
  [--no-wait] [--wait 30s] [--json] [--user U --password P | --password-file F]
```

One `page.apply` item. Result: `{results:[{path,action,changed,version,vector}]}`.
`action` is `create`, `update` or `unchanged`.

## `page apply`

```bash
leafwiki --data-dir ./data page apply --batch FILE|- [--dry-run] [--no-wait] [--wait 30s] [--json]
```

JSONL: one item object per line, at most 200 items and 8 MiB per batch, at most 1 MiB of content
per item. `--batch -` reads stdin; `page write --content-file -` is the other stdin reader (the two
commands cannot both read stdin).

## `page delete`

```bash
leafwiki --data-dir ./data page delete <path> [--recursive] [--dry-run] [--json] [credentials]
```

`--dry-run` adds `affected[]`, `assets`, `favorites`, `has_revisions` and writes nothing. A real
delete reports `irreversible:true`.

## `check`

```bash
leafwiki --data-dir ./data check [--json] [--strict]
```

JSON: `{pages, problems:[{kind,severity,path,detail,hint}], ignored}`.

## `vectors sync` / `vectors build` / `vectors status`

```bash
leafwiki --data-dir ./data vectors sync [--dry-run] [--paths a,b] [--json]
leafwiki --data-dir ./data vectors build [--full] [path-or-id]
leafwiki --data-dir ./data vectors status [--json]
```

Both `build` and `sync` take `<data>/wiki.lock` first and `<data>/vectors.db.lock` second; while a
server runs they exit `75`. `sync` re-embeds only drifted pages, purges orphans, and works with
`--dry-run` without a model. `status` takes no lock.