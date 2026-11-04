---
name: leafwiki
description: Read and write pages in a LeafWiki instance from an agent, through the running server's control socket, or offline (writes only) when it is stopped. Covers page get/write/apply/delete, batches, credentials for deleting, the semantic-index wait, check, and the exit codes. Use when asked to add, update or inspect pages of a LeafWiki wiki ("write this page into the wiki", "update the deploy docs", "check the wiki for broken files").
---

# LeafWiki from an agent

LeafWiki keeps Markdown files as the source of truth and derives its indexes from them. Never edit
those files behind a running server: it will not see the change and the next UI save overwrites it.
Use the CLI, which is the same binary as the server.

All commands take `--data-dir` (or `LEAFWIKI_DATA_DIR`). `--json` prints exactly one JSON object;
everything else, including progress and warnings, goes to stderr.

## 1. Look before you write

```bash
leafwiki --data-dir ./data status                      # protocol, pages, vectors state
leafwiki --data-dir ./data page get platform/deploy --json
```

`page get` answers `{path,id,title,slug,kind,version,tags,aliases,properties,content,vectors}`.
`vectors` is that page's own state: `indexed`, `stale`, `missing`, `queued`, `failed` or `off`.

## 2. Write

```bash
leafwiki --data-dir ./data page write platform/deploy --create --title "Deploy" --content-file deploy.md
leafwiki --data-dir ./data page write platform/deploy --content-file - --tags ops,infra < deploy.md
leafwiki --data-dir ./data page apply --batch changes.jsonl --json
```

- `page write` is one item of a `page.apply`; `--batch` reads **one JSON item per line** (JSONL).
- An item is `{path, create?, delete?, title?, tags?, aliases?, properties?, content?, recursive?}`.
  `slug`, `expected_version` and `content_file` are not implemented: sending them fails the batch.
- Writing the same content again is `unchanged`: no revision, no re-index. You do not need to read
  before you write unless you want to know the title.
- Tags, aliases and properties are written **with** the content; metadata without `content` is
  refused because it would be dropped.
- One batch is atomic in its precheck: one bad item refuses the whole batch and writes nothing.
  A failure during execution stops the batch; the items before it are written and reported.

## 3. Delete

```bash
leafwiki --data-dir ./data page delete platform/deploy --dry-run --json   # impact, writes nothing
leafwiki --data-dir ./data page delete platform/deploy --recursive --user admin --password-file ~/.leafwiki-pw
```

Deleting needs an **editor or an admin** and is irreversible (assets, favorites and revisions go
with it). A page with children needs `--recursive`. `--dry-run` reports the subtree, asset count,
favorite count and whether revisions exist.

## 4. Credentials

Reads, creates and updates are open when the server allows anonymous writes
(`--control-auth=optional`, the default). For `delete`, or when the server runs with
`--control-auth=required`, pass `--user` with `--password`, `--password-file` or
`LEAFWIKI_AGENT_USER`/`LEAFWIKI_AGENT_PASSWORD`. **The order is flag > file > environment.** On a
`--disable-auth` instance nobody can authenticate: deletes are impossible there.

An account with TOTP enabled cannot authenticate here (the channel has no second step) and a
locked account stays locked: both answer `-32001` with kind `totp_required` or `account_locked`,
exit `5`. Use an account without TOTP for deletes.

## 5. Wait for the index

When the server runs with semantic search in `auto` mode, a write queues its page and answers
`vector:"queued"`. `--wait` (default 30 s, `--no-wait` to skip) polls until the page is
`indexed`. `manual`, `paused` and `off` instances do not queue. `vectors sync` and
`vectors build` take the data-directory lock, so they exit `75` while the server runs: stop the
server first, or run the instance in `auto` mode if you need the index to follow writes.

## 6. When the server is stopped

The write commands run **offline**: they take `<data>/wiki.lock`, use the server's own use cases,
and record no revisions. **Reads do not**: `page get` and `status` need the running server.
Offline runs do not support `--dry-run`; start the server for that. If `<data>/vectors.db` and
`<data>/models` are present the changed pages are re-embedded, so the result says
`vector:"indexed"`; otherwise it warns and you should run `leafwiki vectors sync` later (with the
server stopped).

## 7. Check the tree

```bash
leafwiki --data-dir ./data check --json        # read-only; writes nothing, takes no lock
```

Findings are `invalid_filename`, `name_conflict`, `duplicate_id`, `unparsable_frontmatter`,
`repaired_frontmatter`, `unreadable_file`, `unreadable_directory`, `ignored`,
`missing_section_index`. Exit `0` clean, `1` something to fix, `3` no data directory.

## 8. Exit codes

`0` ok · `1` unclassified · `2` usage · `3` page not found · `4` version conflict (read again)
· `5` not allowed / not authenticated · `6` unreachable, timeout or restore running · `7`
capability missing · `8` partial batch (some items written) · `75` the data directory is in use.

A refusal (`5`) never means "retry offline": it means someone else may be listening on that socket.
`7` covers both "this server does not implement the method" and "the semantic index could not
embed the page".
`75` means a server or another process owns the directory: ask it to stop, or use the running
server.

## References

- [CLI reference](./references/cli.md): every command, flag and JSON shape.
- [Content rules](./references/content.md): paths, slugs, frontmatter, properties, batch items.
- [Operating the channel](./references/operations.md): the socket, credentials, offline mode, limits.
- [Troubleshooting](./references/troubleshooting.md): exit codes, degraded modes, common refusals.