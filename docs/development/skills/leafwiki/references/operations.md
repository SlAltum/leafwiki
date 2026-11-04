# Operating the channel

## Where it lives

The server opens `<data>/control.sock` (a Unix domain socket) and publishes
`<data>/control.json` with `{socket, protocol}` (mode 0600, removed on exit). The CLI looks for the
socket in this order: `--control-socket`, the descriptor, the default path. A descriptor written by
another user is ignored on Unix; on Windows its socket must at least sit inside the data directory.

`--control-socket=""` starts the server without a channel. A socket that cannot be bound (a long
path on Windows) is a warning; the server keeps serving.

## Trust boundary

- Socket mode 0660 and the data directory's owner.
- The peer uid is checked in both directions on Linux/macOS (`SO_PEERCRED` /
  `LOCAL_PEERCRED`). Windows has no peer credentials: the directory ACL is the boundary.
- There is no token. Anyone who can open the socket is already inside the boundary that protects
  the data directory.

## Authentication

`--control-auth=optional` (default): reading, creating and updating need no credentials;
**deleting needs an editor or an admin**. `--control-auth=required`: everything except `status` and
`auth.login` needs credentials. `--disable-auth` instances have no user store, so nobody can
authenticate and deletes are impossible.

A credential check never creates a session: it does not write `sessions.db`. It shares the login
lockout counter with the web login, so failed attempts count against the account.

## One writer

The server holds `<data>/wiki.lock` for its whole life. A second instance refuses to start, and
`vectors build`/`vectors sync` exit `75` while it runs. When no server runs, the CLI takes that
lock itself and writes offline.

## Offline mode

Writes only: `page get` and `status` need the running server.

Entered only when the channel is **unreachable** and neither `control.json` nor
`launcher.state.json` exists (either one means "a server should be answering" → exit `75`). An
offline run:

- uses the server's own use cases, with no side effects and **no revision recording**;
- does **not** support `--dry-run` (that needs the server);
- re-embeds the changed pages only when `<data>/vectors.db` and `<data>/models` are both present, and
  reports `vector:"indexed"` only for pages it really embedded.

## Limits

Four concurrent connections, 8 MiB per frame, 60 s idle timeout, per connection 20 requests burst
and 10 per second, five login attempts per minute. The write gate of a running restore refuses
writes with `-32002` while it swaps files.