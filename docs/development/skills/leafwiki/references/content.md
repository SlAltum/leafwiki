# Content rules

## Paths

A page path is its slug chain: `platform/deploy`. Every segment must match
`^[a-zA-Z0-9]+(-[a-zA-Z0-9]+)*$` and must not be a reserved word:
`e, edit, api, assets, branding, index, users, user, login, history, settings`. `.`, `..` and the
Windows device names (`CON`, `PRN`, `AUX`, `NUL`, `COM1`–`COM9`, `LPT1`–`LPT9`) are refused.

One item creates the intermediate sections: `page write a/b --create` makes `a` a section and
`a/b` a page. Creating `a/b` when `a` already exists as a **page** is refused: that would
restructure it. Create the section first (for example by creating `a/child` directly).

## Files on disk

`<data>/root/<path>.md` for a page, `<data>/root/<path>/index.md` for a section. Order is explicit
(`.order.json`), not alphabetical. A directory and a `.md` file with the same name break the start:
`check` reports it as `name_conflict`.

## Frontmatter

Managed keys (never write them yourself):

```
leafwiki_id, leafwiki_title, leafwiki_created_at, leafwiki_updated_at,
leafwiki_creator_id, leafwiki_last_author_id, leafwiki_pinned, leafwiki_aliases
```

`tags` is a list; every other key is a property shown in the UI. **Tags are lower-cased and
de-duplicated**, so `Kubernetes` is stored as `kubernetes` and writing it again with the original
casing is `unchanged`. `leafwiki_`-prefixed keys and
`tags` are reserved: sending one as a property fails the batch. Properties are written **with**
`content`; an item that changes `tags`/`aliases` but not `properties` keeps the properties the page
already has.

## Items

```json
{"path":"platform/deploy","create":true,"title":"Deploy","tags":["ops"],
 "properties":{"env":"prod"},"content":"# Deploy\n…"}
{"path":"platform/deploy","content":"# Deploy\nnew body"}
{"path":"platform/old","delete":true,"recursive":true}
```

Optional fields are optional: leaving `title` out of an update keeps the current title. Writing the
same content, title, tags, aliases and properties again is `unchanged` and writes nothing.

## Attribution

An anonymous write is attributed to the `system` user (`leafwiki_creator_id`,
`leafwiki_last_author_id`). With credentials the account is recorded: a create sets both fields, an
update only moves the last author.